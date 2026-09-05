package cli

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gofrs/flock"
	"github.com/spf13/cobra"

	"go.kenn.io/fotobank/internal/backup"
	"go.kenn.io/fotobank/internal/config"
	"go.kenn.io/fotobank/internal/errs"
)

func newBackupCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "backup",
		Short: "Create and verify recovery archives, or manage metadata snapshots",
	}
	cmd.AddCommand(newBackupSnapshotCmd())
	cmd.AddCommand(newBackupListCmd())
	cmd.AddCommand(newBackupRestoreCmd())
	cmd.AddCommand(newBackupInitCmd(), newBackupCreateCmd(), newBackupVerifyCmd())
	return cmd
}

func newBackupSnapshotCmd() *cobra.Command {
	var out string
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "snapshot",
		Short: "Take a one-shot snapshot of the metadata DB",
		Args:  usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := loadConfigFromCmd(cmd)
			if err != nil {
				return err
			}
			dbPath, err := resolveDBPath(cfg)
			if err != nil {
				return err
			}
			lifetime, err := acquireDatabaseLifetime(dbPath)
			if err != nil {
				return err
			}
			defer lifetime.Close()
			dst := out
			var (
				root        *os.Root
				relativeDst string
			)
			if dst == "" && cfg.Backup.Dir == "" {
				root, err = openBackupNASRoot(cfg)
				if err != nil {
					return fmt.Errorf("open backup destination: %w", err)
				}
				defer root.Close()
				relativeDst = filepath.Join(
					".fotobank",
					"snapshots",
					time.Now().UTC().Format(backup.StampLayout)+backup.SnapshotExt,
				)
				dst = filepath.Join(cfg.NAS.Root, relativeDst)
			} else if dst == "" {
				if err := prepareBackupDir(cfg, cfg.Backup.Dir); err != nil {
					return fmt.Errorf("mkdir backup dir: %w", err)
				}
				dst = filepath.Join(
					cfg.Backup.Dir,
					time.Now().UTC().Format(backup.StampLayout)+backup.SnapshotExt,
				)
			}
			start := time.Now()
			if root == nil {
				err = backup.SnapshotPath(cmd.Context(), lifetime.path, dst)
			} else {
				err = backup.SnapshotPathToRoot(cmd.Context(), lifetime.path, root, relativeDst)
			}
			if err != nil {
				return err
			}
			elapsed := time.Since(start)
			var info os.FileInfo
			if root == nil {
				info, err = os.Stat(dst)
			} else {
				info, err = root.Stat(relativeDst)
			}
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
	cmd.Flags().StringVar(&out, "out", "", "destination path (default: backup.dir/{timestamp}.sqlite)")
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit JSON to stdout")
	cmd.Flags().String("config", "", "path to config file")
	return cmd
}

func newBackupListCmd() *cobra.Command {
	var asJSON bool
	var repositoryPath string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List archive recovery points with --repo, or configured metadata snapshots",
		Args:  usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			if repositoryPath != "" {
				return listArchives(cmd, repositoryPath, asJSON)
			}
			cfg, err := loadConfigFromCmd(cmd)
			if err != nil {
				return err
			}
			var snaps []backup.SnapshotInfo
			if cfg.Backup.Dir == "" {
				root, rootErr := openBackupNASRoot(cfg)
				if rootErr != nil {
					return fmt.Errorf("open backup source: %w", rootErr)
				}
				defer root.Close()
				snaps, err = backup.ListRoot(root, filepath.Join(".fotobank", "snapshots"))
			} else {
				snaps, err = backup.List(cfg.Backup.Dir)
			}
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
	cmd.Flags().StringVar(&repositoryPath, "repo", "", "archive repository to list (default: metadata snapshot directory)")
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit JSON to stdout")
	cmd.Flags().String("config", "", "path to config file")
	return cmd
}

func newBackupRestoreCmd() *cobra.Command {
	var yes, asJSON, dryRun bool
	cmd := &cobra.Command{
		Use:   "restore <snapshot-path>",
		Short: "Restore the metadata DB from a snapshot file",
		Args:  usageArgs(cobra.ExactArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfigFromCmd(cmd)
			if err != nil {
				return err
			}
			snap := args[0]
			dbPath, err := resolveDBPath(cfg)
			if err != nil {
				return err
			}
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

// promptRestoreConfirmation reads a single line from stdin and requires
// it to be exactly "yes" (CR/LF stripped) before proceeding with a
// destructive restore. Any other input cancels the operation. Reading
// up to a newline avoids the line-buffered TTY hang that a fixed-size
// io.ReadFull would suffer when the user types fewer bytes than the
// buffer.
func promptRestoreConfirmation(cmd *cobra.Command, snap, dbPath string) error {
	fmt.Fprintf(cmd.ErrOrStderr(),
		"Restore from %s into %s?\n"+
			"This will move the existing DB to {dbPath}.pre-restore.{timestamp}.\n"+
			"Type 'yes' to proceed: ",
		snap, dbPath)
	r := bufio.NewReader(cmd.InOrStdin())
	line, err := r.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("read confirmation: %w", err)
	}
	if strings.TrimRight(line, "\r\n") != "yes" {
		return errors.New("restore cancelled")
	}
	return nil
}

// restoreDryRun validates the snapshot's integrity and probes the
// lifetime lock without moving any files. Surfacing a corrupt snapshot
// or another database user pre-emptively means an operator finds out
// before any move-aside runs.
func restoreDryRun(cmd *cobra.Command, snap, dbPath, lockPath string, asJSON bool) error {
	if err := backup.ValidateSnapshot(cmd.Context(), snap); err != nil {
		return fmt.Errorf("validate snapshot: %w", err)
	}
	// A missing lock file cannot be held. Do not create it (or its parent)
	// merely to probe it: --dry-run promises not to change the filesystem.
	if _, err := os.Stat(lockPath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return writeRestoreDryRunResult(cmd, snap, dbPath, asJSON)
		}
		return fmt.Errorf("inspect database lifetime lock: %w", err)
	}
	l := flock.New(lockPath)
	ok, err := l.TryLock()
	if err != nil {
		return fmt.Errorf("flock: %w", err)
	}
	if !ok {
		return fmt.Errorf("%w: %s", backup.ErrServerHoldsLock, dbPath)
	}
	_ = l.Unlock()
	return writeRestoreDryRunResult(cmd, snap, dbPath, asJSON)
}

func writeRestoreDryRunResult(cmd *cobra.Command, snap, dbPath string, asJSON bool) error {
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

func prepareBackupDir(cfg *config.Config, dir string) error {
	if cfg.Backup.Dir != "" {
		return os.MkdirAll(dir, 0o700)
	}
	root, err := openBackupNASRoot(cfg)
	if err != nil {
		return err
	}
	defer root.Close()
	return root.MkdirAll(filepath.Join(".fotobank", "snapshots"), 0o700)
}

func openBackupNASRoot(cfg *config.Config) (*os.Root, error) {
	root, err := os.OpenRoot(cfg.NAS.Root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("%w: NAS root is unavailable", errs.ErrContentUnavailable)
		}
		return nil, err
	}
	return root, nil
}

func backupRequiredRoot(cfg *config.Config) string {
	if cfg.Backup.Dir == "" {
		return cfg.NAS.Root
	}
	return ""
}
