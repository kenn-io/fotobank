package main_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"
)

func TestBinaryEmitsOpenAPIWithKnownPaths(t *testing.T) {
	r := require.New(t)
	out := filepath.Join(t.TempDir(), "spec.yaml")
	cmd := exec.Command("go", "run", "./cmd/fotobank-openapi", "-out", out)
	cmd.Dir = repoRoot(t)
	r.NoError(cmd.Run())

	bytes, err := os.ReadFile(out)
	r.NoError(err)
	var doc map[string]any
	r.NoError(yaml.Unmarshal(bytes, &doc))

	paths, _ := doc["paths"].(map[string]any)
	_, hasHealthz := paths["/api/v1/healthz"]
	_, hasMe := paths["/api/v1/me"]
	r.True(hasHealthz && hasMe)
}

func repoRoot(t *testing.T) string {
	t.Helper()
	localEnv, err := exec.Command("git", "rev-parse", "--local-env-vars").Output()
	require.NoError(t, err)
	localVars := strings.Fields(string(localEnv))
	cmd := exec.Command("git", "rev-parse", "--show-toplevel")
	cmd.Env = slices.DeleteFunc(os.Environ(), func(value string) bool {
		name, _, _ := strings.Cut(value, "=")
		return slices.Contains(localVars, name)
	})
	out, err := cmd.Output()
	require.NoError(t, err)
	return strings.TrimSpace(string(out))
}
