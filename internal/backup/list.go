package backup

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// stampLayout is the on-disk filename layout: RFC3339 with millisecond
// precision and a literal "Z" suffix. UTC.
const stampLayout = "2006-01-02T15:04:05.000Z"

// snapshotExt is the suffix every snapshot filename carries.
const snapshotExt = ".sqlite"

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
// created yet).
func List(dir string) ([]SnapshotInfo, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("readdir %s: %w", dir, err)
	}
	var out []SnapshotInfo
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, snapshotExt) {
			continue
		}
		stamp := strings.TrimSuffix(name, snapshotExt)
		ts, err := time.Parse(stampLayout, stamp)
		if err != nil {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, SnapshotInfo{
			Path:      filepath.Join(dir, name),
			Timestamp: ts,
			Size:      info.Size(),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Timestamp.After(out[j].Timestamp)
	})
	return out, nil
}
