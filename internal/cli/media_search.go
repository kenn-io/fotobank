package cli

import (
	json "encoding/json/v2"
	"fmt"
	"io"
	"strconv"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	"go.kenn.io/fotobank/internal/client"
	"go.kenn.io/fotobank/internal/httpapi"
	"go.kenn.io/fotobank/internal/version"
)

func newMediaSearchCmd() *cobra.Command {
	var input httpapi.SearchInput
	var cfgPath, dateAfter, dateBefore string
	var asJSON, hasGPS bool
	cmd := &cobra.Command{
		Use: "search [query]", Short: "Search visible photos and videos",
		Long: "Search the configured owner's library through the daemon. Quote queries containing spaces. Without a query, browse using filters. AI is optional; metadata search remains available without embeddings. Hidden media is excluded.",
		Args: usageArgs(cobra.MaximumNArgs(1)),
		Example: `  fotobank media search "sunset" --type photo --limit 20 --json
  fotobank media search "forest walk" --camera "Example Camera" --has-gps
  fotobank media search --date-after 2026-01-01T00:00:00Z --sort newest
  fotobank media search "sunset" --type photo --limit 20 --cursor <next-cursor> --json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) > 0 {
				input.Q = args[0]
			}
			if input.MediaType != "" && input.MediaType != "photo" && input.MediaType != "video" {
				return newUsageError("--type must be photo or video")
			}
			if input.Sort != "relevance" && input.Sort != "newest" && input.Sort != "oldest" {
				return newUsageError("--sort must be relevance, newest, or oldest")
			}
			if input.Limit < 1 || input.Limit > 200 {
				return newUsageError("--limit must be between 1 and 200")
			}
			for _, field := range []struct {
				name, value string
				target      *time.Time
			}{{"date-after", dateAfter, &input.DateAfter}, {"date-before", dateBefore, &input.DateBefore}} {
				if cmd.Flags().Changed(field.name) {
					stamp, err := time.Parse(time.RFC3339Nano, field.value)
					if err != nil {
						return newUsageError("--%s must be an RFC3339 timestamp", field.name)
					}
					*field.target = stamp
				}
			}
			if cmd.Flags().Changed("has-gps") {
				input.HasGPS = strconv.FormatBool(hasGPS)
			}
			resolved, _, err := localOperatorConfig(cmd.Context(), cfgPath)
			if err != nil {
				return err
			}
			page, err := client.SearchMedia(cmd.Context(), resolved, version.Short, input)
			if err != nil {
				return err
			}
			if asJSON {
				return json.MarshalWrite(cmd.OutOrStdout(), page)
			}
			return printMediaSearch(cmd.OutOrStdout(), page)
		},
	}
	flags := cmd.Flags()
	flags.StringVar(&cfgPath, "config", "", "path to config file")
	flags.BoolVar(&asJSON, "json", false, "emit the API page, including next_cursor and search availability, as JSON")
	flags.StringVar(&input.MediaType, "type", "", "photo or video (default: both)")
	flags.StringVar(&input.Sort, "sort", "relevance", "relevance, newest, or oldest; empty queries use newest by default")
	flags.IntVar(&input.Limit, "limit", 60, "page size (1–200)")
	flags.StringVar(&input.Cursor, "cursor", "", "next_cursor from a previous page; keep the query and filters unchanged")
	flags.StringVar(&dateAfter, "date-after", "", "inclusive capture time, as RFC3339")
	flags.StringVar(&dateBefore, "date-before", "", "exclusive capture time, as RFC3339")
	flags.StringVar(&input.Location, "location", "", "exact location label")
	flags.StringArrayVar(&input.Camera, "camera", nil, "exact make and model; repeat to match any")
	flags.StringArrayVar(&input.Lens, "lens", nil, "exact lens model; repeat to match any")
	flags.StringArrayVar(&input.FacetTag, "tag", nil, "tag key; repeat to match any")
	flags.StringArrayVar(&input.Tag, "tag-label", nil, "tag label; repeat to require all labels")
	flags.BoolVar(&hasGPS, "has-gps", false, "filter geotagged media; --has-gps=false selects media without GPS")
	_ = cmd.RegisterFlagCompletionFunc("type", cobra.FixedCompletions([]string{"photo", "video"}, cobra.ShellCompDirectiveNoFileComp))
	_ = cmd.RegisterFlagCompletionFunc("sort", cobra.FixedCompletions([]string{"relevance", "newest", "oldest"}, cobra.ShellCompDirectiveNoFileComp))
	return cmd
}

func printMediaSearch(out io.Writer, page httpapi.SearchBody) error {
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintf(w, "Sort: %s\n", page.EffectiveSort)
	if page.SemanticUnavailable {
		if page.SemanticUnavailableReason == "embeddings_disabled" {
			fmt.Fprintln(w, "Searching metadata; embeddings are disabled.")
		} else {
			fmt.Fprintln(w, "Searching metadata; semantic search is unavailable.")
		}
	}
	fmt.Fprintln(w, "ID\tTYPE\tCAPTURED\tSCORE")
	for _, item := range page.Results {
		stamp := "-"
		if item.Timestamp != nil {
			stamp = item.Timestamp.Format(time.RFC3339Nano)
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%.6g\n", item.MediaID, item.MediaType, stamp, item.Score)
	}
	if page.NextCursor != nil {
		fmt.Fprintf(w, "More results: repeat with --cursor %s and the same query and filters.\n", *page.NextCursor)
	}
	return w.Flush()
}
