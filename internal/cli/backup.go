package cli

import (
	"errors"
	"github.com/spf13/cobra"
)

func newBackupCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "backup", Short: "Create, verify, and restore complete recovery archives"}
	cmd.AddCommand(newBackupInitCmd(), newBackupCreateCmd(), newBackupListCmd(), newBackupVerifyCmd(), newBackupRestoreCmd())
	return cmd
}

func newBackupListCmd() *cobra.Command {
	var asJSON bool
	var repositoryPath string
	cmd := &cobra.Command{
		Use: "list", Short: "List recovery points in an archive repository", Args: usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			if repositoryPath == "" {
				return errors.New("backup list requires --repo")
			}
			return listArchives(cmd, repositoryPath, asJSON)
		},
	}
	cmd.Flags().StringVar(&repositoryPath, "repo", "", "archive repository to list")
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit JSON to stdout")
	return cmd
}

func newBackupRestoreCmd() *cobra.Command {
	var asJSON bool
	var repositoryPath, target string
	cmd := &cobra.Command{
		Use: "restore [snapshot-id]", Short: "Restore a complete archive into a separate empty directory",
		Args: usageArgs(cobra.MaximumNArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			if repositoryPath == "" || target == "" {
				return errors.New("backup restore requires --repo and --target; use backup verify for a read-only check")
			}
			id := ""
			if len(args) > 0 {
				id = args[0]
			}
			return restoreArchive(cmd, repositoryPath, id, target, asJSON)
		},
	}
	cmd.Flags().String("config", "", "path to config file")
	cmd.Flags().StringVar(&repositoryPath, "repo", "", "archive repository to restore")
	cmd.Flags().StringVar(&target, "target", "", "separate empty directory for recovery")
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit JSON to stdout")
	return cmd
}
