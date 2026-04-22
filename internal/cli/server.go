package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/wesm/fotobank/internal/config"
	"github.com/wesm/fotobank/internal/db"
	"github.com/wesm/fotobank/internal/httpapi"
	"github.com/wesm/fotobank/internal/identity"
	"github.com/wesm/fotobank/internal/owners"
	"github.com/wesm/fotobank/internal/service"
)

// shutdownTimeout bounds how long graceful shutdown waits for in-flight
// requests to drain before the server forcibly closes connections.
const shutdownTimeout = 30 * time.Second

// runServer loads config, opens the database, wires the identity provider
// and HTTP handler, binds the configured listen address, and serves until
// ctx is cancelled or the process receives SIGINT/SIGTERM. It returns 0 on
// graceful shutdown, 1 on config, database, identity, or listener errors,
// and 2 on flag-parse errors.
func runServer(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("server", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	cfgPath := fs.String("config", config.DefaultConfigPath(), "path to config file")
	listen := fs.String("listen", "", "override [http].listen_address")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if *listen != "" {
		cfg.HTTP.ListenAddress = *listen
	}

	dbPath := os.Getenv("FOTOBANK_DB_PATH")
	if dbPath == "" {
		dbPath = filepath.Join(cfg.Flash.Root, "fotobank.sqlite")
	}
	d, err := db.Open(dbPath)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer d.Close()

	ownerSvc := service.NewOwnerService(owners.NewRepo(d.WriteDB(), d.ReadDB()))

	idp, code := buildIdentityProvider(ctx, cfg, ownerSvc, stderr)
	if code != 0 {
		return code
	}

	handler, err := httpapi.New(httpapi.Deps{
		IdentityProvider: idp,
		OwnerService:     ownerSvc,
	})
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}

	ln, err := net.Listen("tcp", cfg.HTTP.ListenAddress)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if sink := os.Getenv("FOTOBANK_TEST_LISTEN_ADDR_SINK"); sink != "" {
		if werr := os.WriteFile(sink, []byte(ln.Addr().String()), 0o600); werr != nil {
			fmt.Fprintln(stderr, "test sink write failed:", werr)
		}
	}
	fmt.Fprintln(stdout, "fotobank server listening on", ln.Addr())

	srv := &http.Server{
		Handler:      handler,
		ReadTimeout:  cfg.HTTP.RequestTimeout,
		WriteTimeout: cfg.HTTP.WriteTimeout,
	}

	sigCtx, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	serveErr := make(chan error, 1)
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErr <- err
			return
		}
		serveErr <- nil
	}()

	select {
	case err := <-serveErr:
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		return 0
	case <-sigCtx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		return 0
	}
}

// buildIdentityProvider selects the identity provider implementation that
// matches cfg.Identity.Mode. In stub mode it also ensures the owners row
// for the configured principal exists. It returns (provider, 0) on success
// or (nil, 1) after writing a diagnostic to stderr.
func buildIdentityProvider(
	ctx context.Context,
	cfg *config.Config,
	ownerSvc *service.OwnerService,
	stderr io.Writer,
) (identity.Provider, int) {
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
			fmt.Fprintln(stderr, err)
			return nil, 1
		}
		return identity.NewStub(p, cfg.Identity.Stub.Handle), 0
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
		}, guard), 0
	default:
		fmt.Fprintf(stderr, "unknown identity mode %q\n", cfg.Identity.Mode)
		return nil, 1
	}
}
