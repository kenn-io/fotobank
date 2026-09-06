package cli_test

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// writeBackupConfig creates a TOML config sufficient for backup commands.
func writeBackupConfig(t *testing.T, tmp string) string {
	t.Helper()
	cfgPath := filepath.Join(tmp, "fotobank.toml")
	require.NoError(t, os.WriteFile(cfgPath, fmt.Appendf(nil, `
[nas]
root = %q
[flash]
root = %q
[identity]
mode = "stub"
[identity.stub]
hub = "local"
user_id = "alice"
handle = "Alice"
storage_key = "550e8400-e29b-41d4-a716-44665544000e"
[http]
listen_address = "127.0.0.1:0"
[imports]
file_lock_path = %q
[observability]
admin_listen = "127.0.0.1:0"
`, filepath.Join(tmp, "nas"), filepath.Join(tmp, "flash"),
		filepath.Join(tmp, "import.lock")), 0o600))
	require.NoError(t, os.MkdirAll(filepath.Join(tmp, "nas"), 0o700))
	require.NoError(t, os.MkdirAll(filepath.Join(tmp, "flash"), 0o700))
	return cfgPath
}
