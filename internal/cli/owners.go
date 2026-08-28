package cli

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"go.kenn.io/fotobank/internal/cli/clictx"
	"go.kenn.io/fotobank/internal/owners"
)

func newOwnersCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "owners",
		Short: "Manage registered owners (principals that own media)",
	}
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
			svc, cleanup, err := clictx.LoadOwnerService()
			if err != nil {
				return err
			}
			defer cleanup()
			p := owners.Principal{Hub: hub, UserID: userID}
			ctx := cmd.Context()
			owner, err := svc.Ensure(ctx, p, storageKey)
			if err != nil {
				return err
			}
			if handle != "" {
				if err := svc.UpdateDisplay(ctx, p, handle); err != nil {
					return err
				}
			}
			fmt.Fprintln(cmd.OutOrStdout(), "added", p, owner.StorageKey)
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
			svc, cleanup, err := clictx.LoadOwnerService()
			if err != nil {
				return err
			}
			defer cleanup()
			rows, err := svc.List(cmd.Context())
			if err != nil {
				return err
			}
			stdout := cmd.OutOrStdout()
			if jsonOut {
				out := make([]map[string]any, 0, len(rows))
				for _, o := range rows {
					out = append(out, map[string]any{
						"hub":         o.Principal.Hub,
						"user_id":     o.Principal.UserID,
						"storage_key": o.StorageKey,
						"handle":      o.DisplayHandle,
						"created_at":  o.CreatedAt,
					})
				}
				return json.NewEncoder(stdout).Encode(out)
			}
			for _, o := range rows {
				fmt.Fprintf(stdout, "%s\t%s\t%s\n", o.Principal, o.StorageKey, o.DisplayHandle)
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
		Short: "Unregister an owner (refuses if media still references them)",
		Args:  usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			if hub == "" || userID == "" {
				return newUsageError("--hub and --user-id are required")
			}
			svc, cleanup, err := clictx.LoadOwnerService()
			if err != nil {
				return err
			}
			defer cleanup()
			if err := svc.Remove(cmd.Context(), owners.Principal{Hub: hub, UserID: userID}, purge); err != nil {
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
