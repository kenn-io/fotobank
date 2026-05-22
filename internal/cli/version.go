package cli

import (
	"github.com/spf13/cobra"

	"go.kenn.io/fotobank/internal/version"
)

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the fotobank version",
		Args:  usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			_, err := cmd.OutOrStdout().Write([]byte(version.Format() + "\n"))
			return err
		},
	}
}
