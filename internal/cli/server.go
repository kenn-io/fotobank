package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/gofrs/flock"
	"github.com/spf13/cobra"

	"github.com/wesm/fotobank/internal/album"
	"github.com/wesm/fotobank/internal/auth/hidden"
	"github.com/wesm/fotobank/internal/backup"
	"github.com/wesm/fotobank/internal/config"
	"github.com/wesm/fotobank/internal/db"
	"github.com/wesm/fotobank/internal/httpapi"
	"github.com/wesm/fotobank/internal/identity"
	"github.com/wesm/fotobank/internal/media"
	"github.com/wesm/fotobank/internal/obs"
	"github.com/wesm/fotobank/internal/owners"
	"github.com/wesm/fotobank/internal/service"
	"github.com/wesm/fotobank/internal/service/usersettings"
	"github.com/wesm/fotobank/internal/share"
	"github.com/wesm/fotobank/internal/shareworker"
	"github.com/wesm/fotobank/internal/storage"
	"github.com/wesm/fotobank/internal/thumb"
	"github.com/wesm/fotobank/internal/version"
	"github.com/wesm/fotobank/internal/web"
)

// shutdownTimeout bounds how long graceful shutdown waits for in-flight
// requests to drain before the server forcibly closes connections.
const shutdownTimeout = 30 * time.Second

// flashEvictInterval is how often the background janitor runs after the
// initial startup eviction. One eviction a day keeps the flash footprint
// bounded without thrashing the NAS on every request.
const flashEvictInterval = 24 * time.Hour

func newServerCmd() *cobra.Command {
	var (
		cfgPath string
		listen  string
	)
	cmd := &cobra.Command{
		Use:   "server",
		Short: "Start the fotobank HTTP server",
		Args:  usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runServer(cmd.Context(), serverOpts{
				cfgPath: cfgPath,
				listen:  listen,
				stdout:  cmd.OutOrStdout(),
				stderr:  cmd.ErrOrStderr(),
			})
		},
	}
	cmd.Flags().StringVar(&cfgPath, "config", "", "path to config file (defaults to DefaultConfigPath)")
	cmd.Flags().StringVar(&listen, "listen", "", "override [http].listen_address")
	return cmd
}

type serverOpts struct {
	cfgPath string
	listen  string
	stdout  io.Writer
	stderr  io.Writer
}

// runServer loads config, opens the database, wires the identity provider
// and HTTP handler, binds the configured listen address, and serves until
// ctx is cancelled or the process receives SIGINT/SIGTERM.
func runServer(ctx context.Context, opts serverOpts) error {
	path := opts.cfgPath
	if path == "" {
		path = config.DefaultConfigPath()
	}
	cfg, err := config.Load(path)
	if err != nil {
		return err
	}
	if opts.listen != "" {
		// --listen can turn a loopback header-mode config into a
		// public bind that Validate would have rejected; re-run it so
		// the CLI override stays as strict as the file-only path.
		cfg.HTTP.ListenAddress = opts.listen
		if err := cfg.Validate(); err != nil {
			return err
		}
	}

	logger := obs.NewLogger(obs.LoggerConfig{
		Format:    cfg.Observability.Logging.Format,
		Level:     cfg.Observability.Logging.Level,
		AddSource: cfg.Observability.Logging.AddSource,
	}, opts.stderr)
	// FOTOBANK_LOG_LEVEL bogus values fall through silently inside
	// obs.NewLogger because at construction time there is no logger
	// to emit on. Now that we have one, surface the issue so an
	// operator setting a typoed level isn't left wondering why the
	// override "didn't work".
	if raw := os.Getenv("FOTOBANK_LOG_LEVEL"); raw != "" && !obs.IsValidLogLevel(raw) {
		logger.Warn("FOTOBANK_LOG_LEVEL ignored — not a valid level",
			"raw", raw, "valid", "debug|info|warn|error")
	}
	// Anything still emitting via slog.Default() (third-party libs,
	// pre-T9 helpers we haven't migrated) inherits the configured
	// formatter and level, tagged component=legacy so an operator can
	// see at a glance which lines came through the implicit channel.
	slog.SetDefault(logger.With("component", "legacy"))

	dbPath := resolveDBPath(cfg)

	// Lifetime advisory lock. Refuses two servers on the same DB and
	// blocks `backup restore` while we're running. POSIX advisory locks
	// release automatically on process exit, so a crash does not strand
	// the lock.
	lockPath := lockPathFor(dbPath)
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o700); err != nil {
		return fmt.Errorf("mkdir lock dir: %w", err)
	}
	lockFile := flock.New(lockPath)
	ok, err := lockFile.TryLock()
	if err != nil {
		return fmt.Errorf("acquire lifetime lock: %w", err)
	}
	if !ok {
		return fmt.Errorf("another fotobank process is using %s", dbPath)
	}
	defer func() { _ = lockFile.Unlock() }()

	d, err := db.Open(dbPath)
	if err != nil {
		return err
	}
	defer d.Close()

	ownerSvc := service.NewOwnerService(owners.NewRepo(d.WriteDB(), d.ReadDB()))

	// buildIdentityProvider must run first: stub mode inserts the
	// configured owner row, and the subsequent loadStorageKeys() call
	// needs that row to construct the storage key map.
	idp, err := buildIdentityProvider(ctx, cfg, ownerSvc)
	if err != nil {
		return err
	}

	keys, err := loadStorageKeys(ctx, ownerSvc)
	if err != nil {
		return err
	}
	storeLayer, flashCache := buildStorageLayer(cfg, keys)

	mediaSvc := service.NewMediaService(media.NewRepo(d.WriteDB(), d.ReadDB()), storeLayer)

	// F2.4 Hidden privacy. hiddenRepo and hiddenSvc are wired after
	// mediaSvc because hidden.NewService takes MediaPrivacy which is
	// implemented by *service.MediaService via ClearAllHiddenForOwner.
	hiddenRepo := hidden.NewRepo(d.WriteDB(), d.ReadDB())
	hiddenSvc := hidden.NewService(hiddenRepo, mediaSvc)
	// FOTOBANK_E2E_LOCKOUT_WINDOW shrinks the lockout window and duration
	// so Playwright tests can exercise the rate limiter without real waits.
	// We require FOTOBANK_E2E_MODE=1 alongside it so a production or
	// staging deployment that happens to inherit FOTOBANK_E2E_LOCKOUT_WINDOW
	// from a shared shell profile can never weaken its hidden-auth
	// rate limit. The e2e-server sets both at startup.
	if raw := os.Getenv("FOTOBANK_E2E_LOCKOUT_WINDOW"); raw != "" {
		if os.Getenv("FOTOBANK_E2E_MODE") != "1" {
			logger.Warn("FOTOBANK_E2E_LOCKOUT_WINDOW ignored — FOTOBANK_E2E_MODE=1 not set")
		} else if d, parseErr := time.ParseDuration(raw); parseErr != nil {
			logger.Warn("FOTOBANK_E2E_LOCKOUT_WINDOW ignored — invalid duration",
				"raw", raw, "err", parseErr)
		} else if d <= 0 {
			logger.Warn("FOTOBANK_E2E_LOCKOUT_WINDOW ignored — duration must be positive",
				"raw", raw)
		} else {
			hiddenSvc.SetLockoutForTest(d, d, 5)
		}
	}

	sharesRepo := share.NewRepo(d.WriteDB(), d.ReadDB())
	albumSvc := service.NewAlbumService(
		album.NewRepo(d.WriteDB(), d.ReadDB()),
		media.NewRepo(d.WriteDB(), d.ReadDB()),
		sharesRepo,
		d,
	)
	shareSvc := service.NewShareService(
		sharesRepo,
		album.NewRepo(d.WriteDB(), d.ReadDB()),
		media.NewRepo(d.WriteDB(), d.ReadDB()),
	)

	thumbQueue := thumb.NewQueue(d.WriteDB(), d.ReadDB())
	thumbSvc := service.NewThumbService(
		media.NewRepo(d.WriteDB(), d.ReadDB()),
		thumbQueue,
		storeLayer,
	)

	// metricsObj owns the private VictoriaMetrics set. Pull-source
	// closures resolve at scrape time from the queues/repos already
	// constructed above. Each closure takes a 250ms timeout so a
	// hung DB cannot block the scrape; on error we log warn and
	// report 0 because gauge sources are advisory, not authoritative.
	metricsObj := obs.NewMetrics(obs.MetricSources{
		ThumbQueueDepth: func(state string) int64 {
			ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
			defer cancel()
			n, qerr := thumbQueue.DepthByState(ctx, state)
			if qerr != nil {
				logger.Warn("thumb queue depth source", "state", state, "err", qerr)
				return 0
			}
			return n
		},
		SharePendingByOp: func(op string) int64 {
			ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
			defer cancel()
			n, qerr := sharesRepo.CountPendingByOp(ctx, op)
			if qerr != nil {
				logger.Warn("share pending source", "op", op, "err", qerr)
				return 0
			}
			return n
		},
	}, obs.BuildInfo{
		Version:   version.Short,
		Commit:    version.Commit,
		BuildDate: version.BuildDate,
	})

	// Grantee-side plumbing: display-handle cache, resolver, and
	// SharedReadService. The resolver uses nil for its clock so it
	// defaults to time.Now().UTC; the display cache middleware is
	// driven off the same repo in httpapi.New when PrincipalDisplay is
	// non-nil.
	displayRepo := share.NewPrincipalDisplayRepo(d.WriteDB(), d.ReadDB())
	resolver := share.NewScopeResolver(sharesRepo, nil, logger.With("component", "share"))
	sharedSvc := service.NewSharedReadService(
		sharesRepo,
		media.NewRepo(d.WriteDB(), d.ReadDB()),
		album.NewRepo(d.WriteDB(), d.ReadDB()),
		storeLayer,
		resolver,
	)

	usersettingsSvc := usersettings.NewService(usersettings.NewRepo(d.WriteDB(), d.ReadDB()))
	eventBus := httpapi.NewEventBus()

	apiHandler, err := httpapi.New(httpapi.Deps{
		IdentityProvider: idp,
		OwnerService:     ownerSvc,
		MediaService:     mediaSvc,
		AlbumService:     albumSvc,
		ThumbService:     thumbSvc,
		ShareService:     shareSvc,
		SharedRead:       sharedSvc,
		UserSettings:     usersettingsSvc,
		EventBus:         eventBus,
		PrincipalDisplay: displayRepo,
		HiddenAuth:       hiddenSvc,
		// DevInsecureHiddenCookies enables the dev cookie name and omits
		// the Secure flag. Only ever true when the operator has explicitly
		// set http.dev_insecure_cookies in the config — never sniffed from
		// the listen address.
		DevInsecureHiddenCookies: cfg.HTTP.DevInsecureCookies,
		Logger:                   logger,
		Metrics:                  metricsObj,
		// RequestIDHeader is only honored in header mode. In stub mode
		// the config defaulting still populates the field, but stub
		// identity does not enroll a trusted upstream proxy, so a
		// caller-supplied value would let any client control the
		// server-issued X-Request-ID and request-scoped log lines.
		RequestIDHeader: requestIDHeaderFor(cfg),
	})
	if err != nil {
		return err
	}

	// Outer mux: /api/* goes through the full httpapi handler (huma
	// routes, raw-mux byte streams, identity + metrics + request-id
	// middleware). Every other path falls through to the embedded SPA
	// handler so a single binary serves both the API and the frontend
	// shell. ServeMux uses longest-prefix match, so "/api/" wins over
	// "/" when both are registered. web.Handler() is constructed once
	// at startup; the embed.FS scoping inside it is cached for the
	// lifetime of the returned handler.
	rootMux := http.NewServeMux()
	rootMux.Handle("/api/", apiHandler)
	rootMux.Handle("/", web.Handler())
	handler := http.Handler(rootMux)

	ln, err := bindListener(cfg.HTTP.ListenAddress)
	if err != nil {
		return err
	}
	if sink := os.Getenv("FOTOBANK_TEST_LISTEN_ADDR_SINK"); sink != "" {
		if werr := os.WriteFile(sink, []byte(ln.Addr().String()), 0o600); werr != nil {
			fmt.Fprintln(opts.stderr, "test sink write failed:", werr)
		}
	}
	fmt.Fprintln(opts.stdout, "fotobank server listening on", ln.Addr())

	srv := &http.Server{
		Handler:      handler,
		ReadTimeout:  cfg.HTTP.RequestTimeout,
		WriteTimeout: cfg.HTTP.WriteTimeout,
	}

	// Build the broker client before spawning bgWG-tracked workers.
	// An error here must short-circuit with a bare return, which only
	// fires d.Close — there are no running goroutines to join yet.
	brokerClient, err := newBrokerClient(cfg.Broker, logger.With("component", "broker"))
	if err != nil {
		return fmt.Errorf("broker init: %w", err)
	}

	sigCtx, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Bind the admin listener BEFORE any bgWG-tracked goroutine is
	// spawned, so a port-conflict (or other bind failure) on the admin
	// port surfaces as a bare return — there are no workers to join,
	// and the deferred d.Close cannot race a mid-flight DB caller. The
	// admin Serve goroutine starts later, after all workers are alive.
	//
	// adminDone is closed by the admin goroutine when Serve returns,
	// signalling shutdown completion. adminFatal carries an unexpected
	// Serve error (anything other than ErrServerClosed) so the main
	// select can treat an admin-side failure as fatal — without it, a
	// transient admin crash would leave the server running silently
	// without /metrics or /readyz. adminFatal stays nil when admin is
	// disabled so its select case is permanently un-selectable.
	ready := obs.NewReady()
	var adminLn net.Listener
	var adminFatal chan error
	adminDone := make(chan struct{})
	if cfg.Observability.AdminEnabled {
		var berr error
		adminLn, berr = bindListener(cfg.Observability.AdminListen)
		if berr != nil {
			return fmt.Errorf("bind admin listener: %w", berr)
		}
		// FOTOBANK_TEST_ADMIN_ADDR_SINK lets e2e tests discover the
		// post-bind address when admin_listen is "127.0.0.1:0".
		if sink := os.Getenv("FOTOBANK_TEST_ADMIN_ADDR_SINK"); sink != "" {
			if werr := os.WriteFile(sink, []byte(adminLn.Addr().String()), 0o600); werr != nil {
				fmt.Fprintln(opts.stderr, "admin sink write failed:", werr)
			}
		}
		adminFatal = make(chan error, 1)
	} else {
		// Closed channel makes the final wait a no-op so callers don't
		// need to special-case the disabled path.
		close(adminDone)
	}

	// bgWG joins every goroutine that holds references to d (SQL) or
	// storeLayer so runServer does not return — and thus `defer d.Close`
	// does not fire — until all of them have observed sigCtx.Done and
	// exited. Without this, a per-claim processOne goroutine mid-SQL
	// would race against d.Close, surfacing as "sql: database is closed"
	// log spam or WAL corruption.
	var bgWG sync.WaitGroup

	// Run one eviction synchronously before Serve so a freshly booted
	// server with a stale flash cache doesn't wait a full interval for
	// cleanup, and so a fatal bug in Evict is visible at boot.
	if flashCache != nil {
		if err := flashCache.Evict(sigCtx); err != nil {
			fmt.Fprintln(opts.stderr, "initial flash eviction failed:", err)
		}
		bgWG.Go(func() {
			runFlashJanitor(sigCtx, flashCache, opts.stderr)
		})
	}

	// Hidden session sweeper: expires old sessions and purges stale
	// failure-log rows every 5 minutes. The interval is overridable via
	// FOTOBANK_TEST_HIDDEN_SWEEP_INTERVAL so e2e tests can observe sweeps
	// without waiting 5 minutes.
	hiddenSweepInterval := 5 * time.Minute
	if raw := os.Getenv("FOTOBANK_TEST_HIDDEN_SWEEP_INTERVAL"); raw != "" {
		if dur, err := time.ParseDuration(raw); err == nil && dur > 0 {
			hiddenSweepInterval = dur
		} else if err != nil {
			fmt.Fprintf(opts.stderr, "FOTOBANK_TEST_HIDDEN_SWEEP_INTERVAL parse error: %v\n", err)
		}
	}
	bgWG.Go(func() {
		runHiddenSweeper(sigCtx, hiddenSvc, hiddenSweepInterval, opts.stderr)
	})

	thumbWorker := thumb.NewWorker(thumbQueue, storeLayer, thumb.Config{
		WorkerConcurrency: cfg.Thumbs.WorkerConcurrency,
		PollInterval:      cfg.Thumbs.PollInterval,
		LeaseTimeout:      cfg.Thumbs.LeaseTimeout,
		Logger:            logger.With("component", "thumb"),
		Metrics:           metricsObj,
	})
	bgWG.Go(func() {
		if err := thumbWorker.Run(sigCtx); err != nil && !errors.Is(err, context.Canceled) {
			fmt.Fprintln(opts.stderr, "thumb worker exited:", err)
		}
	})

	shareCfg := shareworker.Config{
		Repo:    sharesRepo,
		Broker:  brokerClient,
		Logger:  logger.With("component", "share"),
		Metrics: metricsObj,
	}
	// FOTOBANK_TEST_SHARE_WORKER_TICK is a test-only escape hatch that
	// overrides the default 15s tick so e2e tests can observe state
	// transitions within a few seconds.
	if raw := os.Getenv("FOTOBANK_TEST_SHARE_WORKER_TICK"); raw != "" {
		if dur, err := time.ParseDuration(raw); err == nil {
			shareCfg.Tick = dur
		} else {
			fmt.Fprintf(opts.stderr, "FOTOBANK_TEST_SHARE_WORKER_TICK parse error: %v\n", err)
		}
	}
	shareW := shareworker.New(shareCfg)
	bgWG.Go(func() {
		if err := shareW.Run(sigCtx); err != nil && !errors.Is(err, context.Canceled) {
			fmt.Fprintln(opts.stderr, "share worker exited:", err)
		}
	})

	// backupDir is captured outside the cfg.Backup.Enabled block so the
	// admin listener's snapshot_dir readyz check can refer to it. The
	// helper itself short-circuits to a no-op when backups are disabled,
	// so an empty value here is harmless.
	backupDir := backupDirFor(cfg)
	if cfg.Backup.Enabled {
		// Pre-create the snapshot dir at boot so /readyz's snapshot_dir
		// probe doesn't report 503 during the window between server
		// start and the worker's first 15-minute tick. The retention
		// worker would otherwise create it lazily on first Snapshot.
		if err := os.MkdirAll(backupDir, 0o700); err != nil {
			return fmt.Errorf("create backup dir: %w", err)
		}
		interval := 15 * time.Minute
		if raw := os.Getenv("FOTOBANK_TEST_BACKUP_INTERVAL"); raw != "" {
			dur, err := time.ParseDuration(raw)
			switch {
			case err != nil:
				fmt.Fprintf(opts.stderr, "FOTOBANK_TEST_BACKUP_INTERVAL parse error: %v\n", err)
			case dur <= 0:
				// Non-positive durations would panic time.NewTicker;
				// fall back to the production cadence.
				fmt.Fprintf(opts.stderr,
					"FOTOBANK_TEST_BACKUP_INTERVAL must be positive, got %s; using default %s\n",
					dur, interval)
			default:
				interval = dur
			}
		}
		bw := backup.NewWorker(backup.Config{
			DB:       d.WriteDB(),
			Dir:      backupDir,
			Interval: interval,
			Policy: backup.Policy{
				Keep15Min:  cfg.Backup.Keep15Min,
				KeepHourly: cfg.Backup.KeepHourly,
				KeepDaily:  cfg.Backup.KeepDaily,
			},
			Logger:  logger.With("component", "backup"),
			Metrics: metricsObj,
		})
		bgWG.Go(func() {
			if err := bw.Run(sigCtx); err != nil && !errors.Is(err, context.Canceled) {
				fmt.Fprintln(opts.stderr, "backup worker exited:", err)
			}
		})
	}

	// Start the admin Serve goroutine on its own goroutine OUTSIDE
	// bgWG. The listener was already bound earlier in runServer to
	// fail-fast on a port conflict; this point only constructs the
	// mux and starts serving. Shutdown is sequenced so admin remains
	// reachable while the main API drains: ready=false → main API
	// Shutdown → bgWG.Wait → admin Shutdown.
	var adminSrv *http.Server
	if adminLn != nil {
		adminMux := obs.NewAdminMux(obs.AdminConfig{
			Metrics: metricsObj,
			Ready:   ready,
			Checks: []obs.ReadyCheck{
				{
					Name: "db_ping",
					Fn: func(ctx context.Context) error {
						return d.WriteDB().PingContext(ctx)
					},
				},
				obsBackupCheck(cfg, backupDir),
				{
					Name: "nas_root",
					Fn: func(_ context.Context) error {
						_, err := os.Stat(cfg.NAS.Root)
						return err
					},
				},
			},
			ReadyzCfg: obs.ReadyzConfig{
				DeadlineTotal: 2 * time.Second,
				CacheTTL:      5 * time.Second,
			},
			PprofEnabled: cfg.Observability.PprofEnabled,
		})
		adminSrv = &http.Server{Handler: adminMux}
		go func() {
			defer close(adminDone)
			if err := adminSrv.Serve(adminLn); err != nil && !errors.Is(err, http.ErrServerClosed) {
				adminFatal <- err
			}
		}()
	}

	serveErr := make(chan error, 1)
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErr <- err
			return
		}
		serveErr <- nil
	}()

	// shutdownAdmin tears down the admin listener with a bounded
	// timeout so a misbehaving long-lived scraper cannot hold the
	// process open. Mirrors the main API's shutdownTimeout for
	// symmetry.
	shutdownAdmin := func() {
		if adminSrv == nil {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		_ = adminSrv.Shutdown(ctx)
		<-adminDone
	}

	select {
	case err := <-serveErr:
		// Serve exited on its own (bind loss, unrecoverable error).
		// Cancel sigCtx so background workers unwind, then join them
		// before returning so deferred d.Close cannot race.
		ready.Store(false)
		stop()
		bgWG.Wait()
		shutdownAdmin()
		return err
	case err := <-adminFatal:
		// Admin Serve crashed unexpectedly (e.g. listener died after
		// boot). Without surfacing this, the server would keep running
		// invisibly without /metrics or /readyz — operators wouldn't
		// notice until next deploy. Treat as fatal: drain main API,
		// join workers, return the error.
		logger.Error("admin listener exited", "err", err)
		ready.Store(false)
		stop()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		if serr := srv.Shutdown(shutdownCtx); serr != nil {
			_ = srv.Close()
		}
		<-serveErr
		bgWG.Wait()
		<-adminDone
		return fmt.Errorf("admin listener: %w", err)
	case <-sigCtx.Done():
		// 1. Flip readiness false so /readyz returns 503 — load
		//    balancers see "shutting down" before requests start
		//    failing.
		ready.Store(false)
		// 2. Drain the main API listener.
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			// Shutdown timed out or errored; force-close so in-flight
			// connections are torn down before deferred db.Close
			// runs. Drain the serve goroutine to avoid a leak.
			_ = srv.Close()
			<-serveErr
			// Must join before returning: deferred d.Close runs as
			// soon as runServer returns, and a mid-flight processOne
			// must not hit a closed DB handle.
			bgWG.Wait()
			shutdownAdmin()
			return err
		}
		<-serveErr
		// 3. Drain workers (must join before returning: see above).
		bgWG.Wait()
		// 4. Shut down admin LAST so /readyz stays scrapeable through
		//    the bgWG drain.
		shutdownAdmin()
		return nil
	}
}

// obsBackupCheck builds the snapshot_dir readyz probe. When backups
// are disabled the probe is a no-op so the admin listener doesn't
// fail readiness on a path the operator never asked us to maintain.
// When enabled, it round-trips a probe file through the snapshot dir
// to verify both directory existence and write permission — the
// retention worker hits both as part of its tick, so a passing probe
// proves the worker would also succeed.
func obsBackupCheck(cfg *config.Config, dir string) obs.ReadyCheck {
	if !cfg.Backup.Enabled {
		return obs.ReadyCheck{
			Name: "snapshot_dir",
			Fn:   func(context.Context) error { return nil },
		}
	}
	return obs.ReadyCheck{
		Name: "snapshot_dir",
		Fn: func(_ context.Context) error {
			// O_TRUNC (not O_EXCL): a leftover .readyz-probe from a
			// crashed prior check would otherwise wedge readiness in
			// permanent failure. The probe is a write-permission test,
			// not a uniqueness contract.
			probe := filepath.Join(dir, ".readyz-probe")
			f, err := os.OpenFile(probe, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
			if err != nil {
				return err
			}
			_ = f.Close()
			return os.Remove(probe)
		},
	}
}

// bindListener dispatches on the "unix:" prefix: addresses starting
// with "unix:" bind a Unix domain socket; everything else is treated
// as a host:port TCP bind. This matches the validator in internal/config.
// requestIDHeaderFor returns the inbound request-ID header name only
// when identity is in header mode, where a trusted upstream proxy is
// the source of the ID. In stub mode, no proxy is enrolled, so any
// caller-supplied value would let an arbitrary client control the
// server-issued X-Request-ID and request-scoped log fields. Returning
// "" in stub mode forces the middleware to generate a fresh UUID per
// request.
func requestIDHeaderFor(cfg *config.Config) string {
	if cfg.Identity.Mode == "header" {
		return cfg.Identity.Header.RequestIDHeader
	}
	return ""
}

func bindListener(addr string) (net.Listener, error) {
	if after, ok := strings.CutPrefix(addr, "unix:"); ok {
		return net.Listen("unix", after)
	}
	return net.Listen("tcp", addr)
}

// buildIdentityProvider selects the identity provider implementation that
// matches cfg.Identity.Mode. In stub mode it also ensures the owners row
// for the configured principal exists.
func buildIdentityProvider(
	ctx context.Context,
	cfg *config.Config,
	ownerSvc *service.OwnerService,
) (identity.Provider, error) {
	switch cfg.Identity.Mode {
	case "stub":
		p := owners.Principal{
			Hub:    cfg.Identity.Stub.Hub,
			UserID: cfg.Identity.Stub.UserID,
		}
		storageKey := cfg.Identity.Stub.StorageKey
		if storageKey == "" {
			storageKey = cfg.Identity.Stub.UserID
		}
		if err := ownerSvc.Ensure(ctx, p, storageKey); err != nil {
			return nil, err
		}
		return identity.NewStub(p, cfg.Identity.Stub.Handle), nil
	case "header":
		guard := identity.NewGuard(identity.GuardConfig{
			ListenAddress:     cfg.HTTP.ListenAddress,
			TrustedProxyCIDRs: cfg.Identity.Header.TrustedProxyCIDRs,
			ProxySecretHeader: cfg.Identity.Header.ProxySecretHeader,
			ProxySecret:       cfg.Identity.Header.ProxySecret,
			ProxyMTLSCAFile:   cfg.Identity.Header.ProxyMTLSCAFile,
		})
		return identity.NewHeader(identity.HeaderConfig{
			UserIDHeader:    cfg.Identity.Header.UserIDHeader,
			HubHeader:       cfg.Identity.Header.HubHeader,
			HandleHeader:    cfg.Identity.Header.HandleHeader,
			ScopesHeader:    cfg.Identity.Header.ScopesHeader,
			RequestIDHeader: cfg.Identity.Header.RequestIDHeader,
		}, guard), nil
	default:
		return nil, fmt.Errorf("unknown identity mode %q", cfg.Identity.Mode)
	}
}

// loadStorageKeys reads every registered owner and returns the map that
// the storage layer uses to resolve per-owner filesystem prefixes. The
// map is a snapshot: owners added after server start are not visible
// until the server is restarted (Plan B accepts this; dynamic refresh
// lands in Plan D).
func loadStorageKeys(ctx context.Context, ownerSvc *service.OwnerService) (map[owners.Principal]string, error) {
	list, err := ownerSvc.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("load owners: %w", err)
	}
	keys := make(map[owners.Principal]string, len(list))
	for _, o := range list {
		keys[o.Principal] = o.StorageKey
	}
	return keys, nil
}

// flashCacheSubdir is the subdirectory of cfg.Flash.Root that holds
// cached originals. Isolating the cache from cfg.Flash.Root keeps the
// FlashCache janitor (which walks its root and deletes stale entries)
// from ever touching sibling state files such as the sqlite DB, WAL,
// or shm files that live directly under cfg.Flash.Root.
const flashCacheSubdir = "originals"

// flashThumbsSubdir is the sibling subdirectory of flashCacheSubdir that
// holds cached thumbnail bytes. Kept adjacent to flashCacheSubdir so the
// FlashCache directory layout lives in one place.
const flashThumbsSubdir = "thumbs"

// buildStorageLayer assembles the Store implementation dictated by
// cfg.Storage.Mode. When mode is "flash_cache" the returned *FlashCache
// is non-nil so the caller can drive its daily janitor; otherwise it's
// nil and the NAS-only Store is returned. cfg.Validate already rejects
// unknown modes, so the default branch here is defensive.
func buildStorageLayer(cfg *config.Config, keys map[owners.Principal]string) (storage.Store, *storage.FlashCache) {
	nasStore := storage.NewNASOnly(cfg.NAS.Root, keys)
	if cfg.Storage.Mode != "flash_cache" {
		return nasStore, nil
	}
	cacheRoot := filepath.Join(cfg.Flash.Root, flashCacheSubdir)
	fc := storage.NewFlashCache(nasStore, cacheRoot, keys, storage.FlashCacheOptions{
		OriginalsCacheDays:     cfg.Storage.OriginalsCacheDays,
		OriginalsCacheMaxMedia: cfg.Storage.OriginalsCacheMaxMedia,
	})
	if cfg.Storage.ThumbsCacheEnabled {
		thumbsCacheRoot := filepath.Join(cfg.Flash.Root, flashThumbsSubdir)
		fc.EnableThumbs(thumbsCacheRoot)
	}
	return fc, fc
}

// runFlashJanitor drives FlashCache.Evict on a fixed interval until
// ctx is cancelled. Eviction errors are logged to stderr rather than
// fatal — a transient filesystem hiccup should not crash the server.
func runFlashJanitor(ctx context.Context, fc *storage.FlashCache, stderr io.Writer) {
	ticker := time.NewTicker(flashEvictInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := fc.Evict(ctx); err != nil {
				fmt.Fprintln(stderr, "flash eviction failed:", err)
			}
		}
	}
}

// runHiddenSweeper calls Sweep on the hidden service every interval
// until ctx is cancelled. Sweep errors are logged to stderr but do not
// crash the server — a transient DB hiccup should not block the auth
// surface; expired sessions are harmless until the next sweep succeeds.
func runHiddenSweeper(
	ctx context.Context,
	svc *hidden.Service,
	interval time.Duration,
	stderr io.Writer,
) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := svc.Sweep(ctx); err != nil {
				fmt.Fprintln(stderr, "hidden sweep failed:", err)
			}
		}
	}
}
