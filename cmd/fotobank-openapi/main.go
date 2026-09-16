// Package main implements the fotobank-openapi tool, which emits the
// OpenAPI spec for the HTTP API as YAML. With no flags it writes to
// stdout; pass -out to write directly to a file.
package main

import (
	"flag"
	"fmt"
	"go/token"
	"io"
	"os"
	"strings"

	"go.kenn.io/fotobank/internal/httpapi"
)

func main() {
	out := flag.String("out", "", "output file (default stdout)")
	flag.Parse()

	spec := httpapi.OpenAPISpec()
	// The Go client shares the named wire types registered by Huma.
	for name, schema := range spec.Components.Schemas.Map() {
		typ := spec.Components.Schemas.TypeFromRef("#/components/schemas/" + name)
		if typ == nil || !token.IsExported(typ.Name()) || typ.PkgPath() == "" {
			continue
		}
		alias := strings.ReplaceAll(strings.TrimPrefix(typ.PkgPath(), "go.kenn.io/fotobank/internal/"), "/", "_")
		if !strings.HasPrefix(typ.PkgPath(), "go.kenn.io/fotobank/internal/") {
			continue
		}
		if schema.Extensions == nil {
			schema.Extensions = map[string]any{}
		}
		schema.Extensions["x-go-type"] = alias + "." + typ.Name()
		schema.Extensions["x-go-type-import"] = map[string]string{"name": alias, "path": typ.PkgPath()}
	}

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

	data, err := spec.YAML()
	if err == nil {
		_, err = w.Write(data)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
