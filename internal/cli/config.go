package cli

import (
	"fmt"
	"reflect"
	"strings"

	"github.com/spf13/cobra"

	"github.com/wesm/fotobank/internal/config"
)

func newConfigCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Inspect fotobank configuration",
	}
	cmd.AddCommand(newConfigPathCmd())
	cmd.AddCommand(newConfigReadCmd())
	cmd.AddCommand(newConfigValidateCmd())
	return cmd
}

func newConfigPathCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "path",
		Short: "Print the resolved config file path",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			fmt.Fprintln(cmd.OutOrStdout(), config.DefaultConfigPath())
			return nil
		},
	}
}

func newConfigValidateCmd() *cobra.Command {
	var cfgPath string
	cmd := &cobra.Command{
		Use:   "validate",
		Short: "Load the config and report any validation errors",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			path := cfgPath
			if path == "" {
				path = config.DefaultConfigPath()
			}
			if _, err := config.Load(path); err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "ok")
			return nil
		},
	}
	cmd.Flags().StringVar(&cfgPath, "config", "", "path to config file (defaults to DefaultConfigPath)")
	return cmd
}

func newConfigReadCmd() *cobra.Command {
	var cfgPath string
	cmd := &cobra.Command{
		Use:   "read <dotted.key>",
		Short: "Print the scalar value at a dotted TOML key",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path := cfgPath
			if path == "" {
				path = config.DefaultConfigPath()
			}
			cfg, err := config.Load(path)
			if err != nil {
				return err
			}
			v, err := traverse(cfg, args[0])
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), v)
			return nil
		},
	}
	cmd.Flags().StringVar(&cfgPath, "config", "", "path to config file (defaults to DefaultConfigPath)")
	return cmd
}

// traverse resolves a dotted TOML key against the config struct using
// toml tags. Supports scalars only; nested tables only as path segments.
func traverse(cfg *config.Config, key string) (string, error) {
	parts := strings.Split(key, ".")
	v := reflect.ValueOf(cfg).Elem()
	for _, p := range parts {
		if v.Kind() != reflect.Struct {
			return "", fmt.Errorf("unknown config key %q", key)
		}
		t := v.Type()
		found := false
		for i := range t.NumField() {
			tag := strings.Split(t.Field(i).Tag.Get("toml"), ",")[0]
			if tag == p {
				v = v.Field(i)
				found = true
				break
			}
		}
		if !found {
			return "", fmt.Errorf("unknown config key %q", key)
		}
	}
	switch v.Kind() {
	case reflect.String, reflect.Bool, reflect.Int, reflect.Int64, reflect.Float64:
		return fmt.Sprintf("%v", v.Interface()), nil
	default:
		return "", fmt.Errorf("config key %q is not a scalar", key)
	}
}
