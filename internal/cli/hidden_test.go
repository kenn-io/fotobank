package cli_test

import (
	"bytes"
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"go.kenn.io/fotobank/internal/auth/hidden"
	"go.kenn.io/fotobank/internal/cli"
	"go.kenn.io/fotobank/internal/owners"
	"go.kenn.io/fotobank/internal/testutil"
)

// runHiddenCLI runs the CLI with background context, no stdin, returns
// (stdout, stderr, exitCode).
func runHiddenCLI(args ...string) (string, string, int) {
	var stdout, stderr bytes.Buffer
	code := cli.RunContext(context.Background(), args, &stdout, &stderr)
	return stdout.String(), stderr.String(), code
}

// runHiddenCLIIn runs the CLI with the given string piped to stdin.
func runHiddenCLIIn(input string, args ...string) (string, string, int) {
	var stdout, stderr bytes.Buffer
	code := cli.RunWithInput(context.Background(), args, strings.NewReader(input), &stdout, &stderr)
	return stdout.String(), stderr.String(), code
}

// bootstrapHiddenOwner seeds the stub owner row (hub=h, user_id=u) so
// hidden commands that load the DB don't fail with a missing owner.
// Callers must have already set FOTOBANK_CONFIG and FOTOBANK_DB_PATH.
func bootstrapHiddenOwner(t *testing.T) {
	t.Helper()
	_, stderr, code := runHiddenCLI(
		"owners", "add",
		"--hub", "h", "--user-id", "u", "--storage-key", "550e8400-e29b-41d4-a716-446655440000",
	)
	require.Equal(t, 0, code, "bootstrap owner must succeed: %s", stderr)
}

// seedCredentialInDB inserts a raw argon2id credential directly for the
// given principal so tests that start from "already configured" don't
// need to invoke the CLI setup path.
func seedCredentialInDB(t *testing.T, dbPath, hub, userID string) {
	t.Helper()
	d := testutil.OpenTestDBAt(t, dbPath)
	defer func() { _ = d.Close() }()
	repo := hidden.NewRepo(d.WriteDB(), d.ReadDB())
	hash, err := hidden.HashPasscode("mypasscode")
	require.NoError(t, err)
	p := owners.Principal{Hub: hub, UserID: userID}
	require.NoError(t, repo.InsertCredential(context.Background(), p, hash, time.Now().UTC()))
}

// seedOwnerDirectly inserts an owner row directly into the DB.
func seedOwnerDirectly(t *testing.T, dbPath, hub, userID string) {
	t.Helper()
	d := testutil.OpenTestDBAt(t, dbPath)
	defer func() { _ = d.Close() }()
	_, err := d.WriteDB().ExecContext(context.Background(),
		`INSERT OR IGNORE INTO owners(hub, user_id, storage_key, created_at) VALUES(?,?,?,?)`,
		hub, userID, "550e8400-e29b-41d4-a716-446655440000", time.Now().UTC(),
	)
	require.NoError(t, err)
}

// seedHiddenMedia inserts a media row with hidden_at set; returns the media ID.
func seedHiddenMedia(t *testing.T, dbPath, hub, userID string) string {
	t.Helper()
	d := testutil.OpenTestDBAt(t, dbPath)
	defer func() { _ = d.Close() }()
	id := uuid.NewString()
	now := time.Now().UTC()
	_, err := d.WriteDB().ExecContext(context.Background(),
		`INSERT INTO media (id, owner_hub, owner_user_id, media_type, mime_type, path,
		   imported_at, size, checksum, thumb_status, hidden_at)
		 VALUES (?, ?, ?, 'photo', 'image/jpeg', ?, ?, 1, ?, 'pending', ?)`,
		id, hub, userID, "test/"+id+".jpg", now, "cksum-"+id, now,
	)
	require.NoError(t, err)
	return id
}

// hiddenAtIsSet returns true if media.hidden_at is non-null for mediaID.
func hiddenAtIsSet(t *testing.T, dbPath, mediaID string) bool {
	t.Helper()
	d := testutil.OpenTestDBAt(t, dbPath)
	defer func() { _ = d.Close() }()
	var hiddenAt sql.NullTime
	err := d.ReadDB().QueryRowContext(context.Background(),
		`SELECT hidden_at FROM media WHERE id = ?`, mediaID,
	).Scan(&hiddenAt)
	require.NoError(t, err)
	return hiddenAt.Valid
}

// === hidden setup ===

func TestHiddenSetupRefusesNonStubMode(t *testing.T) {
	tmp := t.TempDir()
	cfgPath := writeNonStubConfig(t, tmp)
	dbPath := filepath.Join(tmp, "fotobank.sqlite")
	t.Setenv("FOTOBANK_DB_PATH", dbPath)

	_, stderr, code := runHiddenCLIIn("mypasscode\nmypasscode\n",
		"hidden", "setup", "--config", cfgPath)
	require.Equal(t, 1, code)
	require.Contains(t, stderr, "stub identity mode")
}

func TestHiddenSetupMismatchedConfirmationFails(t *testing.T) {
	tmp := t.TempDir()
	cfgPath := writeBasicConfig(t, tmp)
	dbPath := filepath.Join(tmp, "fotobank.sqlite")
	t.Setenv("FOTOBANK_DB_PATH", dbPath)
	t.Setenv("FOTOBANK_CONFIG", cfgPath)
	bootstrapHiddenOwner(t)

	_, stderr, code := runHiddenCLIIn("mypasscode\ndifferent\n",
		"hidden", "setup", "--config", cfgPath)
	require.Equal(t, 1, code)
	require.Contains(t, stderr, "passcodes do not match")
}

func TestHiddenSetupStoresCredential(t *testing.T) {
	tmp := t.TempDir()
	cfgPath := writeBasicConfig(t, tmp)
	dbPath := filepath.Join(tmp, "fotobank.sqlite")
	t.Setenv("FOTOBANK_DB_PATH", dbPath)
	t.Setenv("FOTOBANK_CONFIG", cfgPath)
	bootstrapHiddenOwner(t)

	stdout, stderr, code := runHiddenCLIIn("mypasscode\nmypasscode\n",
		"hidden", "setup", "--config", cfgPath)
	require.Equal(t, 0, code, "stderr=%s stdout=%s", stderr, stdout)
	require.Contains(t, stdout, "passcode set")
}

func TestHiddenSetupDuplicateReturnsError(t *testing.T) {
	tmp := t.TempDir()
	cfgPath := writeBasicConfig(t, tmp)
	dbPath := filepath.Join(tmp, "fotobank.sqlite")
	t.Setenv("FOTOBANK_DB_PATH", dbPath)
	t.Setenv("FOTOBANK_CONFIG", cfgPath)
	bootstrapHiddenOwner(t)

	// First setup — must succeed.
	_, stderr, code := runHiddenCLIIn("mypasscode\nmypasscode\n",
		"hidden", "setup", "--config", cfgPath)
	require.Equal(t, 0, code, "first setup stderr=%s", stderr)

	// Second setup — must fail (already configured).
	_, stderr, code = runHiddenCLIIn("mypasscode\nmypasscode\n",
		"hidden", "setup", "--config", cfgPath)
	require.Equal(t, 1, code)
	require.NotEmpty(t, stderr)
}

// === hidden change ===

func TestHiddenChangeRefusesNonStubMode(t *testing.T) {
	tmp := t.TempDir()
	cfgPath := writeNonStubConfig(t, tmp)
	dbPath := filepath.Join(tmp, "fotobank.sqlite")
	t.Setenv("FOTOBANK_DB_PATH", dbPath)

	_, stderr, code := runHiddenCLIIn("old\nnew\nnew\n",
		"hidden", "change", "--config", cfgPath)
	require.Equal(t, 1, code)
	require.Contains(t, stderr, "stub identity mode")
}

func TestHiddenChangeVerifiesCurrentAndUpdates(t *testing.T) {
	tmp := t.TempDir()
	cfgPath := writeBasicConfig(t, tmp)
	dbPath := filepath.Join(tmp, "fotobank.sqlite")
	t.Setenv("FOTOBANK_DB_PATH", dbPath)
	t.Setenv("FOTOBANK_CONFIG", cfgPath)
	bootstrapHiddenOwner(t)

	// Setup first.
	_, stderr, code := runHiddenCLIIn("mypasscode\nmypasscode\n",
		"hidden", "setup", "--config", cfgPath)
	require.Equal(t, 0, code, "setup stderr=%s", stderr)

	// Change with correct current passcode.
	stdout, stderr, code := runHiddenCLIIn("mypasscode\nnewpasscode\nnewpasscode\n",
		"hidden", "change", "--config", cfgPath)
	require.Equal(t, 0, code, "stderr=%s stdout=%s", stderr, stdout)
	require.Contains(t, stdout, "passcode changed")
}

func TestHiddenChangeMismatchedNewPasscodeFails(t *testing.T) {
	tmp := t.TempDir()
	cfgPath := writeBasicConfig(t, tmp)
	dbPath := filepath.Join(tmp, "fotobank.sqlite")
	t.Setenv("FOTOBANK_DB_PATH", dbPath)
	t.Setenv("FOTOBANK_CONFIG", cfgPath)
	bootstrapHiddenOwner(t)

	_, stderr, code := runHiddenCLIIn("mypasscode\nmypasscode\n",
		"hidden", "setup", "--config", cfgPath)
	require.Equal(t, 0, code, "setup stderr=%s", stderr)

	_, stderr, code = runHiddenCLIIn("mypasscode\nnewpasscode\ndifferent\n",
		"hidden", "change", "--config", cfgPath)
	require.Equal(t, 1, code)
	require.Contains(t, stderr, "passcodes do not match")
}

func TestHiddenChangeWrongCurrentPasscodeFails(t *testing.T) {
	tmp := t.TempDir()
	cfgPath := writeBasicConfig(t, tmp)
	dbPath := filepath.Join(tmp, "fotobank.sqlite")
	t.Setenv("FOTOBANK_DB_PATH", dbPath)
	t.Setenv("FOTOBANK_CONFIG", cfgPath)
	bootstrapHiddenOwner(t)

	_, stderr, code := runHiddenCLIIn("mypasscode\nmypasscode\n",
		"hidden", "setup", "--config", cfgPath)
	require.Equal(t, 0, code, "setup stderr=%s", stderr)

	_, stderr, code = runHiddenCLIIn("wrongpasscode\nnewpasscode\nnewpasscode\n",
		"hidden", "change", "--config", cfgPath)
	require.Equal(t, 1, code)
	require.NotEmpty(t, stderr)
}

// === hidden disable ===

func TestHiddenDisableRefusesNonStubMode(t *testing.T) {
	tmp := t.TempDir()
	cfgPath := writeNonStubConfig(t, tmp)
	dbPath := filepath.Join(tmp, "fotobank.sqlite")
	t.Setenv("FOTOBANK_DB_PATH", dbPath)

	_, stderr, code := runHiddenCLIIn("mypasscode\nyes\n",
		"hidden", "disable", "--config", cfgPath)
	require.Equal(t, 1, code)
	require.Contains(t, stderr, "stub identity mode")
}

func TestHiddenDisableRequiresConfirmation(t *testing.T) {
	tmp := t.TempDir()
	cfgPath := writeBasicConfig(t, tmp)
	dbPath := filepath.Join(tmp, "fotobank.sqlite")
	t.Setenv("FOTOBANK_DB_PATH", dbPath)
	t.Setenv("FOTOBANK_CONFIG", cfgPath)
	bootstrapHiddenOwner(t)

	_, stderr, code := runHiddenCLIIn("mypasscode\nmypasscode\n",
		"hidden", "setup", "--config", cfgPath)
	require.Equal(t, 0, code, "setup stderr=%s", stderr)

	// Supply wrong confirmation.
	_, stderr, code = runHiddenCLIIn("mypasscode\nno\n",
		"hidden", "disable", "--config", cfgPath)
	require.Equal(t, 1, code)
	require.Contains(t, stderr, "aborted")
}

func TestHiddenDisableSuccess(t *testing.T) {
	tmp := t.TempDir()
	cfgPath := writeBasicConfig(t, tmp)
	dbPath := filepath.Join(tmp, "fotobank.sqlite")
	t.Setenv("FOTOBANK_DB_PATH", dbPath)
	t.Setenv("FOTOBANK_CONFIG", cfgPath)
	bootstrapHiddenOwner(t)

	_, stderr, code := runHiddenCLIIn("mypasscode\nmypasscode\n",
		"hidden", "setup", "--config", cfgPath)
	require.Equal(t, 0, code, "setup stderr=%s", stderr)

	stdout, stderr, code := runHiddenCLIIn("mypasscode\nyes\n",
		"hidden", "disable", "--config", cfgPath)
	require.Equal(t, 0, code, "stderr=%s stdout=%s", stderr, stdout)
	require.Contains(t, stdout, "disabled")
}

// === admin reset-hidden-passcode ===

func TestAdminResetHiddenPasscodeRequiresConfirmFlag(t *testing.T) {
	tmp := t.TempDir()
	cfgPath := writeBasicConfig(t, tmp)
	dbPath := filepath.Join(tmp, "fotobank.sqlite")
	t.Setenv("FOTOBANK_DB_PATH", dbPath)

	_, _, code := runHiddenCLI("admin", "reset-hidden-passcode", "--config", cfgPath)
	require.Equal(t, 2, code)
}

func TestAdminResetHiddenPasscodeStubModeDefaultsToStubPrincipal(t *testing.T) {
	tmp := t.TempDir()
	cfgPath := writeBasicConfig(t, tmp)
	dbPath := filepath.Join(tmp, "fotobank.sqlite")
	t.Setenv("FOTOBANK_DB_PATH", dbPath)
	t.Setenv("FOTOBANK_CONFIG", cfgPath)
	bootstrapHiddenOwner(t)
	seedCredentialInDB(t, dbPath, "h", "u")

	stdout, stderr, code := runHiddenCLI(
		"admin", "reset-hidden-passcode", "--confirm", "--config", cfgPath)
	require.Equal(t, 0, code, "stderr=%s stdout=%s", stderr, stdout)
	require.Contains(t, stdout, "reset")
}

func TestAdminResetHiddenPasscodeNonStubRequiresOwner(t *testing.T) {
	tmp := t.TempDir()
	cfgPath := writeNonStubConfig(t, tmp)
	dbPath := filepath.Join(tmp, "fotobank.sqlite")
	t.Setenv("FOTOBANK_DB_PATH", dbPath)

	_, stderr, code := runHiddenCLI(
		"admin", "reset-hidden-passcode", "--confirm", "--config", cfgPath)
	require.Equal(t, 1, code)
	require.Contains(t, stderr, "--owner")
}

func TestAdminResetHiddenPasscodeNonStubWithOwner(t *testing.T) {
	tmp := t.TempDir()
	cfgPath := writeNonStubConfig(t, tmp)
	dbPath := filepath.Join(tmp, "fotobank.sqlite")
	t.Setenv("FOTOBANK_DB_PATH", dbPath)

	// Seed owner and credential directly.
	seedOwnerDirectly(t, dbPath, "h", "u")
	seedCredentialInDB(t, dbPath, "h", "u")

	stdout, stderr, code := runHiddenCLI(
		"admin", "reset-hidden-passcode",
		"--confirm", "--owner", "h:u", "--config", cfgPath)
	require.Equal(t, 0, code, "stderr=%s stdout=%s", stderr, stdout)
	require.Contains(t, stdout, "reset")
}

// TestAdminResetHiddenPasscodeConfirmFalseIsRejected verifies that
// --confirm=false does NOT perform the reset even though the flag is provided.
// The previous implementation used _ bool for the confirm parameter, so any
// boolean value (including false) would proceed to AdminReset.
func TestAdminResetHiddenPasscodeConfirmFalseIsRejected(t *testing.T) {
	tmp := t.TempDir()
	cfgPath := writeBasicConfig(t, tmp)
	dbPath := filepath.Join(tmp, "fotobank.sqlite")
	t.Setenv("FOTOBANK_DB_PATH", dbPath)
	t.Setenv("FOTOBANK_CONFIG", cfgPath)
	bootstrapHiddenOwner(t)
	seedCredentialInDB(t, dbPath, "h", "u")

	// --confirm=false must be rejected with a usage error (exit code 2).
	_, stderr, code := runHiddenCLI(
		"admin", "reset-hidden-passcode", "--confirm=false", "--config", cfgPath)
	require.Equal(t, 2, code, "must exit 2 on --confirm=false: stderr=%s", stderr)
}

func TestAdminResetHiddenPasscodePreservesHiddenAt(t *testing.T) {
	tmp := t.TempDir()
	cfgPath := writeBasicConfig(t, tmp)
	dbPath := filepath.Join(tmp, "fotobank.sqlite")
	t.Setenv("FOTOBANK_DB_PATH", dbPath)
	t.Setenv("FOTOBANK_CONFIG", cfgPath)
	bootstrapHiddenOwner(t)

	// Seed a media row with hidden_at set.
	mID := seedHiddenMedia(t, dbPath, "h", "u")

	// Setup passcode.
	_, stderr, code := runHiddenCLIIn("mypasscode\nmypasscode\n",
		"hidden", "setup", "--config", cfgPath)
	require.Equal(t, 0, code, "setup stderr=%s", stderr)

	// Admin reset.
	_, stderr, code = runHiddenCLI(
		"admin", "reset-hidden-passcode", "--confirm", "--config", cfgPath)
	require.Equal(t, 0, code, "stderr=%s", stderr)

	// hidden_at on the media row must still be set.
	require.True(t, hiddenAtIsSet(t, dbPath, mID), "hidden_at must be preserved after admin reset")
}
