package main

import (
	"os"

	"go.kenn.io/fotobank/internal/cli"
	"go.kenn.io/fotobank/internal/version"
)

// Overwritten by -ldflags "-X main.vVersion=..." in release builds.
// The Makefile injects these values; we copy them into internal/version
// so any package can read the current build metadata.
var (
	vVersion   = "dev"
	vCommit    = "unknown"
	vBuildDate = "unknown"
)

func main() {
	version.Short = vVersion
	version.Commit = vCommit
	version.BuildDate = vBuildDate
	os.Exit(cli.Run(os.Args[1:], os.Stdout, os.Stderr))
}
