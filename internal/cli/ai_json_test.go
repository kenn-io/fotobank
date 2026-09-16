package cli_test

import (
	"bytes"
	json "encoding/json/v2"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"go.kenn.io/fotobank/internal/ai"
	"go.kenn.io/fotobank/internal/ai/embedding"
	"go.kenn.io/fotobank/internal/ai/failures"
	"go.kenn.io/fotobank/internal/cli"
	"go.kenn.io/fotobank/internal/db"
	"go.kenn.io/fotobank/internal/httpapi"
)

func TestCLIAI_QueueJSON(t *testing.T) {
	for _, command := range []string{"backfill", "retry-failed"} {
		t.Run(command, func(t *testing.T) {
			r := require.New(t)
			tmp := t.TempDir()
			cfg := writeAIEmbedConfig(t, tmp)
			dbPath := filepath.Join(tmp, "catalog.sqlite")
			t.Setenv("FOTOBANK_DB_PATH", dbPath)
			_, mid := seedEmbedOwnerAndPhoto(t, dbPath)
			acknowledgeStubOwner(t, dbPath)
			if command == "retry-failed" {
				d, err := db.Open(dbPath)
				r.NoError(err)
				r.NoError(failures.NewRepo(d.WriteDB(), d.ReadDB()).Record(t.Context(), mid,
					ai.TaskEmbed, embedTestFingerprint(), ai.ErrKindTransient, "transient", 1))
				r.NoError(d.Close())
			}
			startCheckoutServer(t, cfg, dbPath)

			out, stderr, code := runAICLI("ai", command, "--task=embed,tag", "--config", cfg, "--json")
			r.Zero(code, stderr)
			var result map[string]httpapi.AIEnqueuedResult
			r.NoError(json.Unmarshal([]byte(out), &result))
			tagCount := 1
			if command == "retry-failed" {
				tagCount = 0
			}
			r.Equal(map[string]httpapi.AIEnqueuedResult{
				"embed": {Enqueued: 1}, "tag": {Enqueued: tagCount},
			}, result)
		})
	}
}

func TestCLIAI_QueueJSONPartialFailure(t *testing.T) {
	for _, command := range []string{"backfill", "retry-failed"} {
		t.Run(command, func(t *testing.T) {
			r := require.New(t)
			tmp := t.TempDir()
			cfg := writeAIEmbedConfig(t, tmp)
			contents, err := os.ReadFile(cfg)
			r.NoError(err)
			r.NoError(os.WriteFile(cfg, bytes.ReplaceAll(contents, []byte("enabled = true"), []byte("enabled = false")), 0o600))
			dbPath := filepath.Join(tmp, "catalog.sqlite")
			t.Setenv("FOTOBANK_DB_PATH", dbPath)
			seedEmbedOwnerAndPhoto(t, dbPath)
			acknowledgeStubOwner(t, dbPath)
			startCheckoutServer(t, cfg, dbPath)

			// Tag completes, disabled embedding fails, and caption is never attempted.
			out, stderr, code := runAICLI("ai", command, "--task=tag,embed,caption", "--config", cfg, "--json")
			r.Equal(1, code, stderr)
			r.Contains(stderr, "embed")
			var result map[string]httpapi.AIEnqueuedResult
			r.NoError(json.Unmarshal([]byte(out), &result))
			tagCount := 1
			if command == "retry-failed" {
				tagCount = 0
			}
			r.Equal(map[string]httpapi.AIEnqueuedResult{"tag": {Enqueued: tagCount}}, result)

			out, stderr, code = runAICLI("ai", command, "--task=embed", "--config", cfg, "--json")
			r.Equal(1, code, stderr)
			r.Empty(out, "no receipt when no task completed")
		})
	}
}

func TestCLIAI_PromoteJSONConfirmation(t *testing.T) {
	r := require.New(t)
	tmp := t.TempDir()
	cfg := writeAIEmbedConfig(t, tmp)
	dbPath := filepath.Join(tmp, "catalog.sqlite")
	t.Setenv("FOTOBANK_DB_PATH", dbPath)
	d, err := db.Open(dbPath)
	r.NoError(err)
	gens := embedding.NewGenerations(d.WriteDB(), d.ReadDB())
	row, err := gens.FindOrCreateBuilding(t.Context(), embedTestFingerprint(), 8)
	r.NoError(err)
	_, err = d.WriteDB().ExecContext(t.Context(), `UPDATE embedding_generations SET state='retired', retired_at=? WHERE id=?`, time.Now().UTC().Add(-60*24*time.Hour), row.ID)
	r.NoError(err)
	r.NoError(d.Close())
	startCheckoutServer(t, cfg, dbPath)
	args := []string{"ai", "promote-generation", strconv.FormatInt(row.ID, 10), "--config", cfg, "--json"}
	var out, stderr bytes.Buffer
	r.Equal(1, cli.RunWithInput(t.Context(), args, strings.NewReader("n\n"), &out, &stderr))
	r.Empty(out.String())
	r.Contains(stderr.String(), "[y/N]")
	r.Contains(stderr.String(), "aborted")

	stderr.Reset()
	r.Zero(cli.RunWithInput(t.Context(), args, strings.NewReader("yes\n"), &out, &stderr), stderr.String())
	var result httpapi.PromoteGenerationResult
	r.NoError(json.Unmarshal(out.Bytes(), &result))
	r.Equal(httpapi.PromoteGenerationResult{ID: row.ID, Fingerprint: row.Fingerprint}, result)
	r.Contains(stderr.String(), "[y/N]")
	r.Contains(stderr.String(), "WARNING")
}

func TestCLIAI_CompactJSONPartialFailure(t *testing.T) {
	r := require.New(t)
	tmp := t.TempDir()
	cfg := writeAIEmbedConfig(t, tmp)
	dbPath := filepath.Join(tmp, "catalog.sqlite")
	t.Setenv("FOTOBANK_DB_PATH", dbPath)
	d, err := db.Open(dbPath)
	r.NoError(err)
	t.Cleanup(func() { r.NoError(d.Close()) })
	gens := embedding.NewGenerations(d.WriteDB(), d.ReadDB())
	var ids [2]int64
	for i := range ids {
		fp := embedTestFingerprint()
		fp.ModelID = fmt.Sprintf("retired-model-%d", i)
		row, err := gens.FindOrCreateBuilding(t.Context(), fp, 8)
		r.NoError(err)
		_, err = d.WriteDB().ExecContext(t.Context(), `UPDATE embedding_generations SET state='retired', retired_at=? WHERE id=?`, time.Now().UTC().Add(-60*24*time.Hour), row.ID)
		r.NoError(err)
		ids[i] = row.ID
	}
	// Fail the second deletion after the first generation has been removed.
	_, err = d.WriteDB().ExecContext(t.Context(), fmt.Sprintf(`CREATE TRIGGER fail_compaction
		BEFORE DELETE ON embedding_generations WHEN OLD.id = %d
		BEGIN SELECT RAISE(ABORT, 'compaction failed'); END`, ids[1]))
	r.NoError(err)
	startCheckoutServer(t, cfg, dbPath)
	args := []string{"ai", "compact-retired-generations", "--config", cfg, "--json"}
	out, stderr, code := runAICLI(append(args, "--dry-run")...)
	r.Zero(code, stderr)
	var result httpapi.CompactGenerationsResult
	r.NoError(json.Unmarshal([]byte(out), &result))
	r.Len(result.Candidates, 2)
	r.Equal(ids[0], result.Candidates[0].ID)
	r.Equal(ids[1], result.Candidates[1].ID)
	r.Zero(result.Dropped)
	r.Empty(result.Error)

	out, stderr, code = runAICLI(args...)
	r.Equal(1, code, stderr)
	r.NoError(json.Unmarshal([]byte(out), &result))
	r.Equal(1, result.Dropped)
	r.Contains(result.Error, "compaction failed")
	r.Contains(stderr, "compaction failed")

	_, err = d.WriteDB().ExecContext(t.Context(), `DROP TRIGGER fail_compaction`)
	r.NoError(err)
	out, stderr, code = runAICLI(args...)
	r.Zero(code, stderr)
	result = httpapi.CompactGenerationsResult{}
	r.NoError(json.Unmarshal([]byte(out), &result))
	r.Equal(1, result.Dropped, "only the remaining generation is removed")
	r.Empty(result.Error)
}
