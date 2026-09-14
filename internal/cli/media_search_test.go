package cli_test

import (
	"bytes"
	json "encoding/json/v2"
	"fmt"
	"path/filepath"
	"testing"
	"time"
	"uuid"

	"github.com/stretchr/testify/require"
	"go.kenn.io/fotobank/internal/cli"
	"go.kenn.io/fotobank/internal/db"
	"go.kenn.io/fotobank/internal/testutil"
)

func TestMediaSearch(t *testing.T) {
	r := require.New(t)
	dir := t.TempDir()
	cfg := writeBasicConfig(t, dir)
	dbPath := filepath.Join(dir, "catalog.sqlite")
	t.Setenv("FOTOBANK_DB_PATH", dbPath)
	d, err := db.Open(dbPath)
	r.NoError(err)
	t.Cleanup(func() { _ = d.Close() })
	owner := testutil.SeedOwner(t, d.WriteDB(), "h", "u")
	other := testutil.SeedOwner(t, d.WriteDB(), "h", "other")
	var ids [4]string
	for i := range ids {
		principal := owner
		if i == 3 {
			principal = other
		}
		ids[i] = testutil.SeedPhoto(t, d.WriteDB(), principal, fmt.Sprintf("photo-%d", i))
		_, err = d.WriteDB().Exec(`INSERT INTO media_fts(media_id, filename) VALUES (?, 'sunset.jpg')`, ids[i])
		r.NoError(err)
		_, err = d.WriteDB().Exec(`UPDATE assets SET timestamp=? WHERE id=?`, time.Date(2026, 1, i+1, 12, 0, 0, 0, time.UTC), ids[i])
		r.NoError(err)
	}
	_, err = d.WriteDB().Exec(`UPDATE assets SET hidden_at=? WHERE id=?`, time.Now().UTC(), ids[2])
	r.NoError(err)
	_, err = d.WriteDB().Exec(`UPDATE assets SET make='Example', model='Camera', lens_model='Wide', latitude=1, longitude=2, location_label='Coast' WHERE id=?`, ids[0])
	r.NoError(err)
	resultID := uuid.New()
	_, err = d.WriteDB().Exec(`INSERT INTO ai_results(id,media_id,task,model_id,prompt_version,prompt_hash,input_profile,status,generated_at)
		VALUES (?,?,'tag','test-model','v1','test-hash','test-profile','active',?)`, resultID, ids[0], time.Now().UTC())
	r.NoError(err)
	_, err = d.WriteDB().Exec(`INSERT INTO media_tags(result_id,tag_key,tag_label,rank) VALUES (?,'scenery:coast','Coast',1)`, resultID)
	r.NoError(err)
	r.NoError(d.Close())
	startCheckoutServer(t, cfg, dbPath)

	run := func(args ...string) string {
		t.Helper()
		var out, stderr bytes.Buffer
		args = append([]string{"media", "search"}, args...)
		args = append(args, "--config", cfg)
		r.Zero(cli.RunContext(t.Context(), args, &out, &stderr), "%s", stderr.String())
		return out.String()
	}
	var page struct {
		Results []struct {
			MediaID string `json:"media_id"`
		} `json:"results"`
		NextCursor    *string `json:"next_cursor"`
		HasMore       bool    `json:"has_more"`
		Reason        string  `json:"semantic_unavailable_reason"`
		EffectiveSort string  `json:"effective_sort"`
	}
	r.NoError(json.Unmarshal([]byte(run("sunset", "--sort", "oldest", "--limit", "1", "--json")), &page))
	r.Len(page.Results, 1)
	r.Equal(ids[0], page.Results[0].MediaID)
	r.Equal("embeddings_disabled", page.Reason)
	r.True(page.HasMore)
	r.NotNil(page.NextCursor)
	cursor := *page.NextCursor
	r.NoError(json.Unmarshal([]byte(run("sunset", "--sort", "oldest", "--limit", "1", "--cursor", cursor, "--json")), &page))
	r.Len(page.Results, 1)
	r.Equal(ids[1], page.Results[0].MediaID)
	r.False(page.HasMore)
	r.Nil(page.NextCursor)

	r.NoError(json.Unmarshal([]byte(run("sunset", "--type", "photo", "--camera", "Other", "--camera", "Example Camera", "--lens", "Other", "--lens", "Wide", "--tag", "missing", "--tag", "scenery:coast", "--tag-label", "Coast", "--location", "Coast", "--has-gps", "--date-after", "2026-01-01T00:00:00Z", "--date-before", "2026-01-02T00:00:00Z", "--json")), &page))
	r.Len(page.Results, 1)
	r.Equal(ids[0], page.Results[0].MediaID)
	r.NoError(json.Unmarshal([]byte(run("sunset", "--has-gps=false", "--json")), &page))
	r.Len(page.Results, 1)
	r.Equal(ids[1], page.Results[0].MediaID)
	r.NoError(json.Unmarshal([]byte(run("sunset", "--type", "video", "--json")), &page))
	r.Empty(page.Results)
	r.NoError(json.Unmarshal([]byte(run("--sort", "newest", "--json")), &page))
	r.Len(page.Results, 2)
	r.Equal(ids[1], page.Results[0].MediaID)
	r.Equal("newest", page.EffectiveSort)
	r.Empty(page.Reason)

	human := run("sunset", "--limit", "1")
	r.Contains(human, "--cursor")
	r.Contains(human, "metadata")
	r.Contains(human, "ID")
	for _, args := range [][]string{
		{"changed", "--sort", "oldest", "--limit", "1", "--cursor", cursor},
		{"sunset", "--cursor", "not-a-cursor"},
	} {
		var out, stderr bytes.Buffer
		args = append(append([]string{"media", "search"}, args...), "--config", cfg, "--json")
		r.Equal(1, cli.RunContext(t.Context(), args, &out, &stderr))
		r.Empty(out.String())
		r.Contains(stderr.String(), "400")
	}
}
