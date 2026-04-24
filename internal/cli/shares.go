package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/wesm/fotobank/internal/album"
	"github.com/wesm/fotobank/internal/config"
	"github.com/wesm/fotobank/internal/db"
	"github.com/wesm/fotobank/internal/media"
	"github.com/wesm/fotobank/internal/owners"
	"github.com/wesm/fotobank/internal/service"
	"github.com/wesm/fotobank/internal/share"
)

type shareCtx struct {
	svc    *service.ShareService
	caller owners.Principal
	close  func()
}

func loadShareCtx(cfgPath string) (*shareCtx, error) {
	path := cfgPath
	if path == "" {
		path = config.DefaultConfigPath()
	}
	cfg, err := config.Load(path)
	if err != nil {
		return nil, err
	}
	if cfg.Identity.Mode != "stub" {
		return nil, fmt.Errorf("fotobank shares requires identity.mode = stub (got %q)", cfg.Identity.Mode)
	}
	dbPath := os.Getenv("FOTOBANK_DB_PATH")
	if dbPath == "" {
		dbPath = filepath.Join(cfg.Flash.Root, "fotobank.sqlite")
	}
	d, err := db.Open(dbPath)
	if err != nil {
		return nil, err
	}
	return &shareCtx{
		svc: service.NewShareService(
			share.NewRepo(d.WriteDB(), d.ReadDB()),
			album.NewRepo(d.WriteDB(), d.ReadDB()),
			media.NewRepo(d.WriteDB(), d.ReadDB()),
		),
		caller: owners.Principal{Hub: cfg.Identity.Stub.Hub, UserID: cfg.Identity.Stub.UserID},
		close:  func() { _ = d.Close() },
	}, nil
}

func newSharesCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "shares",
		Short: "Manage shares (scopes) for albums or media sets",
		Args:  usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			_ = cmd.Usage()
			return newUsageError("a subcommand is required")
		},
	}
	cmd.AddCommand(newSharesCreateCmd())
	cmd.AddCommand(newSharesListCmd())
	cmd.AddCommand(newSharesShowCmd())
	cmd.AddCommand(newSharesRevokeCmd())
	cmd.AddCommand(newSharesRetryCmd())
	return cmd
}

// --- create ---

func newSharesCreateCmd() *cobra.Command {
	var (
		cfgPath       string
		albumID       string
		mediaCSV      string
		granteeRaw    string
		label         string
		allowDownload bool
		expires       string
	)
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a scope over an album (--album) or media set (--media)",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSharesCreate(cmd.Context(), sharesCreateOpts{
				cfgPath:       cfgPath,
				albumID:       albumID,
				mediaCSV:      mediaCSV,
				granteeRaw:    granteeRaw,
				label:         label,
				allowDownload: allowDownload,
				expires:       expires,
				w:             cmd.OutOrStdout(),
			})
		},
	}
	cmd.Flags().StringVar(&cfgPath, "config", "", "config file path")
	cmd.Flags().StringVar(&albumID, "album", "", "album UUID (album_live target)")
	cmd.Flags().StringVar(&mediaCSV, "media", "", "comma-separated media UUIDs (media_set target)")
	cmd.Flags().StringVar(&granteeRaw, "grantee", "", "grantee principal in hub:user form (required)")
	cmd.Flags().StringVar(&label, "label", "", "optional label (<=200 chars)")
	cmd.Flags().BoolVar(&allowDownload, "allow-download", false, "grant download capability")
	cmd.Flags().StringVar(&expires, "expires", "", "optional RFC3339 expiry timestamp")
	_ = cmd.MarkFlagRequired("grantee")
	return cmd
}

type sharesCreateOpts struct {
	cfgPath, albumID, mediaCSV, granteeRaw, label, expires string
	allowDownload                                          bool
	w                                                      io.Writer
}

func runSharesCreate(ctx context.Context, o sharesCreateOpts) error {
	if o.albumID == "" && o.mediaCSV == "" {
		return newUsageError("exactly one of --album or --media is required")
	}
	if o.albumID != "" && o.mediaCSV != "" {
		return newUsageError("--album and --media are mutually exclusive")
	}
	grantee, err := parseHubUser(o.granteeRaw)
	if err != nil {
		return newUsageError("%s", err.Error())
	}
	var expiresAt *time.Time
	if o.expires != "" {
		t, terr := time.Parse(time.RFC3339, o.expires)
		if terr != nil {
			return newUsageError("invalid --expires: %s", terr.Error())
		}
		expiresAt = &t
	}
	sctx, err := loadShareCtx(o.cfgPath)
	if err != nil {
		return err
	}
	defer sctx.close()

	req := service.CreateShareRequest{
		Label:         o.label,
		Grantee:       grantee,
		AllowDownload: o.allowDownload,
		ExpiresAt:     expiresAt,
	}
	if o.albumID != "" {
		req.TargetType = share.TargetAlbumLive
		req.AlbumID = o.albumID
	} else {
		req.TargetType = share.TargetMediaSet
		req.MediaIDs = splitCSV(o.mediaCSV)
	}
	s, err := sctx.svc.Create(ctx, req, sctx.caller)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(o.w)
	enc.SetIndent("", "  ")
	return enc.Encode(s)
}

// --- list / show / revoke / retry ---

func newSharesListCmd() *cobra.Command {
	var (
		cfgPath, albumID, granteeRaw, statusRaw string
		includeSettled                          bool
		asJSON                                  bool
		limit, offset                           int
	)
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List scopes (default: owner-actionable only)",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSharesList(cmd.Context(), sharesListOpts{
				cfgPath:        cfgPath,
				albumID:        albumID,
				granteeRaw:     granteeRaw,
				statusRaw:      statusRaw,
				includeSettled: includeSettled,
				limit:          limit,
				offset:         offset,
				asJSON:         asJSON,
				w:              cmd.OutOrStdout(),
			})
		},
	}
	cmd.Flags().StringVar(&cfgPath, "config", "", "")
	cmd.Flags().StringVar(&albumID, "album", "", "filter by album_id")
	cmd.Flags().StringVar(&granteeRaw, "grantee", "", "hub:user filter")
	cmd.Flags().StringVar(&statusRaw, "status", "", "comma-separated broker_status filter")
	cmd.Flags().BoolVar(&includeSettled, "include-settled", false, "include revoked_remote rows")
	cmd.Flags().BoolVar(&asJSON, "json", false, "raw JSON instead of table")
	cmd.Flags().IntVar(&limit, "limit", 100, "max rows")
	cmd.Flags().IntVar(&offset, "offset", 0, "pagination offset")
	return cmd
}

type sharesListOpts struct {
	cfgPath, albumID, granteeRaw, statusRaw string
	includeSettled, asJSON                  bool
	limit, offset                           int
	w                                       io.Writer
}

func runSharesList(ctx context.Context, o sharesListOpts) error {
	sctx, err := loadShareCtx(o.cfgPath)
	if err != nil {
		return err
	}
	defer sctx.close()
	statuses, err := share.ParseStatusFilter(o.statusRaw)
	if err != nil {
		return newUsageError("%s", err.Error())
	}
	filter := share.ScopeFilter{
		AlbumID:        o.albumID,
		Status:         statuses,
		IncludeSettled: o.includeSettled,
		Limit:          o.limit,
		Offset:         o.offset,
	}
	if o.granteeRaw != "" {
		g, gerr := parseHubUser(o.granteeRaw)
		if gerr != nil {
			return newUsageError("%s", gerr.Error())
		}
		filter.Grantee = g
	}
	rows, err := sctx.svc.List(ctx, filter, sctx.caller)
	if err != nil {
		return err
	}
	if o.asJSON {
		enc := json.NewEncoder(o.w)
		enc.SetIndent("", "  ")
		return enc.Encode(rows)
	}
	tw := tabwriter.NewWriter(o.w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "UUID\tSTATUS\tTARGET\tGRANTEE\tCREATED\tLAST ERROR")
	for _, s := range rows {
		target := string(s.TargetType)
		if s.TargetAlbumID != nil {
			target = "album " + shortUUID(*s.TargetAlbumID)
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n",
			s.UUID, s.BrokerStatus, target, s.Grantee.String(),
			s.CreatedAt.Format(time.RFC3339), s.BrokerLastError)
	}
	return tw.Flush()
}

func newSharesShowCmd() *cobra.Command {
	var cfgPath string
	cmd := &cobra.Command{
		Use:   "show <uuid>",
		Short: "Show a single scope (including media set membership)",
		Args:  usageArgs(cobra.ExactArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			sctx, err := loadShareCtx(cfgPath)
			if err != nil {
				return err
			}
			defer sctx.close()
			det, err := sctx.svc.Get(cmd.Context(), args[0], sctx.caller)
			if err != nil {
				return err
			}
			enc := json.NewEncoder(cmd.OutOrStdout())
			enc.SetIndent("", "  ")
			return enc.Encode(det)
		},
	}
	cmd.Flags().StringVar(&cfgPath, "config", "", "")
	return cmd
}

func newSharesRevokeCmd() *cobra.Command {
	var cfgPath string
	cmd := &cobra.Command{
		Use:   "revoke <uuid>",
		Short: "Revoke a scope",
		Args:  usageArgs(cobra.ExactArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			sctx, err := loadShareCtx(cfgPath)
			if err != nil {
				return err
			}
			defer sctx.close()
			s, err := sctx.svc.Revoke(cmd.Context(), args[0], sctx.caller)
			if errors.Is(err, share.ErrScopeAlreadyRevoked) {
				fmt.Fprintln(cmd.OutOrStdout(), "already revoked")
				return nil
			}
			if err != nil {
				return err
			}
			enc := json.NewEncoder(cmd.OutOrStdout())
			enc.SetIndent("", "  ")
			return enc.Encode(s)
		},
	}
	cmd.Flags().StringVar(&cfgPath, "config", "", "")
	return cmd
}

func newSharesRetryCmd() *cobra.Command {
	var cfgPath string
	cmd := &cobra.Command{
		Use:   "retry <uuid>",
		Short: "Retry a failed scope",
		Args:  usageArgs(cobra.ExactArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			sctx, err := loadShareCtx(cfgPath)
			if err != nil {
				return err
			}
			defer sctx.close()
			s, err := sctx.svc.Retry(cmd.Context(), args[0], sctx.caller)
			if errors.Is(err, share.ErrRetryNotApplicable) {
				fmt.Fprintln(cmd.OutOrStdout(), "retry not applicable")
				return nil
			}
			if err != nil {
				return err
			}
			enc := json.NewEncoder(cmd.OutOrStdout())
			enc.SetIndent("", "  ")
			return enc.Encode(s)
		},
	}
	cmd.Flags().StringVar(&cfgPath, "config", "", "")
	return cmd
}

// --- helpers ---

func parseHubUser(raw string) (owners.Principal, error) {
	i := strings.IndexByte(raw, ':')
	if i <= 0 || i == len(raw)-1 {
		return owners.Principal{}, fmt.Errorf("invalid hub:user %q", raw)
	}
	return owners.Principal{Hub: raw[:i], UserID: raw[i+1:]}, nil
}

func splitCSV(raw string) []string {
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func shortUUID(s string) string {
	if len(s) <= 8 {
		return s
	}
	return s[:4] + ".." + s[len(s)-4:]
}
