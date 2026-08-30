//go:build !windows

package backup

import "os"

func realSyncDir(path string) error {
	d, err := os.Open(path)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

func syncRootDir(root *os.Root, path string) error {
	d, err := root.Open(path)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
