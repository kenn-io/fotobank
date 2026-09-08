package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"
	"uuid"

	"github.com/spf13/cobra"

	"go.kenn.io/fotobank/internal/client"
	"go.kenn.io/fotobank/internal/config"
	"go.kenn.io/fotobank/internal/httpapi"
	"go.kenn.io/fotobank/internal/owners"
	"go.kenn.io/fotobank/internal/share"
)

func ensureShareDaemon(ctx context.Context, cfgPath string) (client.Lifecycle, error) {
	lifecycle, err := daemonLifecycle(cfgPath, "")
	if err != nil {
		return lifecycle, err
	}
	cfg, err := config.LoadUnchecked(lifecycle.ConfigPath)
	if err != nil {
		return lifecycle, err
	}
	if cfg.Identity.Mode != "stub" {
		return lifecycle, fmt.Errorf("fotobank shares requires identity.mode = stub (got %q)", cfg.Identity.Mode)
	}
	_, err = lifecycle.Ensure(ctx)
	return lifecycle, err
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
		Args:  usageArgs(cobra.NoArgs),
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
	req := httpapi.CreateShareRequest{
		Label:         o.label,
		Grantee:       httpapi.PrincipalDTO{Hub: grantee.Hub, UserID: grantee.UserID},
		AllowDownload: o.allowDownload,
		ExpiresAt:     expiresAt,
	}
	if o.albumID != "" {
		req.TargetType = string(share.TargetAlbumLive)
		req.AlbumID = o.albumID
	} else {
		req.TargetType = string(share.TargetMediaSet)
		req.MediaIDs = splitCSV(o.mediaCSV)
	}
	if len(req.Label) > share.LabelMaxLen {
		return newUsageError("label exceeds 200 characters")
	}
	if o.albumID != "" {
		if _, err := uuid.Parse(o.albumID); err != nil {
			return newUsageError("invalid album UUID %q", o.albumID)
		}
	} else {
		unique := make(map[string]struct{}, len(req.MediaIDs))
		for _, id := range req.MediaIDs {
			if _, err := uuid.Parse(id); err != nil {
				return newUsageError("invalid media UUID %q", id)
			}
			unique[id] = struct{}{}
		}
		if len(unique) == 0 || len(unique) > share.MediaSetMaxLen {
			return newUsageError("--media requires 1..1000 distinct media UUIDs")
		}
	}
	sctx, err := ensureShareDaemon(ctx, o.cfgPath)
	if err != nil {
		return err
	}
	s, err := client.CreateShare(ctx, sctx.DBPath, sctx.Version, req)
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
		Args:  usageArgs(cobra.NoArgs),
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
	_, err := share.ParseStatusFilter(o.statusRaw)
	if err != nil {
		return newUsageError("%s", err.Error())
	}
	filter := httpapi.ListSharesInput{
		AlbumID:        o.albumID,
		Status:         o.statusRaw,
		IncludeSettled: o.includeSettled,
		Limit:          o.limit,
		Offset:         o.offset,
	}
	if o.granteeRaw != "" {
		g, gerr := parseHubUser(o.granteeRaw)
		if gerr != nil {
			return newUsageError("%s", gerr.Error())
		}
		filter.GranteeHub, filter.GranteeUserID = g.Hub, g.UserID
	}
	if o.albumID != "" {
		if _, err := uuid.Parse(o.albumID); err != nil {
			return newUsageError("invalid album UUID %q", o.albumID)
		}
	}
	sctx, err := ensureShareDaemon(ctx, o.cfgPath)
	if err != nil {
		return err
	}
	page, err := client.ListShares(ctx, sctx.DBPath, sctx.Version, filter)
	if err != nil {
		return err
	}
	if o.asJSON {
		enc := json.NewEncoder(o.w)
		enc.SetIndent("", "  ")
		return enc.Encode(page)
	}
	tw := tabwriter.NewWriter(o.w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "UUID\tSTATUS\tTARGET\tGRANTEE\tCREATED\tLAST ERROR")
	for _, s := range page.Items {
		target := s.TargetType
		if s.TargetAlbumID != "" {
			target = "album " + shortUUID(s.TargetAlbumID)
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n",
			s.UUID, s.BrokerStatus, target, s.Grantee.Hub+":"+s.Grantee.UserID,
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
			if _, err := uuid.Parse(args[0]); err != nil {
				return newUsageError("invalid share UUID %q", args[0])
			}
			sctx, err := ensureShareDaemon(cmd.Context(), cfgPath)
			if err != nil {
				return err
			}
			det, err := client.GetShare(cmd.Context(), sctx.DBPath, sctx.Version, args[0])
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
			if _, err := uuid.Parse(args[0]); err != nil {
				return newUsageError("invalid share UUID %q", args[0])
			}
			sctx, err := ensureShareDaemon(cmd.Context(), cfgPath)
			if err != nil {
				return err
			}
			s, err := client.RevokeShare(cmd.Context(), sctx.DBPath, sctx.Version, args[0])
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
			if _, err := uuid.Parse(args[0]); err != nil {
				return newUsageError("invalid share UUID %q", args[0])
			}
			sctx, err := ensureShareDaemon(cmd.Context(), cfgPath)
			if err != nil {
				return err
			}
			s, err := client.RetryShare(cmd.Context(), sctx.DBPath, sctx.Version, args[0])
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
	if i > share.PrincipalFieldMaxLen || len(raw)-i-1 > share.PrincipalFieldMaxLen {
		return owners.Principal{}, fmt.Errorf("hub and user must each be at most %d bytes", share.PrincipalFieldMaxLen)
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

// ParseHubUserForTest, SplitCSVForTest, and ShortUUIDForTest are
// test-only exports so the cli_test package can exercise these pure
// helpers without promoting them into the public API. Mirrors the
// TranslateAlbumErrorForTest pattern in internal/httpapi/albums.go.
func ParseHubUserForTest(raw string) (owners.Principal, error) { return parseHubUser(raw) }

// SplitCSVForTest exposes splitCSV for package cli_test.
func SplitCSVForTest(raw string) []string { return splitCSV(raw) }

// ShortUUIDForTest exposes shortUUID for package cli_test.
func ShortUUIDForTest(s string) string { return shortUUID(s) }
