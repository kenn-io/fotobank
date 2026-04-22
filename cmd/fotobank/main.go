package main

import (
	"os"

	"github.com/wesm/fotobank/internal/version"
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
	// Dispatch added in Task 28; for now, exit 0.
	os.Exit(0)
}
