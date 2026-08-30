//go:build !windows

package checkout

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

func syncCheckoutDirectories(root *os.Root, directory string) error {
	for {
		handle, err := root.Open(directory)
		if err != nil {
			return fmt.Errorf("open directory %s: %w", directory, err)
		}
		if err := errors.Join(handle.Sync(), handle.Close()); err != nil {
			return fmt.Errorf("sync directory %s: %w", directory, err)
		}
		if directory == "." {
			return nil
		}
		directory = filepath.Dir(directory)
	}
}
