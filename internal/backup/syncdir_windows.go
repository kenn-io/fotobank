//go:build windows

package backup

// Windows has no supported equivalent of fsync for directory handles:
// FlushFileBuffers rejects them even when opened with backup semantics.
// Snapshot and restore bytes are flushed before publication; successful
// filesystem namespace operations are the available Windows boundary.
func realSyncDir(string) error {
	return nil
}
