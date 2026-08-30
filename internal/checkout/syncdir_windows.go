//go:build windows

package checkout

import "os"

// Windows has no supported equivalent of fsync for directory handles.
func syncCheckoutDirectories(_ *os.Root, _ string) error {
	return nil
}
