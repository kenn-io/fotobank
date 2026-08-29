package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/gofrs/flock"
	"github.com/spf13/cobra"

	"go.kenn.io/fotobank/internal/checkout"
	"go.kenn.io/fotobank/internal/config"
	"go.kenn.io/fotobank/internal/content"
	"go.kenn.io/fotobank/internal/contentresolver"
	"go.kenn.io/fotobank/internal/db"
	"go.kenn.io/fotobank/internal/errs"
	"go.kenn.io/fotobank/internal/media"
	"go.kenn.io/fotobank/internal/owners"
	"go.kenn.io/fotobank/internal/service"
)

func newCheckoutCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "checkout",
		Short: "Create writable working copies from Docbank content",
	}
	cmd.AddCommand(newCheckoutEstimateCmd())
	cmd.AddCommand(newCheckoutCreateCmd())
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
		Args:  usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			selection, err := flags.selection()
			if err != nil {
				return err
			}
			cfgPath, _ := cmd.Flags().GetString("config")
			return runCheckoutEstimate(cmd.Context(), cfgPath, selection, cmd.OutOrStdout())
		},
	}
	cmd.Flags().String("config", "", "path to config file")
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
		Args:  usageArgs(cobra.ExactArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			selection, err := flags.selection()
			if err != nil {
				return err
			}
			cfgPath, _ := cmd.Flags().GetString("config")
			return runCheckoutCreate(cmd.Context(), cfgPath, args[0], selection, maxBytes, cmd.OutOrStdout())
		},
	}
	cmd.Flags().String("config", "", "path to config file")
	cmd.Flags().Int64Var(&maxBytes, "max-bytes", 0,
		"refuse materialization above this byte count (required with --all)")
	flags.bind(cmd)
	return cmd
}

func runCheckoutEstimate(
	ctx context.Context,
	configPath string,
	selection checkout.Selection,
	stdout io.Writer,
) error {
	runtime, err := openCheckoutRuntime(ctx, configPath, false)
	if err != nil {
		return err
	}
	defer runtime.close()
	estimate, err := runtime.service.Estimate(ctx, runtime.owner, selection)
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "files=%d\tbytes=%d\n", estimate.Files, estimate.Bytes)
	return nil
}

func runCheckoutCreate(
	ctx context.Context,
	configPath string,
	root string,
	selection checkout.Selection,
	maxBytes int64,
	stdout io.Writer,
) error {
	runtime, err := openCheckoutRuntime(ctx, configPath, true)
	if err != nil {
		return err
	}
	defer runtime.close()
	resolvedRoot, err := runtime.content.ResolveCheckoutRoot(root)
	if err != nil {
		return err
	}
	defer resolvedRoot.Close()
	result, err := runtime.service.Create(ctx, runtime.owner, checkout.CreateRequest{
		Root: resolvedRoot, Selection: selection, CapacityLimit: maxBytes,
	})
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "checkout=%s\tfiles=%d\tbytes=%d\troot=%s\n",
		result.Checkout.ID, result.Estimate.Files, result.Estimate.Bytes, result.Checkout.Root)
	return nil
}

type checkoutRuntime struct {
	db           *db.DB
	databaseLock *flock.Flock
	content      *content.Adapter
	service      *service.CheckoutService
	owner        owners.Principal
}

func openCheckoutRuntime(ctx context.Context, configPath string, withContent bool) (*checkoutRuntime, error) {
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
	dbPath := resolveDBPath(cfg)
	lockPath := lockPathFor(dbPath)
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o700); err != nil {
		return nil, fmt.Errorf("create database lock directory: %w", err)
	}
	databaseLock := flock.New(lockPath)
	locked, err := databaseLock.TryRLock()
	if err != nil {
		return nil, fmt.Errorf("lock database lifetime: %w", err)
	}
	if !locked {
		return nil, fmt.Errorf("open checkout: %w: another process is replacing the database", errs.ErrAlreadyExists)
	}
	database, err := db.Open(dbPath)
	if err != nil {
		_ = databaseLock.Unlock()
		return nil, err
	}
	owner := owners.Principal{Hub: cfg.Identity.Stub.Hub, UserID: cfg.Identity.Stub.UserID}
	runtime := &checkoutRuntime{db: database, databaseLock: databaseLock, owner: owner}
	ownerService := service.NewOwnerService(owners.NewRepo(database.WriteDB(), database.ReadDB()))
	if _, err := ownerService.Ensure(ctx, owner, cfg.Identity.Stub.StorageKey); err != nil {
		runtime.close()
		return nil, err
	}
	checkoutRepo := checkout.NewRepo(database.WriteDB(), database.ReadDB())
	if !withContent {
		runtime.service = service.NewCheckoutService(checkoutRepo, nil, "")
		return runtime, nil
	}
	contentStore, err := content.Open(ctx, content.Config{
		Root: cfg.Docbank.Root, ManagedRoots: []string{cfg.NAS.Root, cfg.Flash.Root},
	})
	if err != nil {
		runtime.close()
		return nil, fmt.Errorf("open Docbank vault: %w", err)
	}
	runtime.content = contentStore
	resolver := contentresolver.New(media.NewRepo(database.WriteDB(), database.ReadDB()), contentStore)
	runtime.service = service.NewCheckoutService(checkoutRepo, resolver, dbPath+".checkout.lock")
	return runtime, nil
}

func (r *checkoutRuntime) close() {
	if r.content != nil {
		_ = r.content.Close()
	}
	if r.db != nil {
		_ = r.db.Close()
	}
	if r.databaseLock != nil {
		_ = r.databaseLock.Unlock()
	}
}
