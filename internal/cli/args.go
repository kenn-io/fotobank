package cli

import "github.com/spf13/cobra"

// usageArgs wraps a cobra.PositionalArgs validator so any error it
// produces is tagged as a usageError. Callers use it like:
//
//	Args: usageArgs(cobra.NoArgs)
//
// Execute's error handler can then reliably map argument-validation
// failures to exit code 2 via errors.As, without matching on error
// message text.
func usageArgs(v cobra.PositionalArgs) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if err := v(cmd, args); err != nil {
			return usageError{err: err}
		}
		return nil
	}
}
