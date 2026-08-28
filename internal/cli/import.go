package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"go.kenn.io/fotobank/internal/ai/jobs"
	airuntime "go.kenn.io/fotobank/internal/ai/runtime"
	"go.kenn.io/fotobank/internal/ai/skipped"
	appsettingsstore "go.kenn.io/fotobank/internal/appsettings"
	"go.kenn.io/fotobank/internal/config"
	"go.kenn.io/fotobank/internal/content"
	"go.kenn.io/fotobank/internal/db"
	"go.kenn.io/fotobank/internal/errs"
	"go.kenn.io/fotobank/internal/geo"
	"go.kenn.io/fotobank/internal/ingest"
	"go.kenn.io/fotobank/internal/media"
	"go.kenn.io/fotobank/internal/owners"
	"go.kenn.io/fotobank/internal/service"
)

// newImportCmd wires the `fotobank import` subcommand. It takes a single
// positional source directory and runs the ingest importer against the
// currently configured Docbank vault and media repo.
func newImportCmd() *cobra.Command {
	var (
		cfgPath string
		workers int
		wait    time.Duration
	)
	cmd := &cobra.Command{
		Use:   "import <source-dir>",
		Short: "Import media from a source directory into fotobank",
		Args:  usageArgs(cobra.ExactArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runImport(cmd.Context(), importOpts{
				cfgPath: cfgPath,
				source:  args[0],
				workers: workers,
				wait:    wait,
				stdout:  cmd.OutOrStdout(),
				stderr:  cmd.ErrOrStderr(),
			})
		},
	}
	cmd.Flags().StringVar(&cfgPath, "config", "", "path to config file (defaults to DefaultConfigPath)")
	cmd.Flags().IntVar(&workers, "workers", 0, "import worker count (0 uses [imports].concurrent_workers)")
	cmd.Flags().DurationVar(&wait, "wait", 0, "max wait for the import lock before failing (0 = fail fast)")
	return cmd
}

type importOpts struct {
	cfgPath string
	source  string
	workers int
	wait    time.Duration
	stdout  io.Writer
	stderr  io.Writer
}

// runImport loads the config, opens the DB, resolves the owner from the
// stub-identity config, acquires the import file lock, and drives
// ingest.Importer.ImportDirectory. A non-empty Result.Failures causes a
// non-nil return so the CLI exits with code 1.
func runImport(ctx context.Context, opts importOpts) error {
	path := opts.cfgPath
	if path == "" {
		path = config.DefaultConfigPath()
	}
	cfg, err := config.Load(path)
	if err != nil {
		return err
	}
	if cfg.Identity.Mode != "stub" {
		return fmt.Errorf("fotobank import requires identity.mode = stub (got %q)", cfg.Identity.Mode)
	}

	dbPath := os.Getenv("FOTOBANK_DB_PATH")
	if dbPath == "" {
		dbPath = filepath.Join(cfg.Flash.Root, "fotobank.sqlite")
	}
	// Print resolved paths up front so a user with a misconfigured root
	// (e.g. an unexpanded "~" or a typo) sees IMMEDIATELY where bytes
	// will land, instead of discovering minutes later that the import
	// landed in the wrong directory. Source path is the canonicalized
	// form of the user's argument.
	absSource, _ := filepath.Abs(opts.source)
	fmt.Fprintf(opts.stdout, "source:    %s\n", absSource)
	fmt.Fprintf(opts.stdout, "docbank:   %s\n", cfg.Docbank.Root)
	fmt.Fprintf(opts.stdout, "flash db:  %s\n", dbPath)
	d, err := db.Open(dbPath)
	if err != nil {
		return err
	}
	defer d.Close()
	appSettingsRepo := appsettingsstore.NewRepo(d.WriteDB(), d.ReadDB())
	aiProvider, err := airuntime.NewProvider(ctx, airuntime.Source{
		FilePath: path,
		Repo:     appSettingsRepo,
	})
	if err != nil {
		return fmt.Errorf("load effective ai config: %w", err)
	}
	aiSnap := aiProvider.Effective()
	cfg.AI = aiSnap.Config

	owner := owners.Principal{
		Hub:    cfg.Identity.Stub.Hub,
		UserID: cfg.Identity.Stub.UserID,
	}
	ownerSvc := service.NewOwnerService(owners.NewRepo(d.WriteDB(), d.ReadDB()))
	registeredOwner, err := ownerSvc.Ensure(ctx, owner, cfg.Identity.Stub.StorageKey)
	if err != nil {
		return err
	}

	repo := media.NewRepo(d.WriteDB(), d.ReadDB())
	assetRepo := media.NewAssetRepo(d.WriteDB(), d.ReadDB())
	contentStore, err := content.Open(ctx, content.Config{Root: cfg.Docbank.Root})
	if err != nil {
		return fmt.Errorf("open Docbank vault: %w", err)
	}
	defer contentStore.Close()

	lockPath := cfg.Imports.FileLockPath
	if lockPath == "" {
		lockPath = filepath.Join(cfg.Flash.Root, ".fotobank", "import.lock")
	}
	unlock, err := ingest.Acquire(ctx, lockPath, opts.wait)
	if err != nil {
		if errors.Is(err, errs.ErrConcurrentImport) {
			return fmt.Errorf("another import is in progress (lock: %s)", lockPath)
		}
		return err
	}
	defer unlock()

	workers := opts.workers
	if workers <= 0 {
		workers = cfg.Imports.ConcurrentWorkers
	}

	places, err := geo.NewNaturalEarth()
	if err != nil {
		return fmt.Errorf("load geo gazetteer: %w", err)
	}
	imp := ingest.NewImporter(contentStore, assetRepo, repo, registeredOwner.StorageKey, places)

	// Wire the production AIEnqueuer so an offline import auto-enqueues
	// for AI processing. Queue rows use runtime claim fingerprints so
	// server workers claim the same settings identity that admin Apply
	// publishes.
	aiQueue := jobs.NewQueue(d.WriteDB(), d.ReadDB())
	aiSkippedRepo := skipped.NewRepo(d.WriteDB(), d.ReadDB())
	enq := ingest.NewRealAIEnqueuer(
		aiSnap.Result.Tag, aiSnap.Result.Caption,
		aiQueue.Enqueue,
		aiSkippedRepo.Record,
	).WithClaimFingerprints(aiSnap.Claim.Tag, aiSnap.Claim.Caption, aiQueue.EnqueueClaim)
	if cfg.AI.Embed.Enabled {
		enq.WithEmbedClaim(aiSnap.Result.Embed, aiSnap.Claim.Embed)
	}
	imp.SetAIEnqueuer(enq)

	progress := newImportProgress(opts.stdout)
	res, err := imp.ImportDirectory(ctx, opts.source, ingest.Options{
		Owner:             owner,
		ConcurrentWorkers: workers,
		SettleInterval:    cfg.Imports.SettleInterval,
		Progress:          progress.handle,
	})
	if err != nil {
		return err
	}
	progress.finish()

	fmt.Fprintf(opts.stdout, "imported=%d\tduplicates=%d\tconflicts=%d\tfailures=%d\n",
		res.Imported, res.Duplicates, res.Conflicts, len(res.Failures))

	if len(res.Failures) > 0 {
		for _, f := range res.Failures {
			fmt.Fprintln(opts.stderr, f)
		}
		return fmt.Errorf("import completed with %d failure(s)", len(res.Failures))
	}
	return nil
}

// importProgress prints live progress for `fotobank import`. On a TTY
// it refreshes a single line via carriage return and rate-limits paints
// to ~10 Hz so a fast import doesn't drown stdout. When stdout is piped
// (CI, tee'd to a file) it falls back to a one-line-per-50-files
// summary so logs stay readable. handle and finish are safe to call in
// any order — finish is a no-op if no Progress events arrived.
type importProgress struct {
	w         io.Writer
	tty       bool
	lastLen   int
	lastTick  time.Time
	announced bool
}

func newImportProgress(w io.Writer) *importProgress {
	return &importProgress{w: w, tty: isTerminal(w)}
}

func (p *importProgress) handle(ev ingest.ProgressEvent) {
	// Discovery announcement (Done=0) — print once unconditionally so the
	// user knows discovery completed and how big the run is.
	if ev.Done == 0 && !p.announced {
		p.announced = true
		fmt.Fprintf(p.w, "Discovered %d candidate(s). Importing…\n", ev.Total)
		return
	}
	if p.tty {
		// Rate-limit TTY paints; always paint the final event.
		now := time.Now()
		if ev.Done < ev.Total && now.Sub(p.lastTick) < 100*time.Millisecond {
			return
		}
		p.lastTick = now
		line := formatProgressLine(ev)
		// Pad to last length so a shrinking line doesn't leave residue
		// (file-name shorter than the previous one).
		pad := ""
		if n := p.lastLen - len(line); n > 0 {
			pad = pad + spaces(n)
		}
		fmt.Fprintf(p.w, "\r%s%s", line, pad)
		p.lastLen = len(line)
		return
	}
	// Non-TTY: emit a line every 50 candidates and on the final event.
	if ev.Done%50 != 0 && ev.Done != ev.Total {
		return
	}
	fmt.Fprintln(p.w, formatProgressLine(ev))
}

func (p *importProgress) finish() {
	if p.tty && p.lastLen > 0 {
		fmt.Fprintln(p.w)
		p.lastLen = 0
	}
}

func formatProgressLine(ev ingest.ProgressEvent) string {
	name := filepath.Base(ev.Path)
	return fmt.Sprintf(
		"  %d/%d · imported=%d dup=%d conflict=%d fail=%d · %s",
		ev.Done, ev.Total,
		ev.Imported, ev.Duplicates, ev.Conflicts, ev.Failures,
		name,
	)
}

func spaces(n int) string {
	if n <= 0 {
		return ""
	}
	b := make([]byte, n)
	for i := range b {
		b[i] = ' '
	}
	return string(b)
}

// isTerminal returns true if w is *os.File pointing at a terminal.
// Anything else (bytes.Buffer in tests, pipes, redirects) returns false
// so callers fall back to the line-per-batch path.
func isTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	return term.IsTerminal(int(f.Fd()))
}
