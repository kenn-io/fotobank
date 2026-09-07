package cli

import (
	"context"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"go.kenn.io/fotobank/internal/client"
	"go.kenn.io/fotobank/internal/httpapi"
	"go.kenn.io/fotobank/internal/owners"
	"go.kenn.io/fotobank/internal/version"
)

// newImportCmd wires the `fotobank import` subcommand. It takes a single
// positional source directory and submits it to the daemon's import service.
func newImportCmd() *cobra.Command {
	var (
		cfgPath string
		workers int
		wait    time.Duration
		asJSON  bool
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
				asJSON:  asJSON,
				stdout:  cmd.OutOrStdout(),
				stderr:  cmd.ErrOrStderr(),
			})
		},
	}
	cmd.Flags().StringVar(&cfgPath, "config", "", "path to config file (defaults to DefaultConfigPath)")
	cmd.Flags().IntVar(&workers, "workers", 0, "import worker count (0 uses [imports].concurrent_workers)")
	cmd.Flags().DurationVar(&wait, "wait", 0, "max wait for the import lock before failing (0 = fail fast)")
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit the final import result as JSON; progress goes to stderr")
	return cmd
}

type importOpts struct {
	asJSON  bool
	cfgPath string
	source  string
	workers int
	wait    time.Duration
	stdout  io.Writer
	stderr  io.Writer
}

// runImport validates local arguments, ensures the daemon, and renders the
// daemon's progress and final result. It never opens application storage.
func runImport(ctx context.Context, opts importOpts) error {
	source, err := localOperatorPath(opts.source)
	if opts.source == "" {
		err = errors.New("source directory is required")
	}
	if err == nil && (opts.workers < 0 || opts.wait < 0) {
		err = errors.New("workers and wait must be non-negative")
	}
	result := httpapi.ImportResult{Failures: []string{}}
	progressOutput := opts.stdout
	if opts.asJSON {
		progressOutput = opts.stderr
	}
	if err == nil {
		var databasePath string
		var owner owners.Principal
		databasePath, owner, err = localOperatorConfig(ctx, opts.cfgPath)
		if err == nil {
			fmt.Fprintf(progressOutput, "source:    %s\n", source)
			progress := newImportProgress(progressOutput)
			result, err = client.Import(ctx, databasePath, version.Short, httpapi.ImportRequest{
				Hub: owner.Hub, UserID: owner.UserID, Source: source, Workers: opts.workers, Wait: opts.wait.String(),
			}, progress.handle)
			progress.finish()
		}
	}
	if err != nil {
		result.Error = err.Error()
	}
	if opts.asJSON {
		return errors.Join(err, json.MarshalWrite(opts.stdout, result))
	}
	fmt.Fprintf(opts.stdout, "imported=%d\tduplicates=%d\tconflicts=%d\tfailures=%d\n",
		result.Imported, result.Duplicates, result.Conflicts, len(result.Failures))
	for _, failure := range result.Failures {
		fmt.Fprintln(opts.stderr, failure)
	}
	return err
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

func (p *importProgress) handle(ev httpapi.ImportProgress) {
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

func formatProgressLine(ev httpapi.ImportProgress) string {
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
