package cli

import (
	json "encoding/json/v2"
	"fmt"
	"io"
	"strconv"
	"text/tabwriter"
	"time"
	"uuid"

	"github.com/spf13/cobra"

	"go.kenn.io/fotobank/internal/client"
	"go.kenn.io/fotobank/internal/httpapi"
	"go.kenn.io/fotobank/internal/version"
)

func newMediaCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use: "media", Short: "Find and inspect photos and videos",
		Args: usageArgs(cobra.NoArgs),
		Long: "Find and inspect photos and videos belonging to the configured owner. Hidden media is not included. Commands use the daemon and start it when needed.",
		Example: `  fotobank media list --type photo --limit 20 --json
  fotobank media list --camera "Example Camera" --has-gps
  fotobank media search "sunset" --type photo --json
  fotobank media show <photo-id> --json`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			_ = cmd.Usage()
			return newUsageError("a subcommand is required")
		},
	}
	cmd.AddCommand(newMediaListCmd(), newMediaSearchCmd(), newMediaShowCmd())
	return cmd
}

func newMediaListCmd() *cobra.Command {
	var input httpapi.ListMediaInput
	var cfgPath string
	var asJSON, hasGPS bool
	cmd := &cobra.Command{
		Use: "list", Short: "List a page of visible photos and videos",
		Args: usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			if input.MediaType != "" && input.MediaType != "photo" && input.MediaType != "video" {
				return newUsageError("--type must be photo or video")
			}
			if input.Limit < 1 || input.Limit > 1000 || input.Offset < 0 {
				return newUsageError("--limit must be between 1 and 1000; --offset must be non-negative")
			}
			if cmd.Flags().Changed("has-gps") {
				input.HasGPS = strconv.FormatBool(hasGPS)
			}
			resolved, _, err := localOperatorConfig(cmd.Context(), cfgPath)
			if err != nil {
				return err
			}
			page, err := client.ListMedia(cmd.Context(), resolved, version.Short, input)
			if err != nil {
				return err
			}
			if asJSON {
				return json.MarshalWrite(cmd.OutOrStdout(), page)
			}
			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "ID\tTYPE\tFILE\tBYTES")
			for _, item := range page.Items {
				fmt.Fprintf(w, "%s\t%s\t%q\t%d\n", item.ID, item.Type, item.OriginalFilename, item.Size)
			}
			if err := w.Flush(); err != nil {
				return err
			}
			if page.NextOffset != nil {
				_, err = fmt.Fprintf(cmd.OutOrStdout(), "More results: repeat with --offset %d and the same filters.\n", *page.NextOffset)
			}
			return err
		},
	}
	flags := cmd.Flags()
	flags.StringVar(&cfgPath, "config", "", "path to config file")
	flags.BoolVar(&asJSON, "json", false, "emit the API page, including next_offset, as JSON")
	flags.StringVar(&input.MediaType, "type", "", "photo or video (default: both)")
	flags.IntVar(&input.Limit, "limit", 100, "page size (1–1000)")
	flags.IntVar(&input.Offset, "offset", 0, "pagination offset")
	flags.BoolVar(&input.SortDesc, "sort-desc", false, "sort capture timestamps newest first (default: oldest first)")
	flags.StringArrayVar(&input.Camera, "camera", nil, "exact make and model; repeat to match any")
	flags.StringArrayVar(&input.Lens, "lens", nil, "exact lens model; repeat to match any")
	flags.StringArrayVar(&input.FacetTag, "tag", nil, "tag key; repeat to match any")
	flags.BoolVar(&hasGPS, "has-gps", false, "filter geotagged media; --has-gps=false selects media without GPS")
	_ = cmd.RegisterFlagCompletionFunc("type", cobra.FixedCompletions([]string{"photo", "video"}, cobra.ShellCompDirectiveNoFileComp))
	return cmd
}

func newMediaShowCmd() *cobra.Command {
	var cfgPath string
	var asJSON bool
	cmd := &cobra.Command{
		Use: "show <id>", Short: "Inspect a photo or video, its metadata and attached files",
		Args: usageArgs(cobra.ExactArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := uuid.Parse(args[0])
			if err != nil {
				return newUsageError("media id must be a UUID")
			}
			resolved, _, err := localOperatorConfig(cmd.Context(), cfgPath)
			if err != nil {
				return err
			}
			item, err := client.GetMedia(cmd.Context(), resolved, version.Short, id)
			if err != nil {
				return err
			}
			if asJSON {
				return json.MarshalWrite(cmd.OutOrStdout(), item)
			}
			return printMediaDetail(cmd.OutOrStdout(), item)
		},
	}
	cmd.Flags().StringVar(&cfgPath, "config", "", "path to config file")
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit full metadata and attached files as JSON")
	return cmd
}

func printMediaDetail(out io.Writer, item httpapi.MediaDTO) error {
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintf(w, "ID\t%s\nType\t%s (%s)\nFile\t%q\nBytes\t%d\nSHA-256\t%s\n", item.ID, item.Type, item.MimeType, item.OriginalFilename, item.Size, item.SHA256)
	if item.Timestamp != nil {
		fmt.Fprintf(w, "Captured\t%s\n", item.Timestamp.Format(time.RFC3339))
	}
	if item.Make != "" || item.Model != "" {
		fmt.Fprintf(w, "Camera\t%q %q\n", item.Make, item.Model)
	}
	if item.Width != nil && item.Height != nil {
		fmt.Fprintf(w, "Dimensions\t%d × %d\n", *item.Width, *item.Height)
	}
	if item.Files != nil && len(*item.Files) > 0 {
		fmt.Fprintln(w, "\nAttached file ID\tRole\tFile\tBytes\tSHA-256")
		for _, file := range *item.Files {
			fmt.Fprintf(w, "%s\t%s\t%q\t%d\t%s\n", file.ID, file.Role, file.OriginalFilename, file.Size, file.SHA256)
		}
	}
	return w.Flush()
}
