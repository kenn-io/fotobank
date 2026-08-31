package checkout

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestCommitSourceStillCurrentHashesMetadataPreservingEdit(t *testing.T) {
	r := require.New(t)
	root := t.TempDir()
	path := "photo.jpg"
	original := []byte("original bytes")
	replacement := []byte("replaced bytes")
	r.Len(replacement, len(original))
	fullPath := root + string(os.PathSeparator) + path
	r.NoError(os.WriteFile(fullPath, original, 0o600))
	mtime := time.Date(2026, time.August, 31, 12, 0, 0, 0, time.UTC)
	r.NoError(os.Chtimes(fullPath, mtime, mtime))

	workingRoot, err := os.OpenRoot(root)
	r.NoError(err)
	t.Cleanup(func() { r.NoError(workingRoot.Close()) })
	info, err := workingRoot.Lstat(path)
	r.NoError(err)
	file, err := workingRoot.Open(path)
	r.NoError(err)
	t.Cleanup(func() { _ = file.Close() })
	identity, err := filesystemIdentity(file)
	r.NoError(err)
	digest := sha256.Sum256(original)
	entry := Entry{
		RelativePath: path, ObservedSize: info.Size(), ObservedMTime: info.ModTime(),
		ObservedIdentity: identity, ObservedSHA256: hex.EncodeToString(digest[:]),
	}

	r.NoError(os.WriteFile(fullPath, replacement, 0o600))
	r.NoError(os.Chtimes(fullPath, mtime, mtime))
	clean, err := commitSourceStillCurrent(t.Context(), workingRoot, file, entry)
	r.NoError(err)
	r.False(clean)
}
