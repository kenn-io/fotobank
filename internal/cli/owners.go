package cli

import (
	"context"
	json "encoding/json/v2"
	"fmt"
	"uuid"

	"github.com/spf13/cobra"

	"go.kenn.io/fotobank/internal/client"
	"go.kenn.io/fotobank/internal/httpapi"
)

func ensureOwnerDaemon(ctx context.Context, cfgPath string) (client.Lifecycle, error) {
	lifecycle, err := daemonLifecycle(cfgPath, "")
	if err != nil {
		return lifecycle, err
	}
	_, err = lifecycle.Ensure(ctx)
	return lifecycle, err
}

func newOwnersCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "owners",
		Short: "Manage registered owners (principals that own media)",
	}
	cmd.PersistentFlags().String("config", "", "config file path")
	cmd.AddCommand(newOwnersAddCmd())
	cmd.AddCommand(newOwnersListCmd())
	cmd.AddCommand(newOwnersRemoveCmd())
	return cmd
}

func newOwnersAddCmd() *cobra.Command {
	var (
		hub        string
		userID     string
		storageKey string
		handle     string
	)
	cmd := &cobra.Command{
		Use:   "add",
		Short: "Register a new owner",
		Args:  usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			if hub == "" || userID == "" {
				return newUsageError("--hub and --user-id are required")
			}
			request := httpapi.RegisterOwnerRequest{Hub: hub, UserID: userID, Handle: handle}
			if storageKey != "" {
				key, err := uuid.Parse(storageKey)
				if err != nil {
					return newUsageError("--storage-key must be a UUID")
				}
				request.StorageKey = &key
			}
			cfgPath, _ := cmd.Flags().GetString("config")
			lifecycle, err := ensureOwnerDaemon(cmd.Context(), cfgPath)
			if err != nil {
				return err
			}
			owner, err := client.RegisterOwner(cmd.Context(), lifecycle.DBPath, lifecycle.Version, request)
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "added", owner.Hub+":"+owner.UserID, owner.StorageKey)
			return nil
		},
	}
	cmd.Flags().StringVar(&hub, "hub", "", "identity hub (required)")
	cmd.Flags().StringVar(&userID, "user-id", "", "user ID within the hub (required)")
	cmd.Flags().StringVar(&storageKey, "storage-key", "", "optional deterministic storage UUID")
	cmd.Flags().StringVar(&handle, "handle", "", "optional display handle")
	return cmd
}

func newOwnersListCmd() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List registered owners",
		Args:  usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfgPath, _ := cmd.Flags().GetString("config")
			lifecycle, err := ensureOwnerDaemon(cmd.Context(), cfgPath)
			if err != nil {
				return err
			}
			result, err := client.ListOwners(cmd.Context(), lifecycle.DBPath, lifecycle.Version)
			if err != nil {
				return err
			}
			stdout := cmd.OutOrStdout()
			if jsonOut {
				return json.MarshalWrite(stdout, result)
			}
			for _, o := range result.Items {
				fmt.Fprintf(stdout, "%s:%s\t%s\t%s\n", o.Hub, o.UserID, o.StorageKey, o.Handle)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit JSON instead of tab-separated text")
	return cmd
}

func newOwnersRemoveCmd() *cobra.Command {
	var (
		hub    string
		userID string
		purge  bool
	)
	cmd := &cobra.Command{
		Use:   "remove",
		Short: "Unregister an owner (refuses if assets or checkouts reference them)",
		Args:  usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			if hub == "" || userID == "" {
				return newUsageError("--hub and --user-id are required")
			}
			if purge {
				return newUsageError("--purge is not implemented")
			}
			cfgPath, _ := cmd.Flags().GetString("config")
			lifecycle, err := ensureOwnerDaemon(cmd.Context(), cfgPath)
			if err != nil {
				return err
			}
			if err := client.RemoveOwner(cmd.Context(), lifecycle.DBPath, lifecycle.Version, httpapi.RemoveOwnerRequest{Hub: hub, UserID: userID}); err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "removed")
			return nil
		},
	}
	cmd.Flags().StringVar(&hub, "hub", "", "identity hub (required)")
	cmd.Flags().StringVar(&userID, "user-id", "", "user ID within the hub (required)")
	cmd.Flags().BoolVar(&purge, "purge", false, "also remove owned media (not implemented)")
	return cmd
}
