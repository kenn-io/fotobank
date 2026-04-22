// Package main implements the fotobank-openapi tool, which emits the
// OpenAPI spec for the HTTP API as indented JSON. With no flags it
// writes to stdout (so `make api-generate` can redirect into
// openapi.json); pass -out to write directly to a file.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/wesm/fotobank/internal/httpapi"
)

func main() {
	out := flag.String("out", "", "output file (default stdout)")
	flag.Parse()

	spec := httpapi.OpenAPISpec()

	var w io.Writer = os.Stdout
	if *out != "" {
		f, err := os.Create(*out)
		if err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
		defer f.Close()
		w = f
	}

	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(spec); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
