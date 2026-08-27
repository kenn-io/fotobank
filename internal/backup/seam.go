package backup

// syncDir is the package-level fsync seam used by Snapshot's parent-dir
// fsync and Restore's post-rename fsync. Tests swap this via setSyncDir
// to count calls or inject errors.
var syncDir = realSyncDir

// setSyncDir installs fn as the package-level dir-fsync function for
// the duration of the test, restoring the original on cleanup.
func setSyncDir(t interface{ Cleanup(func()) }, fn func(string) error) {
	prev := syncDir
	syncDir = fn
	t.Cleanup(func() { syncDir = prev })
}
