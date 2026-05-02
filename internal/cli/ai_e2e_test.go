package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/wesm/fotobank/internal/ai"
	"github.com/wesm/fotobank/internal/ai/embedding"
	"github.com/wesm/fotobank/internal/ai/failures"
	"github.com/wesm/fotobank/internal/cli"
	"github.com/wesm/fotobank/internal/db"
	"github.com/wesm/fotobank/internal/owners"
)

// writeAIEmbedConfig writes a TOML config sufficient for the embed-task
// CLI subcommands: stub identity, an [ai.embed] block enabled with a
// stable model+input_edge+dimension, and a [search] retain window. The
// vision endpoint is left unset since none of the embed CLI paths
// reach the chat client.
func writeAIEmbedConfig(t *testing.T, tmp string) string {
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
hub = "h"
user_id = "u"
storage_key = "h/u"
[ai]
enabled = false
[ai.embed]
enabled = true
model = "siglip2"
endpoint = "http://example.invalid/v1"
dimension = 8
input_edge = 384
max_retries = 1
[search]
retain_retired_days = 30
`, filepath.Join(tmp, "nas"), filepath.Join(tmp, "flash")), 0o600))
	require.NoError(t, os.MkdirAll(filepath.Join(tmp, "nas"), 0o700))
	require.NoError(t, os.MkdirAll(filepath.Join(tmp, "flash"), 0o700))
	return cfgPath
}

// runAICLI invokes the fotobank CLI in-process and returns
// stdout/stderr/exit-code. Mirrors the helper used by other cli_test
// suites.
func runAICLI(args ...string) (string, string, int) {
	var so, se bytes.Buffer
	code := cli.RunContext(context.Background(), args, &so, &se)
	return so.String(), se.String(), code
}

// embedTestFingerprint mirrors the canonical fingerprint that the CLI
// derives from cfg.AI.Embed (writeAIEmbedConfig). Tests use it to seed
// ai_failures rows that the retry-failed path will match.
func embedTestFingerprint() ai.Fingerprint {
	return ai.Fingerprint{
		ModelID:       "siglip2",
		PromptVersion: "",
		InputProfile:  "jpeg-384-q85-metadata-stripped-embed-v1",
	}
}

// seedEmbedOwnerAndPhoto opens the DB, inserts an owner and one
// thumb-ready photo, and returns the principal + media ID so embed
// tests can drive ScanEmbed end-to-end. Closes the DB before
// returning so subsequent CLI invocations can re-open it without
// fighting over the rw lock.
func seedEmbedOwnerAndPhoto(t *testing.T, dbPath string) (owners.Principal, string) {
	t.Helper()
	d, err := db.Open(dbPath)
	require.NoError(t, err)
	defer func() { _ = d.Close() }()

	p := owners.Principal{Hub: "h", UserID: "u"}
	_, err = d.WriteDB().ExecContext(context.Background(),
		`INSERT OR IGNORE INTO owners(hub, user_id, storage_key, created_at) VALUES (?,?,?,?)`,
		p.Hub, p.UserID, "h/u", time.Now().UTC())
	require.NoError(t, err)

	mid := uuid.NewString()
	_, err = d.WriteDB().ExecContext(context.Background(),
		`INSERT INTO media(id, owner_hub, owner_user_id, media_type, mime_type, path,
		 imported_at, size, checksum, thumb_status, thumb_version, import_source_path)
		 VALUES (?,?,?, 'photo','image/jpeg', ?, ?, 0, ?, 'ready', 1, ?)`,
		mid, p.Hub, p.UserID, "/photos/p1.jpg", time.Now().UTC(),
		"checksum-"+mid, "p1")
	require.NoError(t, err)
	return p, mid
}

// acknowledgeStubOwner runs `fotobank ai acknowledge --hidden-processing`
// so that subsequent ack-gated subcommands (Backfill, RetryFailed) pass
// the ack check. This matches how an operator would unblock the AI
// surface in production: ack once, then run the embed jobs.
func acknowledgeStubOwner(t *testing.T, cfgPath string) {
	t.Helper()
	stdout, stderr, code := runAICLI(
		"ai", "acknowledge", "--hidden-processing", "--config", cfgPath,
	)
	require.Equal(t, 0, code, "stdout=%s stderr=%s", stdout, stderr)
}

// listGenerationsRow mirrors the JSON shape the list-generations
// subcommand emits per row. Tests decode into this so a field
// rename in the production type is caught.
type listGenerationsRow struct {
	ID            int64      `json:"id"`
	Fingerprint   string     `json:"fingerprint"`
	ModelID       string     `json:"model_id"`
	InputProfile  string     `json:"input_profile"`
	State         string     `json:"state"`
	Dimension     int        `json:"dimension"`
	EmbeddedCount int        `json:"embedded_count"`
	EligibleCount int        `json:"eligible_count"`
	CreatedAt     time.Time  `json:"created_at"`
	ActivatedAt   *time.Time `json:"activated_at,omitempty"`
	RetiredAt     *time.Time `json:"retired_at,omitempty"`
}

func TestCLIAI_BackfillEmbed(t *testing.T) {
	r := require.New(t)
	tmp := t.TempDir()
	cfgPath := writeAIEmbedConfig(t, tmp)
	dbPath := filepath.Join(tmp, "fotobank.sqlite")
	t.Setenv("FOTOBANK_CONFIG", cfgPath)
	t.Setenv("FOTOBANK_DB_PATH", dbPath)

	seedEmbedOwnerAndPhoto(t, dbPath)
	acknowledgeStubOwner(t, cfgPath)

	stdout, stderr, code := runAICLI("ai", "backfill", "--task=embed", "--config", cfgPath)
	r.Equal(0, code, "stdout=%s stderr=%s", stdout, stderr)
	// The CLI prints "<task>: enqueued <n>" then "total: <n>"; for the
	// embed-only invocation the count should be 1 (one thumb-ready
	// photo with no mapping in the new building generation).
	r.Contains(stdout, "embed: enqueued 1")
	r.Contains(stdout, "total: 1")

	// Re-open the DB to verify the job actually landed in ai_jobs.
	d, err := db.Open(dbPath)
	r.NoError(err)
	defer func() { _ = d.Close() }()
	var n int
	r.NoError(d.ReadDB().QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM ai_jobs WHERE task='embed' AND status='pending'`,
	).Scan(&n))
	r.Equal(1, n, "one embed job must be enqueued")
}

func TestCLIAI_RetryFailedEmbed(t *testing.T) {
	r := require.New(t)
	tmp := t.TempDir()
	cfgPath := writeAIEmbedConfig(t, tmp)
	dbPath := filepath.Join(tmp, "fotobank.sqlite")
	t.Setenv("FOTOBANK_CONFIG", cfgPath)
	t.Setenv("FOTOBANK_DB_PATH", dbPath)

	_, mid := seedEmbedOwnerAndPhoto(t, dbPath)
	acknowledgeStubOwner(t, cfgPath)

	// Seed a current-fingerprint embed failure for the photo so the
	// retry-failed path has something to delete + re-enqueue.
	d, err := db.Open(dbPath)
	r.NoError(err)
	failR := failures.NewRepo(d.WriteDB(), d.ReadDB())
	r.NoError(failR.Record(context.Background(),
		mid, ai.TaskEmbed, embedTestFingerprint(),
		ai.ErrKindTransient, "transient", 1,
	))
	_ = d.Close()

	stdout, stderr, code := runAICLI("ai", "retry-failed", "--task=embed", "--config", cfgPath)
	r.Equal(0, code, "stdout=%s stderr=%s", stdout, stderr)
	r.Contains(stdout, "embed: re-enqueued 1")

	// Failure row must be gone, embed job must be enqueued.
	d, err = db.Open(dbPath)
	r.NoError(err)
	defer func() { _ = d.Close() }()
	var failN int
	r.NoError(d.ReadDB().QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM ai_failures WHERE task='embed' AND media_id=?`, mid,
	).Scan(&failN))
	r.Equal(0, failN, "embed failure row must be deleted")
	var jobN int
	r.NoError(d.ReadDB().QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM ai_jobs WHERE task='embed' AND status='pending'`,
	).Scan(&jobN))
	r.Equal(1, jobN, "embed job must be re-enqueued after retry")
}

func TestCLIAI_ListGenerations(t *testing.T) {
	r := require.New(t)
	tmp := t.TempDir()
	cfgPath := writeAIEmbedConfig(t, tmp)
	dbPath := filepath.Join(tmp, "fotobank.sqlite")
	t.Setenv("FOTOBANK_CONFIG", cfgPath)
	t.Setenv("FOTOBANK_DB_PATH", dbPath)

	// Seed an owner+photo so eligible_count is non-trivial. The
	// activator's eligibility query needs a thumb-ready row owned by
	// the configured stub principal to count anything > 0.
	seedEmbedOwnerAndPhoto(t, dbPath)

	// Seed a building generation matching the configured fingerprint
	// so list-generations has at least one row to emit.
	d, err := db.Open(dbPath)
	r.NoError(err)
	gens := embedding.NewGenerations(d.WriteDB(), d.ReadDB())
	row, err := gens.FindOrCreateBuilding(context.Background(), embedTestFingerprint(), 8)
	r.NoError(err)
	_ = d.Close()

	stdout, stderr, code := runAICLI("ai", "list-generations", "--config", cfgPath)
	r.Equal(0, code, "stdout=%s stderr=%s", stdout, stderr)

	var got []listGenerationsRow
	r.NoError(json.Unmarshal([]byte(stdout), &got))
	r.NotEmpty(got, "at least one generation must be emitted")

	var found bool
	for _, g := range got {
		if g.ID == row.ID {
			found = true
			r.Equal(embedTestFingerprint().String(), g.Fingerprint)
			r.Equal("siglip2", g.ModelID)
			r.Equal("jpeg-384-q85-metadata-stripped-embed-v1", g.InputProfile)
			r.Equal("building", g.State)
			r.Equal(8, g.Dimension)
			r.Equal(0, g.EmbeddedCount)
			// One thumb-ready photo with no ai_skipped row counts as
			// eligible under the activator's predicate.
			r.Equal(1, g.EligibleCount)
			r.False(g.CreatedAt.IsZero())
			r.Nil(g.ActivatedAt)
			r.Nil(g.RetiredAt)
		}
	}
	r.True(found, "seeded building generation must appear in output")

	// --state filter narrows by state; passing 'retired' returns no
	// rows on this fixture.
	stdout, stderr, code = runAICLI("ai", "list-generations", "--state", "retired", "--config", cfgPath)
	r.Equal(0, code, "stdout=%s stderr=%s", stdout, stderr)
	var retired []listGenerationsRow
	r.NoError(json.Unmarshal([]byte(stdout), &retired))
	r.Empty(retired)
}

func TestCLIAI_PromoteGeneration_AdminOverride(t *testing.T) {
	r := require.New(t)
	tmp := t.TempDir()
	cfgPath := writeAIEmbedConfig(t, tmp)
	dbPath := filepath.Join(tmp, "fotobank.sqlite")
	t.Setenv("FOTOBANK_CONFIG", cfgPath)
	t.Setenv("FOTOBANK_DB_PATH", dbPath)

	// Seed a building generation and retire it so promote-generation
	// has a canonical "old retired row" to bring back.
	d, err := db.Open(dbPath)
	r.NoError(err)
	gens := embedding.NewGenerations(d.WriteDB(), d.ReadDB())
	row, err := gens.FindOrCreateBuilding(context.Background(), embedTestFingerprint(), 8)
	r.NoError(err)
	r.NoError(gens.Retire(context.Background(), row.ID))
	_ = d.Close()

	stdout, stderr, code := runAICLI(
		"ai", "promote-generation", strconv.FormatInt(row.ID, 10), "--yes", "--config", cfgPath,
	)
	r.Equal(0, code, "stdout=%s stderr=%s", stdout, stderr)
	r.Contains(stdout, "promoted")

	// Re-read to assert state is now active and retired_at is cleared.
	d, err = db.Open(dbPath)
	r.NoError(err)
	defer func() { _ = d.Close() }()
	var state string
	var retiredAt *time.Time
	r.NoError(d.ReadDB().QueryRowContext(context.Background(),
		`SELECT state, retired_at FROM embedding_generations WHERE id=?`, row.ID,
	).Scan(&state, &retiredAt))
	r.Equal("active", state)
	r.Nil(retiredAt, "retired_at must be cleared on re-promotion")
}

func TestCLIAI_CompactRetiredGenerationsDryRun(t *testing.T) {
	r := require.New(t)
	tmp := t.TempDir()
	cfgPath := writeAIEmbedConfig(t, tmp)
	dbPath := filepath.Join(tmp, "fotobank.sqlite")
	t.Setenv("FOTOBANK_CONFIG", cfgPath)
	t.Setenv("FOTOBANK_DB_PATH", dbPath)

	// Seed a retired generation whose retired_at is older than the
	// configured window (30 days). Use a direct UPDATE because the
	// Retire helper stamps retired_at = now.
	d, err := db.Open(dbPath)
	r.NoError(err)
	gens := embedding.NewGenerations(d.WriteDB(), d.ReadDB())
	row, err := gens.FindOrCreateBuilding(context.Background(), embedTestFingerprint(), 8)
	r.NoError(err)
	old := time.Now().UTC().Add(-60 * 24 * time.Hour)
	_, err = d.WriteDB().ExecContext(context.Background(),
		`UPDATE embedding_generations SET state='retired', retired_at=? WHERE id=?`,
		old, row.ID)
	r.NoError(err)
	_ = d.Close()

	stdout, stderr, code := runAICLI(
		"ai", "compact-retired-generations", "--dry-run", "--config", cfgPath,
	)
	r.Equal(0, code, "stdout=%s stderr=%s", stdout, stderr)
	// Dry-run output should mention the candidate id; real compaction
	// would print "<n> retired generations dropped".
	r.Contains(stdout, strconv.FormatInt(row.ID, 10))
	r.NotContains(stdout, "dropped")

	// Verify the row is still present (dry-run did not delete).
	d, err = db.Open(dbPath)
	r.NoError(err)
	defer func() { _ = d.Close() }()
	var n int
	r.NoError(d.ReadDB().QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM embedding_generations WHERE id=?`, row.ID,
	).Scan(&n))
	r.Equal(1, n, "dry-run must NOT drop the retired generation")
}
