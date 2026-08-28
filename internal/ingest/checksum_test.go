package ingest_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"go.kenn.io/fotobank/internal/ingest"
)

func TestSHA256KnownValue(t *testing.T) {
	r := require.New(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "abc.bin")
	r.NoError(os.WriteFile(path, []byte("abc"), 0o600))

	got, err := ingest.SHA256(path)
	r.NoError(err)
	r.Equal("ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad", got)
}

func TestSHA256MissingFile(t *testing.T) {
	r := require.New(t)
	_, err := ingest.SHA256(filepath.Join(t.TempDir(), "nope"))
	r.Error(err)
}
