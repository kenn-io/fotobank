package cli

import (
	json "encoding/json/v2"
	"fmt"
	"path/filepath"

	"github.com/spf13/cobra"
	"go.kenn.io/fotobank/internal/client"
	"go.kenn.io/fotobank/internal/config"
	"go.kenn.io/fotobank/internal/httpapi"
	"go.kenn.io/fotobank/internal/version"
)

func daemonLifecycle(configPath, listen string) (client.Lifecycle, error) {
	if configPath == "" {
		configPath = config.DefaultConfigPath()
	}
	configPath, err := localOperatorPath(configPath)
	if err != nil {
		return client.Lifecycle{}, err
	}
	cfg, err := config.LoadUnchecked(configPath)
	if err != nil {
		return client.Lifecycle{}, err
	}
	configPath, err = filepath.EvalSymlinks(configPath)
	if err != nil {
		return client.Lifecycle{}, err
	}
	return client.Lifecycle{ConfigPath: configPath, Version: version.Short, Listen: listen,
		StartTimeout: cfg.Daemon.StartTimeout, StopTimeout: cfg.Daemon.StopTimeout}, nil
}

func newDaemonCmd() *cobra.Command {
	var configPath, listen string
	var asJSON bool
	var recovery bool
	cmd := &cobra.Command{Use: "daemon", Short: "Manage the Fotobank daemon"}
	cmd.PersistentFlags().StringVar(&configPath, "config", "", "path to config.toml")
	cmd.PersistentFlags().BoolVar(&asJSON, "json", false, "machine-readable output")
	for _, action := range []string{"start", "stop", "restart", "status"} {
		child := &cobra.Command{Use: action, Short: action + " the Fotobank daemon", Args: usageArgs(cobra.NoArgs),
			RunE: func(cmd *cobra.Command, _ []string) error {
				lifecycle, err := daemonLifecycle(configPath, listen)
				if err != nil {
					return err
				}
				lifecycle.Recovery = recovery
				var status httpapi.DaemonStatus
				switch action {
				case "stop":
					err = lifecycle.Stop(cmd.Context())
				case "status":
					status, err = lifecycle.Status(cmd.Context())
				case "restart":
					if err = lifecycle.Stop(cmd.Context()); err == nil {
						status, err = lifecycle.Ensure(cmd.Context())
					}
				case "start":
					status, err = lifecycle.Ensure(cmd.Context())
				}
				if err != nil {
					return err
				}
				if asJSON {
					return json.MarshalWrite(cmd.OutOrStdout(), status)
				}
				if !status.Running {
					_, err = fmt.Fprintln(cmd.OutOrStdout(), "daemon not running")
					return err
				}
				if status.Recovery {
					_, err = fmt.Fprintf(cmd.OutOrStdout(), "Fotobank is running in recovery mode (pid %d). Photo operations are unavailable.\n", status.PID)
					return err
				}
				_, err = fmt.Fprintf(cmd.OutOrStdout(), "Fotobank is running (pid %d).\nWeb UI: %s\n", status.PID, status.WebURL)
				return err
			}}
		if action == "start" || action == "restart" {
			child.Flags().BoolVar(&recovery, "recovery", false, "start without opening photo storage")
			child.Flags().StringVar(&listen, "listen", "", "override [http].listen_address")
		}
		cmd.AddCommand(child)
	}
	run := &cobra.Command{Use: "run", Short: "Run Fotobank in the foreground", Args: usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runServer(cmd.Context(), serverOpts{cfgPath: configPath, listen: listen, recovery: recovery, stdout: cmd.OutOrStdout(), stderr: cmd.ErrOrStderr()})
		}}
	run.Flags().StringVar(&listen, "listen", "", "override [http].listen_address")
	run.Flags().BoolVar(&recovery, "recovery", false, "run without opening photo storage")
	cmd.AddCommand(run)
	return cmd
}
