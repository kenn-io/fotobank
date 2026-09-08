package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"uuid"

	"github.com/spf13/cobra"

	"go.kenn.io/fotobank/internal/album"
	"go.kenn.io/fotobank/internal/client"
	"go.kenn.io/fotobank/internal/config"
	"go.kenn.io/fotobank/internal/httpapi"
)

// ensureAlbumDaemon keeps album commands scoped to the configured stub owner.
// The daemon supplies that identity; the client does not send an owner override.
func ensureAlbumDaemon(ctx context.Context, cfgPath string) (client.Lifecycle, error) {
	lifecycle, err := daemonLifecycle(cfgPath, "")
	if err != nil {
		return lifecycle, err
	}
	cfg, err := config.LoadUnchecked(lifecycle.ConfigPath)
	if err != nil {
		return lifecycle, err
	}
	if cfg.Identity.Mode != "stub" {
		return lifecycle, fmt.Errorf(
			"fotobank albums requires identity.mode = stub (got %q)",
			cfg.Identity.Mode)
	}
	_, err = lifecycle.Ensure(ctx)
	return lifecycle, err
}

// newAlbumsCmd wires the `fotobank albums` command group. Running it
// without a subcommand prints usage and returns a usage error.
func newAlbumsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:               "albums",
		Short:             "Manage albums (create, rename, delete, add/remove media)",
		Args:              usageArgs(cobra.NoArgs),
		PersistentPreRunE: validateAlbumCommand,
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
	ac, err := ensureAlbumDaemon(ctx, cfgPath)
	if err != nil {
		return err
	}
	it, err := client.CreateAlbum(ctx, ac.DBPath, ac.Version, httpapi.AlbumNameRequest{Name: name})
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
			ac, err := ensureAlbumDaemon(cmd.Context(), cfgPath)
			if err != nil {
				return err
			}
			it, err := client.RenameAlbum(cmd.Context(), ac.DBPath, ac.Version, args[0], httpapi.AlbumNameRequest{Name: args[1]})
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
			ac, err := ensureAlbumDaemon(cmd.Context(), cfgPath)
			if err != nil {
				return err
			}
			if err := client.DeleteAlbum(cmd.Context(), ac.DBPath, ac.Version, args[0]); err != nil {
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
			ac, err := ensureAlbumDaemon(cmd.Context(), cfgPath)
			if err != nil {
				return err
			}
			result, err := client.ListAlbums(cmd.Context(), ac.DBPath, ac.Version, httpapi.ListAlbumsInput{Limit: limit, Offset: offset})
			if err != nil {
				return err
			}
			if asJSON {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(result)
			}
			tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "UUID\tName\tItems\tUpdated")
			for _, it := range result.Items {
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
				httpapi.ListAlbumMediaInput{AlbumID: args[0], Limit: limit, Offset: offset, SortBy: sortBy, SortAsc: sortAsc},
				cmd.OutOrStdout())
		},
	}
	cmd.Flags().StringVar(&cfgPath, "config", "", "path to config file (defaults to DefaultConfigPath)")
	cmd.Flags().IntVar(&limit, "limit", 100, "max rows to return")
	cmd.Flags().IntVar(&offset, "offset", 0, "pagination offset")
	cmd.Flags().StringVar(&sortBy, "sort-by", "added", "added, imported, or taken")
	cmd.Flags().BoolVar(&sortAsc, "sort-asc", false, "invert the default DESC sort")
	return cmd
}

func runAlbumsShow(
	ctx context.Context,
	cfgPath, id string,
	filter httpapi.ListAlbumMediaInput,
	stdout io.Writer,
) error {
	ac, err := ensureAlbumDaemon(ctx, cfgPath)
	if err != nil {
		return err
	}
	detail, err := client.GetAlbum(ctx, ac.DBPath, ac.Version, id)
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
	result, err := client.ListAlbumMedia(ctx, ac.DBPath, ac.Version, filter)
	if err != nil {
		return err
	}
	fmt.Fprintln(stdout, "members:")
	for _, m := range result.Items {
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
			ac, err := ensureAlbumDaemon(cmd.Context(), cfgPath)
			if err != nil {
				return err
			}
			result, err := client.AddAlbumMedia(cmd.Context(), ac.DBPath, ac.Version, args[0], httpapi.AddAlbumMediaRequest{MediaIDs: args[1:]})
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "added %d, already present %d\n", result.Added, result.AlreadyPresent)
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
			ac, err := ensureAlbumDaemon(cmd.Context(), cfgPath)
			if err != nil {
				return err
			}
			if err := client.RemoveAlbumMedia(cmd.Context(), ac.DBPath, ac.Version, args[0], args[1]); err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "removed", args[1])
			return nil
		},
	}
	cmd.Flags().StringVar(&cfgPath, "config", "", "path to config file (defaults to DefaultConfigPath)")
	return cmd
}

// Reject malformed arguments before automatic startup can create state.
// The service remains authoritative for names, membership and ownership.
func validateAlbumCommand(cmd *cobra.Command, args []string) error {
	var ids []string
	var name string
	switch cmd.Name() {
	case "create":
		name = args[0]
	case "rename":
		ids, name = args[:1], args[1]
	case "delete", "show":
		ids = args[:1]
	case "add", "remove":
		ids = args
	}
	if cmd.Name() == "create" || cmd.Name() == "rename" {
		if trimmed := strings.TrimSpace(name); trimmed == "" || len(trimmed) > album.NameMaxLen {
			return newUsageError("album name must be 1..200 characters after trimming")
		}
	}
	for _, id := range ids {
		if _, err := uuid.Parse(id); err != nil {
			return newUsageError("invalid album or media ID %q", id)
		}
	}
	if cmd.Name() == "add" {
		unique := make(map[string]struct{}, len(args)-1)
		for _, id := range args[1:] {
			unique[id] = struct{}{}
		}
		if len(unique) > album.BatchMaxLen {
			return newUsageError("add accepts at most 500 distinct media IDs")
		}
	}
	if cmd.Name() == "show" {
		sortBy, _ := cmd.Flags().GetString("sort-by")
		if sortBy != "added" && sortBy != "imported" && sortBy != "taken" {
			return newUsageError("--sort-by must be added, imported, or taken")
		}
	}
	return nil
}
