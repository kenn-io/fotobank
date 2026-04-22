package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"

	"github.com/wesm/fotobank/internal/cli/clictx"
	"github.com/wesm/fotobank/internal/owners"
)

// runOwners dispatches `fotobank owners <subcommand>` to the appropriate
// handler. Returns exit code 2 on usage errors, 1 on runtime errors, and
// 0 on success.
func runOwners(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: fotobank owners <add|list|remove>")
		return 2
	}
	switch args[0] {
	case "add":
		return runOwnersAdd(args[1:], stdout, stderr)
	case "list":
		return runOwnersList(args[1:], stdout, stderr)
	case "remove":
		return runOwnersRemove(args[1:], stdout, stderr)
	default:
		fmt.Fprintln(stderr, "usage: fotobank owners <add|list|remove>")
		return 2
	}
}

func runOwnersAdd(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("owners add", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	hub := fs.String("hub", "", "")
	uid := fs.String("user-id", "", "")
	key := fs.String("storage-key", "", "")
	handle := fs.String("handle", "", "")
	if err := fs.Parse(args); err != nil || *hub == "" || *uid == "" || *key == "" {
		fmt.Fprintln(stderr, "usage: fotobank owners add --hub H --user-id U --storage-key K [--handle H]")
		return 2
	}
	svc, cleanup, err := clictx.LoadOwnerService()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer cleanup()
	p := owners.Principal{Hub: *hub, UserID: *uid}
	if err := svc.Ensure(context.Background(), p, *key); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if *handle != "" {
		if err := svc.UpdateDisplay(context.Background(), p, *handle); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
	}
	fmt.Fprintln(stdout, "added", p)
	return 0
}

func runOwnersList(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("owners list", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	jsonOut := fs.Bool("json", false, "")
	_ = fs.Parse(args)

	svc, cleanup, err := clictx.LoadOwnerService()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer cleanup()
	rows, err := svc.List(context.Background())
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if *jsonOut {
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
		return encodeJSON(stdout, stderr, out)
	}
	for _, o := range rows {
		fmt.Fprintf(stdout, "%s\t%s\t%s\n", o.Principal, o.StorageKey, o.DisplayHandle)
	}
	return 0
}

func runOwnersRemove(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("owners remove", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	hub := fs.String("hub", "", "")
	uid := fs.String("user-id", "", "")
	purge := fs.Bool("purge", false, "also remove media rows (Plan B+)")
	if err := fs.Parse(args); err != nil || *hub == "" || *uid == "" {
		fmt.Fprintln(stderr, "usage: fotobank owners remove --hub H --user-id U [--purge]")
		return 2
	}
	svc, cleanup, err := clictx.LoadOwnerService()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer cleanup()
	if err := svc.Remove(context.Background(), owners.Principal{Hub: *hub, UserID: *uid}, *purge); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fmt.Fprintln(stdout, "removed")
	return 0
}

func encodeJSON(stdout, stderr io.Writer, v any) int {
	enc := json.NewEncoder(stdout)
	if err := enc.Encode(v); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}
