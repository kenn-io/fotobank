package cli

import (
	"context"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/spf13/cobra"

	"go.kenn.io/fotobank/internal/client"
	"go.kenn.io/fotobank/internal/httpapi"
	"go.kenn.io/fotobank/internal/owners"
	"go.kenn.io/fotobank/internal/version"
)

func newContentCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "content",
		Short: "Inspect and recover authoritative content",
	}
	cmd.AddCommand(newContentRecoverCmd())
	return cmd
}

func newContentRecoverCmd() *cobra.Command {
	var (
		asJSON bool
		wait   time.Duration
	)
	cmd := &cobra.Command{
		Use:   "recover",
		Short: "Finish interrupted imports and report unmatched content",
		Args:  usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfgPath, _ := cmd.Flags().GetString("config")
			return runContentRecovery(cmd.Context(), contentRecoveryOpts{
				configPath: cfgPath,
				wait:       wait,
				asJSON:     asJSON,
				stdout:     cmd.OutOrStdout(),
			})
		},
	}
	cmd.Flags().String("config", "", "path to config file")
	cmd.Flags().DurationVar(&wait, "wait", 0, "max wait for the import lock before failing (0 = fail fast)")
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit JSON to stdout")
	return cmd
}

type contentRecoveryOpts struct {
	configPath string
	wait       time.Duration
	asJSON     bool
	stdout     io.Writer
}

// runContentRecovery is a daemon client; recovery never opens local storage.
func runContentRecovery(ctx context.Context, opts contentRecoveryOpts) error {
	result := httpapi.ContentRecoveryResult{}
	var err error
	if opts.wait < 0 {
		err = fmt.Errorf("wait must be non-negative")
	} else {
		var databasePath string
		var owner owners.Principal
		databasePath, owner, err = localOperatorConfig(ctx, opts.configPath)
		if err == nil {
			result, err = client.RecoverContent(ctx, databasePath, version.Short, httpapi.ContentRecoveryRequest{
				Hub: owner.Hub, UserID: owner.UserID, Wait: opts.wait.String(),
			})
		}
	}
	if err != nil {
		result.Error = err.Error()
	}
	if opts.asJSON {
		return errors.Join(err, json.MarshalWrite(opts.stdout, result))
	}
	for _, report := range result.Reports {
		fmt.Fprintf(opts.stdout,
			"owner=%s adopted=%d finalized=%d pending=%d conflicts=%d orphans=%d\n",
			report.Owner, report.Adopted, report.Finalized, report.Pending,
			report.Conflicts, len(report.OrphanPaths))
		for _, orphanPath := range report.OrphanPaths {
			fmt.Fprintf(opts.stdout, "  unmatched: %s\n", orphanPath)
		}
	}
	return err
}
