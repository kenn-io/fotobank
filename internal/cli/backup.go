package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/gofrs/flock"
	"github.com/spf13/cobra"

	"github.com/wesm/fotobank/internal/backup"
	"github.com/wesm/fotobank/internal/config"
)

// snapshotStampLayout matches backup.stampLayout: an RFC3339 timestamp
// with millisecond precision and a literal "Z" suffix. Snapshot
// filenames written by the worker and the CLI must agree so List can
// parse both. Duplicated here because backup keeps its layout
// unexported.
const snapshotStampLayout = "2006-01-02T15:04:05.000Z"

func newBackupCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "backup",
		Short: "Snapshot, list, and restore the metadata DB",
	}
	cmd.AddCommand(newBackupSnapshotCmd())
	cmd.AddCommand(newBackupListCmd())
	cmd.AddCommand(newBackupRestoreCmd())
	return cmd
}

func newBackupSnapshotCmd() *cobra.Command {
	var out string
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "snapshot",
		Short: "Take a one-shot snapshot of the metadata DB",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := loadConfigFromCmd(cmd)
			if err != nil {
				return err
			}
			dst := out
			if dst == "" {
				dir := backupDirFor(cfg)
				if err := os.MkdirAll(dir, 0o700); err != nil {
					return fmt.Errorf("mkdir backup dir: %w", err)
				}
				dst = filepath.Join(dir,
					time.Now().UTC().Format(snapshotStampLayout)+".sqlite")
			}
			start := time.Now()
			if err := backup.SnapshotPath(cmd.Context(), resolveDBPath(cfg), dst); err != nil {
				return err
			}
			elapsed := time.Since(start)
			info, err := os.Stat(dst)
			if err != nil {
				return fmt.Errorf("stat snapshot: %w", err)
			}
			if asJSON {
				type result struct {
					Path       string `json:"path"`
					SizeBytes  int64  `json:"size_bytes"`
					DurationMs int64  `json:"duration_ms"`
					Timestamp  string `json:"timestamp"`
				}
				return json.NewEncoder(cmd.OutOrStdout()).Encode(result{
					Path:       dst,
					SizeBytes:  info.Size(),
					DurationMs: elapsed.Milliseconds(),
					Timestamp:  info.ModTime().UTC().Format(time.RFC3339Nano),
				})
			}
			fmt.Fprintf(cmd.OutOrStdout(), "snapshot ok: %s (%d bytes in %s)\n",
				dst, info.Size(), elapsed)
			return nil
		},
	}
	cmd.Flags().StringVar(&out, "out", "", "destination path (default: backup.dir/{ms-timestamp}.sqlite)")
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit JSON to stdout")
	cmd.Flags().String("config", "", "path to config file")
	return cmd
}

func newBackupListCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List all snapshots in the configured backup dir, newest-first",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := loadConfigFromCmd(cmd)
			if err != nil {
				return err
			}
			snaps, err := backup.List(backupDirFor(cfg))
			if err != nil {
				return err
			}
			if asJSON {
				type item struct {
					Timestamp string `json:"timestamp"`
					SizeBytes int64  `json:"size_bytes"`
					Path      string `json:"path"`
				}
				items := make([]item, 0, len(snaps))
				for _, s := range snaps {
					items = append(items, item{
						Timestamp: s.Timestamp.Format(time.RFC3339Nano),
						SizeBytes: s.Size,
						Path:      s.Path,
					})
				}
				return json.NewEncoder(cmd.OutOrStdout()).Encode(items)
			}
			for _, s := range snaps {
				fmt.Fprintf(cmd.OutOrStdout(), "%s  %d  %s\n",
					s.Timestamp.Format(time.RFC3339), s.Size, s.Path)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit JSON to stdout")
	cmd.Flags().String("config", "", "path to config file")
	return cmd
}

func newBackupRestoreCmd() *cobra.Command {
	var yes, asJSON, dryRun bool
	cmd := &cobra.Command{
		Use:   "restore <snapshot-path>",
		Short: "Restore the metadata DB from a snapshot file",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfigFromCmd(cmd)
			if err != nil {
				return err
			}
			snap := args[0]
			dbPath := resolveDBPath(cfg)
			lockPath := lockPathFor(dbPath)

			if dryRun {
				return restoreDryRun(cmd, snap, dbPath, lockPath, asJSON)
			}

			if !yes {
				if err := promptRestoreConfirmation(cmd, snap, dbPath); err != nil {
					return err
				}
			}

			res, err := backup.Restore(cmd.Context(), snap, dbPath, lockPath)
			if err != nil {
				return err
			}
			if asJSON {
				type result struct {
					RestoredFrom string   `json:"restored_from"`
					DBPath       string   `json:"db_path"`
					MovedAside   []string `json:"moved_aside"`
				}
				return json.NewEncoder(cmd.OutOrStdout()).Encode(result{
					RestoredFrom: res.SnapshotPath,
					DBPath:       res.DBPath,
					MovedAside:   res.MovedAside,
				})
			}
			fmt.Fprintf(cmd.OutOrStdout(),
				"restored from %s -> %s\nprevious DB moved aside:\n",
				res.SnapshotPath, res.DBPath)
			for _, p := range res.MovedAside {
				fmt.Fprintf(cmd.OutOrStdout(), "  %s\n", p)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "skip the interactive confirmation prompt")
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit JSON to stdout")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "validate + acquire lock only; no file changes")
	cmd.Flags().String("config", "", "path to config file")
	return cmd
}

// promptRestoreConfirmation reads up to four bytes from stdin and
// requires "yes\n" or "yes\r" before proceeding with a destructive
// restore. Any other input cancels the operation.
func promptRestoreConfirmation(cmd *cobra.Command, snap, dbPath string) error {
	fmt.Fprintf(cmd.ErrOrStderr(),
		"Restore from %s into %s?\n"+
			"This will move the existing DB to {dbPath}.pre-restore.{timestamp}.\n"+
			"Type 'yes' to proceed: ",
		snap, dbPath)
	in := cmd.InOrStdin()
	var resp [4]byte
	n, _ := io.ReadFull(in, resp[:])
	if string(resp[:n]) != "yes\n" && string(resp[:n]) != "yes\r" {
		return errors.New("restore cancelled")
	}
	return nil
}

// restoreDryRun validates the snapshot's integrity and probes the
// lifetime lock without moving any files. Surfacing a corrupt snapshot
// or an in-flight server pre-emptively means an operator finds out
// before any move-aside runs.
func restoreDryRun(cmd *cobra.Command, snap, dbPath, lockPath string, asJSON bool) error {
	if err := backup.ValidateSnapshot(cmd.Context(), snap); err != nil {
		return fmt.Errorf("validate snapshot: %w", err)
	}
	l := flockNew(lockPath)
	ok, err := l.TryLock()
	if err != nil {
		return fmt.Errorf("flock: %w", err)
	}
	if !ok {
		return fmt.Errorf("%w: %s", backup.ErrServerHoldsLock, dbPath)
	}
	_ = l.Unlock()
	if asJSON {
		type result struct {
			DryRun       bool   `json:"dry_run"`
			SnapshotPath string `json:"snapshot_path"`
			DBPath       string `json:"db_path"`
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(result{
			DryRun: true, SnapshotPath: snap, DBPath: dbPath,
		})
	}
	fmt.Fprintf(cmd.OutOrStdout(),
		"dry-run ok: snapshot %s would replace %s\n", snap, dbPath)
	return nil
}

// backupDirFor returns the configured backup dir, defaulting to
// {nas.root}/.fotobank/snapshots.
func backupDirFor(cfg *config.Config) string {
	if cfg.Backup.Dir != "" {
		return cfg.Backup.Dir
	}
	return filepath.Join(cfg.NAS.Root, ".fotobank", "snapshots")
}

// flockHandle is the subset of *flock.Flock the dry-run path uses.
// Exposing it as an interface lets tests stub the lock behaviour
// without standing up a real server.
type flockHandle interface {
	TryLock() (bool, error)
	Unlock() error
}

// flockNew is the package-level constructor injection point so tests
// can stub the lock behaviour. Production calls flock.New directly.
var flockNew = func(path string) flockHandle {
	return flock.New(path)
}
