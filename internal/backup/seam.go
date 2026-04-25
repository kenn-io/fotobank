package backup

import "os"

// syncDir is the package-level fsync seam used by Snapshot's parent-dir
// fsync and Restore's post-rename fsync. Tests swap this via setSyncDir
// to count calls or inject errors.
//
//nolint:unused // Wired up by Snapshot (T3) and Restore (T6); landed here as foundation.
var syncDir = realSyncDir

//nolint:unused // Wired up by Snapshot (T3) and Restore (T6); landed here as foundation.
func realSyncDir(path string) error {
	d, err := os.Open(path)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

// setSyncDir installs fn as the package-level dir-fsync function for
// the duration of the test, restoring the original on cleanup.
//
//nolint:unused // Used by Snapshot tests (T3) and Restore tests (T6); landed here as foundation.
func setSyncDir(t interface{ Cleanup(func()) }, fn func(string) error) {
	prev := syncDir
	syncDir = fn
	t.Cleanup(func() { syncDir = prev })
}
