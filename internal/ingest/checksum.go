package ingest

import (
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"io"
	"os"
)

// Checksum returns the hex-encoded MD5 of the file at path. Deliberately
// MD5 (not SHA-256) — master spec's dedup key is MD5, matching the
// Python tool's legacy archive keys.
func Checksum(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := md5.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", fmt.Errorf("md5: %w", err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
