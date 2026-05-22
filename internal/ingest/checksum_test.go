package ingest_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"go.kenn.io/fotobank/internal/ingest"
)

func TestChecksumKnownValue(t *testing.T) {
	r := require.New(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "abc.bin")
	r.NoError(os.WriteFile(path, []byte("abc"), 0o600))

	got, err := ingest.Checksum(path)
	r.NoError(err)
	r.Equal("900150983cd24fb0d6963f7d28e17f72", got)
}

func TestChecksumMissingFile(t *testing.T) {
	r := require.New(t)
	_, err := ingest.Checksum(filepath.Join(t.TempDir(), "nope"))
	r.Error(err)
}
