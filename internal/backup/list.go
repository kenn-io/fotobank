package backup

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// StampLayout is the portable on-disk filename layout with nanosecond
// precision and a literal "Z" suffix. UTC. Exported so the worker
// (this package) and the CLI (internal/cli) write filenames List can
// parse — the layout is the contract between writers and the reader.
// Nanosecond precision (vs. millisecond) prevents filename collision
// between back-to-back snapshots taken within the same millisecond.
const StampLayout = "20060102T150405.000000000Z"

// SnapshotExt is the suffix every snapshot filename carries.
const SnapshotExt = ".sqlite"

// SnapshotInfo describes a single retained snapshot on disk. It carries
// the absolute path, the timestamp parsed from the filename, and the
// file size in bytes.
type SnapshotInfo struct {
	Path      string
	Timestamp time.Time
	Size      int64
}

// List enumerates valid snapshot files in dir, newest-first. Files
// whose names do not match the timestamp layout are skipped silently;
// .partial files are skipped. A missing directory returns an empty
// slice, not an error (callers may have a dir that the worker has not
// created yet). Returned SnapshotInfo.Path values are absolute even
// when dir is relative — callers (CLI, sweep, restore) treat them as
// stable identifiers and may pass them across cwd-changing boundaries.
func List(dir string) ([]SnapshotInfo, error) {
	absDir, err := filepath.Abs(dir)
	if err != nil {
		absDir = dir
	}
	entries, err := os.ReadDir(absDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("readdir %s: %w", absDir, err)
	}
	return listEntries(entries, absDir), nil
}

// ListRoot enumerates snapshots beneath an opened filesystem root. Returned
// paths use the name through which root was opened, while enumeration remains
// bound to the opened directory if that pathname changes afterward.
func ListRoot(root *os.Root, relativeDir string) ([]SnapshotInfo, error) {
	dir, err := root.Open(relativeDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("readdir rooted snapshots: %w", err)
	}
	defer dir.Close()
	entries, err := dir.ReadDir(-1)
	if err != nil {
		return nil, fmt.Errorf("readdir rooted snapshots: %w", err)
	}
	return listEntries(entries, filepath.Join(root.Name(), relativeDir)), nil
}

func listEntries(entries []fs.DirEntry, displayDir string) []SnapshotInfo {
	var out []SnapshotInfo
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, SnapshotExt) {
			continue
		}
		stamp := strings.TrimSuffix(name, SnapshotExt)
		ts, err := time.Parse(StampLayout, stamp)
		if err != nil {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, SnapshotInfo{
			Path:      filepath.Join(displayDir, name),
			Timestamp: ts,
			Size:      info.Size(),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Timestamp.After(out[j].Timestamp)
	})
	return out
}
