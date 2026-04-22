// Package cli implements the top-level fotobank command dispatcher.
// Run routes the first positional argument to a subcommand handler
// (server, owners, config) and provides built-in help and version output.
package cli

import (
	"context"
	"fmt"
	"io"

	"github.com/wesm/fotobank/internal/version"
)

// Run dispatches a fotobank CLI invocation. args is expected to be os.Args[1:].
// Output that the user requested (help, version) is written to stdout; error
// output (usage on bad input) is written to stderr. The returned integer is
// the process exit code: 0 on success, 2 on usage errors.
//
// Run is a thin wrapper around RunContext with a background context. Call
// RunContext directly when a long-lived subcommand (for example server)
// needs to observe cancellation from the caller.
func Run(args []string, stdout, stderr io.Writer) int {
	return RunContext(context.Background(), args, stdout, stderr)
}

// RunContext dispatches a fotobank CLI invocation, threading ctx through to
// long-lived subcommands. Subcommands that complete synchronously (owners,
// config, version, help) ignore ctx; the server subcommand observes it to
// trigger graceful shutdown when the caller cancels.
func RunContext(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		printUsage(stderr)
		return 2
	}
	switch args[0] {
	case "server":
		return runServer(ctx, args[1:], stdout, stderr)
	case "owners":
		return runOwners(args[1:], stdout, stderr)
	case "config":
		return runConfigCmd(args[1:], stdout, stderr)
	case "version":
		fmt.Fprintln(stdout, version.Format())
		return 0
	case "help", "-h", "--help":
		printUsage(stdout)
		return 0
	default:
		printUsage(stderr)
		return 2
	}
}

func printUsage(w io.Writer) {
	fmt.Fprint(w, `usage: fotobank <command> [flags]

commands:
  server        Start the HTTP server
  owners        Manage owners (add/list/remove)
  config        Inspect configuration (path/read/validate)
  version       Print version
  help          Show this help
`)
}
