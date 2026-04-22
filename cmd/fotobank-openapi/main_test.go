package main_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBinaryEmitsOpenAPIWithKnownPaths(t *testing.T) {
	r := require.New(t)
	out := filepath.Join(t.TempDir(), "spec.json")
	cmd := exec.Command("go", "run", "./cmd/fotobank-openapi", "-out", out)
	cmd.Dir = repoRoot(t)
	r.NoError(cmd.Run())

	bytes, err := os.ReadFile(out)
	r.NoError(err)
	var doc map[string]any
	r.NoError(json.Unmarshal(bytes, &doc))

	paths, _ := doc["paths"].(map[string]any)
	_, hasHealthz := paths["/api/v1/healthz"]
	_, hasMe := paths["/api/v1/me"]
	r.True(hasHealthz && hasMe)
}

func repoRoot(t *testing.T) string {
	t.Helper()
	out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	require.NoError(t, err)
	return strings.TrimSpace(string(out))
}
