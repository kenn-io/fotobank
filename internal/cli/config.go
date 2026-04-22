package cli

import (
	"flag"
	"fmt"
	"io"
	"reflect"
	"strings"

	"github.com/wesm/fotobank/internal/config"
)

func runConfigCmd(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: fotobank config <path|read|validate>")
		return 2
	}
	switch args[0] {
	case "path":
		return runConfigPath(args[1:], stdout, stderr)
	case "read":
		return runConfigRead(args[1:], stdout, stderr)
	case "validate":
		return runConfigValidate(args[1:], stdout, stderr)
	default:
		fmt.Fprintln(stderr, "usage: fotobank config <path|read|validate>")
		return 2
	}
}

func runConfigPath(_ []string, stdout, _ io.Writer) int {
	fmt.Fprintln(stdout, config.DefaultConfigPath())
	return 0
}

func runConfigValidate(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("config validate", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	cfgPath := fs.String("config", config.DefaultConfigPath(), "path to config file")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if _, err := config.Load(*cfgPath); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fmt.Fprintln(stdout, "ok")
	return 0
}

func runConfigRead(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("config read", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	cfgPath := fs.String("config", config.DefaultConfigPath(), "path to config file")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(stderr, "config read requires one key (e.g. nas.root)")
		return 2
	}
	cfg, err := config.Load(*cfgPath)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	v, err := traverse(cfg, fs.Arg(0))
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fmt.Fprintln(stdout, v)
	return 0
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
