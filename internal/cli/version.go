package cli

import (
	"github.com/spf13/cobra"

	"github.com/wesm/fotobank/internal/version"
)

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the fotobank version",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			_, err := cmd.OutOrStdout().Write([]byte(version.Format() + "\n"))
			return err
		},
	}
}
