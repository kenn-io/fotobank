package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"go.kenn.io/fotobank/internal/album"
	"go.kenn.io/fotobank/internal/config"
	"go.kenn.io/fotobank/internal/media"
	"go.kenn.io/fotobank/internal/owners"
	"go.kenn.io/fotobank/internal/service"
	"go.kenn.io/fotobank/internal/share"
)

// albumCtx bundles the dependencies every `fotobank albums` subcommand
// needs: an auth-scoped service, the stub caller principal, and a
// teardown callback that closes the underlying DB.
type albumCtx struct {
	svc    *service.AlbumService
	caller owners.Principal
	close  func()
}

// loadAlbumCtx loads the CLI's configuration, opens the DB (respecting
// FOTOBANK_DB_PATH), and constructs an AlbumService plus the caller
// Principal. Enforces stub-mode identity like `thumbs regenerate`
// because we need a single well-defined caller for operator tooling.
func loadAlbumCtx(cfgPath string) (*albumCtx, error) {
	path := cfgPath
	if path == "" {
		path = config.DefaultConfigPath()
	}
	cfg, err := config.Load(path)
	if err != nil {
		return nil, err
	}
	if cfg.Identity.Mode != "stub" {
		return nil, fmt.Errorf(
			"fotobank albums requires identity.mode = stub (got %q)",
			cfg.Identity.Mode)
	}
	d, err := openDatabase(cfg)
	if err != nil {
		return nil, err
	}
	return &albumCtx{
		svc: service.NewAlbumService(
			album.NewRepo(d.WriteDB(), d.ReadDB()),
			media.NewRepo(d.WriteDB(), d.ReadDB()),
			share.NewRepo(d.WriteDB(), d.ReadDB()),
			d.DB,
		),
		caller: owners.Principal{Hub: cfg.Identity.Stub.Hub, UserID: cfg.Identity.Stub.UserID},
		close:  func() { _ = d.Close() },
	}, nil
}

// newAlbumsCmd wires the `fotobank albums` command group. Running it
// without a subcommand prints usage and returns a usage error.
func newAlbumsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "albums",
		Short: "Manage albums (create, rename, delete, add/remove media)",
		Args:  usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			_ = cmd.Usage()
			return newUsageError("a subcommand is required")
		},
	}
	cmd.AddCommand(newAlbumsCreateCmd())
	cmd.AddCommand(newAlbumsRenameCmd())
	cmd.AddCommand(newAlbumsDeleteCmd())
	cmd.AddCommand(newAlbumsListCmd())
	cmd.AddCommand(newAlbumsShowCmd())
	cmd.AddCommand(newAlbumsAddCmd())
	cmd.AddCommand(newAlbumsRemoveCmd())
	return cmd
}

func newAlbumsCreateCmd() *cobra.Command {
	var cfgPath string
	cmd := &cobra.Command{
		Use:   "create <name>",
		Short: "Create a new album",
		Args:  usageArgs(cobra.ExactArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAlbumsCreate(cmd.Context(), cfgPath, args[0], cmd.OutOrStdout())
		},
	}
	cmd.Flags().StringVar(&cfgPath, "config", "", "path to config file (defaults to DefaultConfigPath)")
	return cmd
}

func runAlbumsCreate(ctx context.Context, cfgPath, name string, stdout io.Writer) error {
	ac, err := loadAlbumCtx(cfgPath)
	if err != nil {
		return err
	}
	defer ac.close()
	it, err := ac.svc.Create(ctx, ac.caller, name)
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "%s\t%s\n", it.ID, it.Name)
	return nil
}

func newAlbumsRenameCmd() *cobra.Command {
	var cfgPath string
	cmd := &cobra.Command{
		Use:   "rename <id> <name>",
		Short: "Rename an album",
		Args:  usageArgs(cobra.ExactArgs(2)),
		RunE: func(cmd *cobra.Command, args []string) error {
			ac, err := loadAlbumCtx(cfgPath)
			if err != nil {
				return err
			}
			defer ac.close()
			it, err := ac.svc.Rename(cmd.Context(), args[0], args[1], ac.caller)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\n", it.ID, it.Name)
			return nil
		},
	}
	cmd.Flags().StringVar(&cfgPath, "config", "", "path to config file (defaults to DefaultConfigPath)")
	return cmd
}

func newAlbumsDeleteCmd() *cobra.Command {
	var cfgPath string
	cmd := &cobra.Command{
		Use:   "delete <id>",
		Short: "Delete an album (cascades album_media)",
		Args:  usageArgs(cobra.ExactArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			ac, err := loadAlbumCtx(cfgPath)
			if err != nil {
				return err
			}
			defer ac.close()
			if err := ac.svc.Delete(cmd.Context(), args[0], ac.caller); err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "deleted", args[0])
			return nil
		},
	}
	cmd.Flags().StringVar(&cfgPath, "config", "", "path to config file (defaults to DefaultConfigPath)")
	return cmd
}

func newAlbumsListCmd() *cobra.Command {
	var (
		cfgPath string
		limit   int
		offset  int
		asJSON  bool
	)
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List albums belonging to the caller",
		Args:  usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			ac, err := loadAlbumCtx(cfgPath)
			if err != nil {
				return err
			}
			defer ac.close()
			items, err := ac.svc.List(cmd.Context(), ac.caller, limit, offset)
			if err != nil {
				return err
			}
			if asJSON {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(items)
			}
			tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "UUID\tName\tItems\tUpdated")
			for _, it := range items {
				fmt.Fprintf(tw, "%s\t%s\t%d\t%s\n",
					it.ID, it.Name, it.ItemCount,
					it.UpdatedAt.Format("2006-01-02 15:04:05"))
			}
			return tw.Flush()
		},
	}
	cmd.Flags().StringVar(&cfgPath, "config", "", "path to config file (defaults to DefaultConfigPath)")
	cmd.Flags().IntVar(&limit, "limit", 100, "max rows to return")
	cmd.Flags().IntVar(&offset, "offset", 0, "pagination offset")
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit rows as JSON")
	return cmd
}

func newAlbumsShowCmd() *cobra.Command {
	var (
		cfgPath string
		limit   int
		offset  int
		sortBy  string
		sortAsc bool
	)
	cmd := &cobra.Command{
		Use:   "show <id>",
		Short: "Show album detail + a page of member media IDs",
		Args:  usageArgs(cobra.ExactArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAlbumsShow(cmd.Context(), cfgPath, args[0],
				album.AlbumMediaFilter{Limit: limit, Offset: offset, SortBy: sortBy, SortAsc: sortAsc},
				cmd.OutOrStdout())
		},
	}
	cmd.Flags().StringVar(&cfgPath, "config", "", "path to config file (defaults to DefaultConfigPath)")
	cmd.Flags().IntVar(&limit, "limit", 100, "max rows to return")
	cmd.Flags().IntVar(&offset, "offset", 0, "pagination offset")
	cmd.Flags().StringVar(&sortBy, "sort-by", "added", "added or imported")
	cmd.Flags().BoolVar(&sortAsc, "sort-asc", false, "invert the default DESC sort")
	return cmd
}

func runAlbumsShow(
	ctx context.Context,
	cfgPath, id string,
	filter album.AlbumMediaFilter,
	stdout io.Writer,
) error {
	ac, err := loadAlbumCtx(cfgPath)
	if err != nil {
		return err
	}
	defer ac.close()
	detail, err := ac.svc.GetDetail(ctx, id, ac.caller)
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "id:         %s\n", detail.ID)
	fmt.Fprintf(stdout, "name:       %s\n", detail.Name)
	fmt.Fprintf(stdout, "items:      %d\n", detail.ItemCount)
	fmt.Fprintf(stdout, "updated_at: %s\n", detail.UpdatedAt.Format("2006-01-02 15:04:05"))
	if detail.Cover != nil {
		fmt.Fprintf(stdout, "cover:      media=%s version=%d\n",
			detail.Cover.MediaID, detail.Cover.ThumbVersion)
	}
	rows, err := ac.svc.ListMedia(ctx, id, filter, ac.caller)
	if err != nil {
		return err
	}
	fmt.Fprintln(stdout, "members:")
	for _, m := range rows {
		fmt.Fprintln(stdout, "  ", m.ID)
	}
	return nil
}

func newAlbumsAddCmd() *cobra.Command {
	var cfgPath string
	cmd := &cobra.Command{
		Use:   "add <album_id> <media_id> [<media_id>...]",
		Short: "Add one or more media rows to an album (idempotent)",
		Args:  usageArgs(cobra.MinimumNArgs(2)),
		RunE: func(cmd *cobra.Command, args []string) error {
			ac, err := loadAlbumCtx(cfgPath)
			if err != nil {
				return err
			}
			defer ac.close()
			added, already, err := ac.svc.AddMedia(cmd.Context(), args[0], args[1:], ac.caller)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "added %d, already present %d\n", added, already)
			return nil
		},
	}
	cmd.Flags().StringVar(&cfgPath, "config", "", "path to config file (defaults to DefaultConfigPath)")
	return cmd
}

func newAlbumsRemoveCmd() *cobra.Command {
	var cfgPath string
	cmd := &cobra.Command{
		Use:   "remove <album_id> <media_id>",
		Short: "Remove a media row from an album",
		Args:  usageArgs(cobra.ExactArgs(2)),
		RunE: func(cmd *cobra.Command, args []string) error {
			ac, err := loadAlbumCtx(cfgPath)
			if err != nil {
				return err
			}
			defer ac.close()
			if err := ac.svc.RemoveMedia(cmd.Context(), args[0], args[1], ac.caller); err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "removed", args[1])
			return nil
		},
	}
	cmd.Flags().StringVar(&cfgPath, "config", "", "path to config file (defaults to DefaultConfigPath)")
	return cmd
}
