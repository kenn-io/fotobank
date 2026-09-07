package cli

import (
	"context"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"go.kenn.io/fotobank/internal/checkout"
	"go.kenn.io/fotobank/internal/config"
	"go.kenn.io/fotobank/internal/operator"
	"go.kenn.io/fotobank/internal/owners"
	"go.kenn.io/fotobank/internal/service"
	"go.kenn.io/fotobank/internal/version"
)

func newCheckoutCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "checkout",
		Short: "Create writable working copies from Docbank content",
	}
	cmd.AddCommand(newCheckoutEstimateCmd())
	cmd.AddCommand(newCheckoutCreateCmd())
	cmd.AddCommand(newCheckoutCommitCmd())
	cmd.AddCommand(newCheckoutListCmd())
	cmd.AddCommand(newCheckoutStatusCmd())
	return cmd
}

func newCheckoutListCmd() *cobra.Command {
	var (
		configPath string
		asJSON     bool
	)
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List writable checkouts and their current state",
		Args:  usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runCheckoutList(cmd.Context(), configPath, asJSON, cmd.OutOrStdout())
		},
	}
	cmd.Flags().StringVar(&configPath, "config", "", "path to config file")
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit JSON to stdout")
	return cmd
}

func newCheckoutStatusCmd() *cobra.Command {
	var (
		configPath string
		asJSON     bool
	)
	cmd := &cobra.Command{
		Use:   "status <checkout-id>",
		Short: "Show checkout state and files that need attention",
		Args:  usageArgs(cobra.ExactArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runCheckoutStatus(cmd.Context(), configPath, args[0], asJSON, cmd.OutOrStdout())
		},
	}
	cmd.Flags().StringVar(&configPath, "config", "", "path to config file")
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit JSON to stdout")
	return cmd
}

type checkoutSelectionFlags struct {
	assets []string
	albums []string
	years  []string
	all    bool
}

func (f *checkoutSelectionFlags) bind(cmd *cobra.Command) {
	cmd.Flags().StringArrayVar(&f.assets, "asset", nil, "include an asset UUID (repeatable)")
	cmd.Flags().StringArrayVar(&f.albums, "album", nil, "include an album UUID (repeatable)")
	cmd.Flags().StringArrayVar(&f.years, "year", nil, "include YYYY or YYYY:YYYY (repeatable)")
	cmd.Flags().BoolVar(&f.all, "all", false, "include every ready asset")
}

func (f checkoutSelectionFlags) selection() (checkout.Selection, error) {
	selection := checkout.Selection{
		All: f.all, AssetIDs: f.assets, AlbumIDs: f.albums,
	}
	for _, value := range f.years {
		parts := strings.Split(value, ":")
		if len(parts) > 2 || parts[0] == "" {
			return checkout.Selection{}, newUsageError("invalid --year %q; use YYYY or YYYY:YYYY", value)
		}
		start, err := strconv.Atoi(parts[0])
		if err != nil {
			return checkout.Selection{}, newUsageError("invalid --year %q; use YYYY or YYYY:YYYY", value)
		}
		end := start
		if len(parts) == 2 {
			end, err = strconv.Atoi(parts[1])
			if err != nil {
				return checkout.Selection{}, newUsageError("invalid --year %q; use YYYY or YYYY:YYYY", value)
			}
		}
		selection.Years = append(selection.Years, checkout.YearRange{Start: start, End: end})
	}
	return selection, nil
}

func newCheckoutEstimateCmd() *cobra.Command {
	var flags checkoutSelectionFlags
	cmd := &cobra.Command{
		Use:   "estimate",
		Short: "Estimate the files and bytes selected for a checkout",
		Long: "Estimate selected files through the running Fotobank server. " +
			"Use the same OS account, stub-mode configuration, and application version as serve.",
		Args: usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			selection, err := flags.selection()
			if err != nil {
				return err
			}
			cfgPath, _ := cmd.Flags().GetString("config")
			asJSON, _ := cmd.Flags().GetBool("json")
			return runCheckoutEstimate(cmd.Context(), cfgPath, selection, asJSON, cmd.OutOrStdout())
		},
	}
	cmd.Flags().String("config", "", "path to config file")
	cmd.Flags().Bool("json", false, "write structured file and byte estimates")
	flags.bind(cmd)
	return cmd
}

func newCheckoutCreateCmd() *cobra.Command {
	var (
		flags    checkoutSelectionFlags
		maxBytes int64
	)
	cmd := &cobra.Command{
		Use:   "create <empty-root>",
		Short: "Materialize an exact-version writable checkout",
		Long: "Materialize an exact-version writable checkout through the running Fotobank server. " +
			"Use the same OS account, stub-mode configuration, and application version as serve. " +
			"Do not open or edit the checkout root until this command finishes.",
		Args: usageArgs(cobra.ExactArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			selection, err := flags.selection()
			if err != nil {
				return err
			}
			cfgPath, _ := cmd.Flags().GetString("config")
			asJSON, _ := cmd.Flags().GetBool("json")
			return runCheckoutCreate(cmd.Context(), cfgPath, args[0], selection, maxBytes, asJSON, cmd.OutOrStdout())
		},
	}
	cmd.Flags().String("config", "", "path to config file")
	cmd.Flags().Bool("json", false, "write structured checkout results and errors")
	cmd.Flags().Int64Var(&maxBytes, "max-bytes", 0,
		"refuse materialization above this byte count (required with --all)")
	flags.bind(cmd)
	return cmd
}

func newCheckoutCommitCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "commit <checkout-id>",
		Short: "Commit settled tracked edits to Docbank",
		Args:  usageArgs(cobra.ExactArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfgPath, _ := cmd.Flags().GetString("config")
			jsonOutput, _ := cmd.Flags().GetBool("json")
			return runCheckoutCommit(cmd.Context(), cfgPath, args[0], jsonOutput, cmd.OutOrStdout())
		},
	}
	cmd.Flags().String("config", "", "path to config file")
	cmd.Flags().Bool("json", false, "write structured commit counts and errors")
	return cmd
}

func runCheckoutEstimate(
	ctx context.Context,
	configPath string,
	selection checkout.Selection,
	asJSON bool,
	stdout io.Writer,
) error {
	dbPath, owner, err := localOperatorConfig(configPath)
	var result operator.EstimateResult
	if err == nil {
		result, err = operator.Estimate(ctx, dbPath, version.Short, operator.EstimateRequest{
			Hub: owner.Hub, UserID: owner.UserID, Selection: selection,
		})
	}
	if asJSON {
		if err != nil {
			result.Error = err.Error()
		}
		return errors.Join(err, writeCheckoutJSON(stdout, result))
	}
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(stdout, "files=%d\tbytes=%d\n", result.Files, result.Bytes)
	return err
}

func runCheckoutCreate(
	ctx context.Context,
	configPath string,
	root string,
	selection checkout.Selection,
	maxBytes int64,
	asJSON bool,
	stdout io.Writer,
) error {
	dbPath, owner, err := localOperatorConfig(configPath)
	if err == nil {
		root, err = localOperatorPath(root)
	}
	result := operator.CreateResult{Root: root}
	if err == nil {
		result, err = operator.Create(ctx, dbPath, version.Short, operator.CreateRequest{
			Hub: owner.Hub, UserID: owner.UserID, Root: root, Selection: selection, MaxBytes: maxBytes,
		})
	}
	if asJSON {
		if err != nil {
			result.Error = err.Error()
		}
		return errors.Join(err, writeCheckoutJSON(stdout, result))
	}
	if err != nil && result.CheckoutID == "" {
		return err
	}
	_, outputErr := fmt.Fprintf(stdout, "checkout=%s\tfiles=%d\tbytes=%d\tmaterialized=%d\troot=%s\n",
		result.CheckoutID, result.Files, result.Bytes, result.Materialized, result.Root)
	return errors.Join(err, outputErr)
}

func runCheckoutCommit(
	ctx context.Context,
	configPath string,
	checkoutID string,
	jsonOutput bool,
	stdout io.Writer,
) error {
	dbPath, owner, err := localOperatorConfig(configPath)
	if err != nil {
		return err
	}
	result, commitErr := operator.Commit(ctx, dbPath, version.Short, checkoutID,
		owner)
	if jsonOutput {
		if commitErr != nil && result.Error == "" {
			result.Error = commitErr.Error()
		}
		if err := json.MarshalWrite(stdout, result); err != nil {
			return errors.Join(commitErr, err)
		}
		return commitErr
	}
	_, outputErr := fmt.Fprintf(stdout, "pending=%d\tcommitted=%d\tconflicts=%d\n",
		result.Pending, result.Committed, result.Conflicts)
	return errors.Join(commitErr, outputErr)
}

type checkoutSummaryOutput struct {
	ID        string                    `json:"id"`
	State     checkout.State            `json:"state"`
	Root      string                    `json:"root"`
	Layout    string                    `json:"layout"`
	LastError string                    `json:"last_error"`
	Entries   checkoutEntryCountsOutput `json:"entries"`
	CreatedAt time.Time                 `json:"created_at"`
	UpdatedAt time.Time                 `json:"updated_at"`
}

type checkoutEntryCountsOutput struct {
	Total    int `json:"total"`
	Clean    int `json:"clean"`
	Pending  int `json:"pending"`
	Conflict int `json:"conflict"`
	Missing  int `json:"missing"`
	Error    int `json:"error"`
}

type checkoutSelectionOutput struct {
	All      bool                 `json:"all"`
	AssetIDs []string             `json:"asset_ids"`
	AlbumIDs []string             `json:"album_ids"`
	Years    []checkoutYearOutput `json:"years"`
}

type checkoutYearOutput struct {
	Start int `json:"start"`
	End   int `json:"end"`
}

type checkoutProblemOutput struct {
	FileID         string              `json:"file_id"`
	Path           string              `json:"path"`
	State          checkout.EntryState `json:"state"`
	LastError      string              `json:"last_error"`
	BaseVersionID  string              `json:"base_version_id"`
	BaseSHA256     string              `json:"base_sha256"`
	ObservedSHA256 string              `json:"observed_sha256"`
	UpdatedAt      time.Time           `json:"updated_at"`
}

type checkoutStatusOutput struct {
	Checkout  checkoutSummaryOutput   `json:"checkout"`
	Selection checkoutSelectionOutput `json:"selection"`
	Problems  []checkoutProblemOutput `json:"problems"`
}

func runCheckoutList(
	ctx context.Context,
	configPath string,
	asJSON bool,
	stdout io.Writer,
) error {
	runtime, err := openCheckoutRuntime(ctx, configPath)
	if err != nil {
		return err
	}
	defer runtime.close()
	rows, err := runtime.service.List(ctx, runtime.owner)
	if err != nil {
		return err
	}
	out := make([]checkoutSummaryOutput, len(rows))
	for index, row := range rows {
		out[index] = projectCheckoutSummary(row)
	}
	if asJSON {
		return writeCheckoutJSON(stdout, out)
	}
	tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tSTATE\tFILES\tPENDING\tCONFLICTS\tMISSING\tERRORS\tUPDATED\tROOT")
	for _, row := range rows {
		fmt.Fprintf(tw, "%s\t%s\t%d\t%d\t%d\t%d\t%d\t%s\t%s\n",
			row.ID, row.State, row.Entries.Total, row.Entries.Pending,
			row.Entries.Conflict, row.Entries.Missing, row.Entries.Error,
			row.UpdatedAt.Format(time.RFC3339), row.Root)
	}
	return tw.Flush()
}

func runCheckoutStatus(
	ctx context.Context,
	configPath, checkoutID string,
	asJSON bool,
	stdout io.Writer,
) error {
	runtime, err := openCheckoutRuntime(ctx, configPath)
	if err != nil {
		return err
	}
	defer runtime.close()
	status, err := runtime.service.Status(ctx, runtime.owner, checkoutID)
	if err != nil {
		return err
	}
	out := projectCheckoutStatus(status)
	if asJSON {
		return writeCheckoutJSON(stdout, out)
	}
	fmt.Fprintf(stdout, "Checkout: %s\nState: %s\nRoot: %s\nSelection: %s\n",
		status.Checkout.ID, status.Checkout.State, status.Checkout.Root,
		formatCheckoutSelection(status.Selection))
	fmt.Fprintf(stdout, "Files: %d total; %d clean, %d pending, %d conflicts, %d missing, %d errors\n",
		status.Checkout.Entries.Total, status.Checkout.Entries.Clean,
		status.Checkout.Entries.Pending, status.Checkout.Entries.Conflict,
		status.Checkout.Entries.Missing, status.Checkout.Entries.Error)
	if status.Checkout.LastError != "" {
		fmt.Fprintf(stdout, "Checkout error: %s\n", status.Checkout.LastError)
	}
	if len(status.Problems) == 0 {
		_, err := fmt.Fprintln(stdout, "Files needing attention: none")
		return err
	}
	tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "STATE\tPATH\tFILE ID\tLAST ERROR")
	for _, entry := range status.Problems {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n",
			entry.State, entry.RelativePath, entry.FileID, entry.LastError)
	}
	return tw.Flush()
}

func projectCheckoutSummary(summary checkout.Summary) checkoutSummaryOutput {
	return checkoutSummaryOutput{
		ID: summary.ID, State: summary.State, Root: summary.Root, Layout: summary.Layout,
		LastError: summary.LastError, Entries: checkoutEntryCountsOutput{
			Total: summary.Entries.Total, Clean: summary.Entries.Clean,
			Pending: summary.Entries.Pending, Conflict: summary.Entries.Conflict,
			Missing: summary.Entries.Missing, Error: summary.Entries.Error,
		},
		CreatedAt: summary.CreatedAt, UpdatedAt: summary.UpdatedAt,
	}
}

func projectCheckoutStatus(status checkout.Status) checkoutStatusOutput {
	years := make([]checkoutYearOutput, len(status.Selection.Years))
	for index, yearRange := range status.Selection.Years {
		years[index] = checkoutYearOutput{Start: yearRange.Start, End: yearRange.End}
	}
	problems := make([]checkoutProblemOutput, len(status.Problems))
	for index, entry := range status.Problems {
		problems[index] = checkoutProblemOutput{
			FileID: entry.FileID, Path: entry.RelativePath, State: entry.State,
			LastError: entry.LastError, BaseVersionID: entry.BaseVersionID,
			BaseSHA256: entry.BaseSHA256, ObservedSHA256: entry.ObservedSHA256,
			UpdatedAt: entry.UpdatedAt,
		}
	}
	return checkoutStatusOutput{
		Checkout: projectCheckoutSummary(status.Checkout),
		Selection: checkoutSelectionOutput{
			All:      status.Selection.All,
			AssetIDs: append([]string(nil), status.Selection.AssetIDs...),
			AlbumIDs: append([]string(nil), status.Selection.AlbumIDs...),
			Years:    years,
		},
		Problems: problems,
	}
}

func formatCheckoutSelection(selection checkout.Selection) string {
	if selection.All {
		return "all visible assets"
	}
	parts := make([]string, 0, len(selection.AssetIDs)+len(selection.AlbumIDs)+len(selection.Years))
	for _, id := range selection.AssetIDs {
		parts = append(parts, "asset "+id)
	}
	for _, id := range selection.AlbumIDs {
		parts = append(parts, "album "+id)
	}
	for _, years := range selection.Years {
		if years.Start == years.End {
			parts = append(parts, fmt.Sprintf("year %d", years.Start))
		} else {
			parts = append(parts, fmt.Sprintf("years %d:%d", years.Start, years.End))
		}
	}
	return strings.Join(parts, ", ")
}

func writeCheckoutJSON(w io.Writer, value any) error {
	if err := json.MarshalWrite(w, value); err != nil {
		return fmt.Errorf("write checkout JSON: %w", err)
	}
	_, err := fmt.Fprintln(w)
	return err
}

type checkoutRuntime struct {
	db      *databaseHandle
	service *service.CheckoutService
	owner   owners.Principal
}

func openCheckoutRuntime(ctx context.Context, configPath string) (*checkoutRuntime, error) {
	if configPath == "" {
		configPath = config.DefaultConfigPath()
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		return nil, err
	}
	if cfg.Identity.Mode != "stub" {
		return nil, fmt.Errorf("fotobank checkout requires identity.mode = stub (got %q)", cfg.Identity.Mode)
	}
	dbPath, err := resolveDBPath(cfg)
	if err != nil {
		return nil, err
	}
	database, err := openDatabasePath(dbPath)
	if err != nil {
		return nil, err
	}
	owner := owners.Principal{Hub: cfg.Identity.Stub.Hub, UserID: cfg.Identity.Stub.UserID}
	runtime := &checkoutRuntime{db: database, owner: owner}
	ownerService := service.NewOwnerService(owners.NewRepo(database.WriteDB(), database.ReadDB()))
	if _, err := ownerService.Ensure(ctx, owner, cfg.Identity.Stub.StorageKey); err != nil {
		runtime.close()
		return nil, err
	}
	checkoutRepo := checkout.NewRepo(database.WriteDB(), database.ReadDB())
	runtime.service = service.NewCheckoutService(checkoutRepo, nil, nil, "", nil)
	return runtime, nil
}

func (r *checkoutRuntime) close() {
	if r.db != nil {
		_ = r.db.Close()
	}
}
