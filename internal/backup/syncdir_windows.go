//go:build windows

package backup

import "os"

// Windows has no supported equivalent of fsync for directory handles:
// FlushFileBuffers rejects them even when opened with backup semantics.
// Snapshot and restore bytes are flushed before publication; successful
// filesystem namespace operations are the available Windows boundary.
func realSyncDir(string) error {
	return nil
}

func syncRootDir(*os.Root, string) error {
	return nil
}
