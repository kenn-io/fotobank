package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"go.kenn.io/fotobank/internal/config"
	"go.kenn.io/fotobank/internal/db"
	"go.kenn.io/fotobank/internal/media"
	"go.kenn.io/fotobank/internal/owners"
	"go.kenn.io/fotobank/internal/reconcile"
	"go.kenn.io/fotobank/internal/service"
)

// newReconcileCmd wires the `fotobank reconcile` subcommand. It walks the
// configured owner's NAS subtree, compares it to the media table, and
// prints the drift report. Deletes are opt-in via --commit-deletes /
// --commit-temps; a default invocation is read-only.
func newReconcileCmd() *cobra.Command {
	var (
		cfgPath       string
		commitDeletes bool
		commitTemps   bool
		jsonOut       bool
	)
	cmd := &cobra.Command{
		Use:   "reconcile",
		Short: "Report and optionally commit drift between NAS bytes and the media table",
		Args:  usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runReconcile(cmd.Context(), reconcileOpts{
				cfgPath:       cfgPath,
				commitDeletes: commitDeletes,
				commitTemps:   commitTemps,
				jsonOut:       jsonOut,
				stdout:        cmd.OutOrStdout(),
			})
		},
	}
	cmd.Flags().StringVar(&cfgPath, "config", "", "path to config file (defaults to DefaultConfigPath)")
	cmd.Flags().BoolVar(&commitDeletes, "commit-deletes", false, "delete phantom DB rows for missing files")
	cmd.Flags().BoolVar(&commitTemps, "commit-temps", false, "delete stale import temp files")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit the report as JSON")
	return cmd
}

type reconcileOpts struct {
	cfgPath       string
	commitDeletes bool
	commitTemps   bool
	jsonOut       bool
	stdout        io.Writer
}

// runReconcile loads the config, opens the DB, resolves the stub owner,
// looks up the owner's storage_key, and drives reconcile.Reconcile.
// Detected drift is informational — the command returns nil regardless of
// how many items the report lists.
func runReconcile(ctx context.Context, opts reconcileOpts) error {
	path := opts.cfgPath
	if path == "" {
		path = config.DefaultConfigPath()
	}
	cfg, err := config.Load(path)
	if err != nil {
		return err
	}
	if cfg.Identity.Mode != "stub" {
		return fmt.Errorf("fotobank reconcile requires identity.mode = stub (got %q)", cfg.Identity.Mode)
	}

	dbPath := os.Getenv("FOTOBANK_DB_PATH")
	if dbPath == "" {
		dbPath = filepath.Join(cfg.Flash.Root, "fotobank.sqlite")
	}
	d, err := db.Open(dbPath)
	if err != nil {
		return err
	}
	defer d.Close()

	owner := owners.Principal{
		Hub:    cfg.Identity.Stub.Hub,
		UserID: cfg.Identity.Stub.UserID,
	}
	ownerSvc := service.NewOwnerService(owners.NewRepo(d.WriteDB(), d.ReadDB()))

	// Reconcile is a diagnostic — even the --commit-* flags only mutate
	// media/temp rows, not owners. Require that the owner already exists
	// (via import or an earlier setup command) so a default run is
	// unambiguously read-only against owners.
	resolved, err := resolveOwnerStorageKey(ctx, ownerSvc, owner)
	if err != nil {
		return err
	}

	repo := media.NewRepo(d.WriteDB(), d.ReadDB())
	rep, err := reconcile.Reconcile(ctx, repo, reconcile.Options{
		Owner:         owner,
		StorageKey:    resolved,
		NASRoot:       cfg.NAS.Root,
		CommitDeletes: opts.commitDeletes,
		CommitTemps:   opts.commitTemps,
	})
	if err != nil {
		return err
	}

	if opts.jsonOut {
		enc := json.NewEncoder(opts.stdout)
		return enc.Encode(rep)
	}
	printReconcileReport(opts.stdout, rep, opts.commitDeletes, opts.commitTemps)
	return nil
}

// resolveOwnerStorageKey looks up p's storage_key from the owners repo.
// Reconcile only needs one owner's key, so we iterate the (small) list
// rather than growing OwnerService's surface area with a per-principal
// Get just for this caller.
func resolveOwnerStorageKey(ctx context.Context, ownerSvc *service.OwnerService, p owners.Principal) (string, error) {
	list, err := ownerSvc.List(ctx)
	if err != nil {
		return "", fmt.Errorf("load owners: %w", err)
	}
	for _, o := range list {
		if o.Principal == p {
			return o.StorageKey, nil
		}
	}
	return "", fmt.Errorf("owner %s not registered", p)
}

// printReconcileReport emits a human-readable report. Detail lists are
// suppressed when empty to keep the common "clean archive" output short.
func printReconcileReport(w io.Writer, rep reconcile.Report, commitDeletes, commitTemps bool) {
	fmt.Fprintln(w, "fotobank reconcile report")
	fmt.Fprintf(w, "  Orphans:         %d\n", len(rep.Orphans))
	fmt.Fprintf(w, "  Missing:         %d\n", len(rep.Missing))
	fmt.Fprintf(w, "  Size mismatches: %d\n", len(rep.SizeMismatch))
	fmt.Fprintf(w, "  Stale temps:     %d\n", len(rep.StaleTemps))
	if commitDeletes {
		fmt.Fprintf(w, "  Deleted rows:    %d\n", rep.DeletedRows)
	}
	if commitTemps {
		fmt.Fprintf(w, "  Deleted temps:   %d\n", rep.DeletedTemps)
	}

	if len(rep.Orphans) > 0 {
		fmt.Fprintln(w, "\nOrphans:")
		for _, o := range rep.Orphans {
			fmt.Fprintf(w, "  %s (%d bytes)\n", o.Path, o.Size)
		}
	}
	if len(rep.Missing) > 0 {
		fmt.Fprintln(w, "\nMissing:")
		for _, m := range rep.Missing {
			fmt.Fprintf(w, "  %s %s\n", m.ID, m.Path)
		}
	}
	if len(rep.SizeMismatch) > 0 {
		fmt.Fprintln(w, "\nSize mismatches:")
		for _, s := range rep.SizeMismatch {
			fmt.Fprintf(w, "  %s db=%d disk=%d\n", s.MediaID, s.DBSize, s.OnDiskSize)
		}
	}
	if len(rep.StaleTemps) > 0 {
		fmt.Fprintln(w, "\nStale temps:")
		for _, p := range rep.StaleTemps {
			fmt.Fprintf(w, "  %s\n", p)
		}
	}
}
