// Package version exposes build-time metadata injected via -ldflags.
// The CLI's main.go copies ldflag-injected values into these vars before
// dispatching, so any package can read the current version without
// depending on main.
package version

import "fmt"

var (
	Short     = "dev"
	Commit    = "unknown"
	BuildDate = "unknown"
)

// Format renders a single-line version string suitable for `fotobank version`.
func Format() string {
	return fmt.Sprintf("fotobank %s (%s) built %s", Short, Commit, BuildDate)
}
