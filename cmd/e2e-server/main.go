// Command e2e-server boots a fotobank server against a freshly created
// temp-file SQLite database so Playwright can run smoke tests against
// the embedded SPA. It writes a minimal TOML config under a temp dir,
// then delegates to internal/cli the same way the production fotobank
// binary does.
//
// The server listens on 127.0.0.1:$FOTOBANK_E2E_PORT (default 18080) so
// the Playwright webServer config in the frontend package can target a
// matching URL. The default is 18080 (not 8080) because 8080 is a
// commonly-contested port; the env var override lets dev environments
// shift if they hit collisions.
//
// Environment variables:
//   - FOTOBANK_E2E_PORT: listen port (default 18080)
//   - FOTOBANK_E2E_HIDDEN_UNCONFIGURED=1: skip credential seed so the
//     "unconfigured CTA" Playwright test can assert the gate CTA renders
//   - FOTOBANK_E2E_LOCKOUT_WINDOW: duration string (e.g. "5s") to override
//     the hidden-auth lockout window and duration (default 300s production).
//     The threshold stays at 5 failures; only the window and lockout duration
//     shrink so the lockout test can complete without real wait. The override
//     is gated on FOTOBANK_E2E_MODE=1 (set by this command) so an inherited
//     environment variable cannot weaken a production deployment.
//   - FOTOBANK_E2E_AI_PRE_ACK=1: pre-record the hidden-processing
//     acknowledgement during fixture seeding. Default leaves the ack
//     modal active so the AI Playwright spec can exercise the gate.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"math"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/google/uuid"

	"go.kenn.io/fotobank/internal/ai"
	"go.kenn.io/fotobank/internal/ai/ack"
	"go.kenn.io/fotobank/internal/ai/embedding"
	"go.kenn.io/fotobank/internal/ai/failures"
	"go.kenn.io/fotobank/internal/ai/imginput"
	"go.kenn.io/fotobank/internal/ai/parse"
	aiprompts "go.kenn.io/fotobank/internal/ai/prompts"
	"go.kenn.io/fotobank/internal/ai/results"
	"go.kenn.io/fotobank/internal/album"
	"go.kenn.io/fotobank/internal/auth/hidden"
	"go.kenn.io/fotobank/internal/cli"
	"go.kenn.io/fotobank/internal/content"
	"go.kenn.io/fotobank/internal/db"
	"go.kenn.io/fotobank/internal/media"
	"go.kenn.io/fotobank/internal/owners"
	"go.kenn.io/fotobank/internal/service"
	"go.kenn.io/fotobank/internal/share"
	"go.kenn.io/fotobank/internal/testutil/mediaseed"
	"go.kenn.io/fotobank/internal/testutil/scalecache"
	"go.kenn.io/fotobank/internal/thumb"
)

// e2eVisionModelID matches the [ai.tag] / [ai.caption] model entries
// the cfg heredoc writes — the AI fingerprints persisted on seeded
// results must use the same model id so the gap scanner skips them.
const e2eVisionModelID = "qwen2.5-vl:3b"

// Embed pipeline constants. Keep all three in lockstep — the seeded
// embedding generation row carries this fingerprint (model + input
// profile), and the mock /v1/embeddings endpoint responds with vectors
// of e2eEmbedDim. A mismatch between the seed dim and the configured
// dim makes the boot Probe fail; a mismatch between the seed input
// profile and the cfg.AI.Embed.InputEdge makes the gap scanner think
// the seeded mappings belong to a different fingerprint and re-enqueue
// them.
const (
	e2eEmbedModelID  = "siglip2"
	e2eEmbedDim      = 4
	e2eEmbedEdge     = 384
	e2eEmbedProfile  = "jpeg-384-q85-metadata-stripped-embed-v1"
	e2eVisibleCount  = 30
	e2eHiddenCount   = 5
	e2eMappedCount   = 22 // 22/30 ≈ 73% — under the 80% banner threshold.
	e2eOwnerHub      = "local"
	e2eOwnerUserID   = "alice"
	e2eOwnerStorageK = "550e8400-e29b-41d4-a716-44665544000e"
)

// e2ePort returns the listen port for the e2e server, honoring
// FOTOBANK_E2E_PORT and falling back to 18080.
func e2ePort() string {
	if p := os.Getenv("FOTOBANK_E2E_PORT"); p != "" {
		return p
	}
	return "18080"
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	// Mark this process as the e2e server so internal/cli/server.go will
	// honor FOTOBANK_E2E_LOCKOUT_WINDOW. A bare production process with
	// FOTOBANK_E2E_LOCKOUT_WINDOW inherited from the shell silently
	// ignores the override.
	if err := os.Setenv("FOTOBANK_E2E_MODE", "1"); err != nil {
		return fmt.Errorf("set FOTOBANK_E2E_MODE: %w", err)
	}
	tmp, err := os.MkdirTemp("", "fotobank-e2e-")
	if err != nil {
		return fmt.Errorf("creating temp dir: %w", err)
	}
	cfgPath := filepath.Join(tmp, "fotobank.toml")
	nasRoot := filepath.Join(tmp, "nas")
	flashRoot := filepath.Join(tmp, "flash")
	docbankRoot := filepath.Join(tmp, "docbank")
	for _, d := range []string{nasRoot, flashRoot, docbankRoot} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return fmt.Errorf("creating %s: %w", d, err)
		}
	}

	// Mock OpenAI-compat VLM. Started before the cfg is written so its
	// listen URL can be threaded into [ai.vision].endpoint. The server
	// is intentionally leaked: the CLI server runs in the foreground for
	// the lifetime of the e2e process, so a graceful shutdown isn't
	// required — the OS reclaims the listener on exit.
	vlmURL, _, err := startMockVLM()
	if err != nil {
		return fmt.Errorf("start mock vlm: %w", err)
	}

	// Mock OpenAI-compat embeddings endpoint. Same lifetime semantics
	// as the VLM mock. Returns deterministic e2eEmbedDim-dim vectors so
	// the boot Probe and any runtime EmbedTexts call land successfully;
	// see startMockEmbed for the failure-injection knobs.
	embedURL, _, err := startMockEmbed()
	if err != nil {
		return fmt.Errorf("start mock embed: %w", err)
	}

	// FOTOBANK_TEST_EMBED_GAPSCAN_INTERVAL / _ACTIVATOR_TICK / _COMPACTOR_INTERVAL
	// override the production-default 1m / 1m / 24h cadences. The seed
	// directly inserts an active generation row + 22/30 mappings; the
	// runtime workers must not race the seed by enqueuing fresh embed
	// jobs for the unmapped 8, or the under-80% banner test would flap
	// as completeness drifts upward during the run. Setting all three
	// to 1h keeps the workers idle for the duration of the suite while
	// still wiring the boot path so the Probe and search service get
	// constructed.
	for k, v := range map[string]string{
		"FOTOBANK_TEST_EMBED_GAPSCAN_INTERVAL":   "1h",
		"FOTOBANK_TEST_EMBED_ACTIVATOR_TICK":     "1h",
		"FOTOBANK_TEST_EMBED_COMPACTOR_INTERVAL": "1h",
	} {
		if err := os.Setenv(k, v); err != nil {
			return fmt.Errorf("set %s: %w", k, err)
		}
	}

	// Default the sharing UI to ON so the broad Playwright suite — which
	// asserts share buttons, modals, and the /shares route — runs against
	// the same surface the production binary exposes when an operator
	// opts in. The sharing-disabled variant (I3) sets
	// FOTOBANK_E2E_SHARING_ENABLED=false to verify the gated SPA.
	sharingEnabled := os.Getenv("FOTOBANK_E2E_SHARING_ENABLED") != "false"

	cfg := fmt.Sprintf(`
[nas]
root = "%s"
[docbank]
root = "%s"
[flash]
root = "%s"
[identity]
mode = "stub"
[identity.stub]
hub = "%s"
user_id = "%s"
handle = "Alice"
storage_key = "%s"
[ui]
sharing_enabled = %t
[http]
listen_address = "127.0.0.1:%s"
# dev_insecure_cookies must be true for the e2e server: the __Host- prefix
# and Secure flag prevent unlock cookies from round-tripping over plain
# http://127.0.0.1 — the browser drops them silently. The dev cookie name
# (fotobank-hidden, no Secure) is used instead.
dev_insecure_cookies = true
[imports]
file_lock_path = "%s"
[backup]
enabled = false
[observability]
admin_listen = "127.0.0.1:0"
[ai]
enabled = true
[ai.vision]
endpoint = "%s/v1"
# 5s timeout keeps a hung mock from stalling the e2e suite. The mock
# answers in <10ms in practice, so this is generous.
timeout = "5s"
max_retries = 1
max_inflight = 1
[ai.tag]
enabled = true
model = "%s"
worker_concurrency = 1
[ai.caption]
enabled = true
model = "%s"
worker_concurrency = 1
[ai.embed]
enabled = true
model = "%s"
endpoint = "%s/v1"
dimension = %d
input_edge = %d
timeout = "5s"
max_retries = 1
worker_concurrency = 1
batch_size = 8
[search]
# Lower the activation threshold so the seeded mapping count crosses
# the bar even when the activator picks up a building generation
# mid-run (defense-in-depth — the seed inserts active directly).
activation_threshold = 50
`, nasRoot, docbankRoot, flashRoot, e2eOwnerHub, e2eOwnerUserID, e2eOwnerStorageK,
		sharingEnabled,
		e2ePort(), filepath.Join(tmp, "import.lock"),
		vlmURL, e2eVisionModelID, e2eVisionModelID,
		e2eEmbedModelID, embedURL, e2eEmbedDim, e2eEmbedEdge)
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		return fmt.Errorf("writing config: %w", err)
	}

	// Seed deterministic media rows so the Playwright MediaDetail GPS
	// tests can navigate to known IDs without env-var plumbing. The
	// server reopens the DB on startup; SQLite + golang-migrate
	// migrations are idempotent, so this is safe.
	//
	// Scale mode (FOTOBANK_E2E_SCALE_ROWS=N) skips every fixture
	// function and instead seeds N rows via mediaseed.SeedScaleLibrary
	// — used by the /library Playwright scale spec, where the existing
	// curated fixtures (~50 rows) wouldn't exercise virtualization,
	// pagination, or the request-fan-out the spec measures.
	dbPath := filepath.Join(flashRoot, "fotobank.sqlite")
	if scaleRows, ok := scaleModeRows(); ok {
		realThumbs := scaleRealThumbsEnabled()
		if err := seedScaleFixtures(dbPath, nasRoot, scaleRows, realThumbs); err != nil {
			return fmt.Errorf("seed scale fixtures: %w", err)
		}
	} else if err := seedFixtures(dbPath, nasRoot, docbankRoot); err != nil {
		return fmt.Errorf("seed fixtures: %w", err)
	}

	// SIGINT/SIGTERM forwarding lives inside cli.RunContext's server
	// subcommand (signal.NotifyContext on the inbound ctx). Wiring a
	// second handler here would be a no-op race against the inner one.
	ctx := context.Background()
	if code := cli.RunContext(ctx, []string{"server", "--config", cfgPath}, os.Stdout, os.Stderr); code != 0 {
		return fmt.Errorf("server exited with code %d", code)
	}
	return nil
}

// startMockVLM stands up an OpenAI-compatible chat-completions stub on
// a free loopback port. It implements the two routes the production
// gateway hits: GET /v1/models (health probe) and POST
// /v1/chat/completions (worker generate). Tag vs caption requests are
// disambiguated by scanning the user message for the substring "tags"
// (the canonical tag prompt mentions JSON tags); each branch returns a
// deterministic JSON envelope wrapped in the OpenAI choices shape.
func startMockVLM() (string, *http.Server, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", nil, fmt.Errorf("listen mock vlm: %w", err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/models", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{
				{"id": e2eVisionModelID, "object": "model"},
			},
		})
	})
	mux.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []struct {
				Role    string `json:"role"`
				Content []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		// Concatenate every text part across messages so the matcher
		// works regardless of how the worker structures the prompt.
		var allText strings.Builder
		for _, m := range body.Messages {
			for _, c := range m.Content {
				if c.Type == "text" {
					allText.WriteString(c.Text)
					allText.WriteByte('\n')
				}
			}
		}
		var inner string
		if strings.Contains(allText.String(), "\"tags\"") {
			inner = `{"tags":["e2e-tag-a","e2e-tag-b"]}`
		} else {
			inner = `{"caption":"An e2e test photo of a small dog on a beach."}`
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{
				{"message": map[string]any{"role": "assistant", "content": inner}},
			},
		})
	})
	srv := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		// http.ErrServerClosed only fires on Shutdown, which we never
		// call in the e2e harness — anything here is a real listen
		// failure that the operator should see at the next request.
		_ = srv.Serve(ln)
	}()
	return "http://" + ln.Addr().String(), srv, nil
}

// startMockEmbed stands up an OpenAI-compatible embeddings stub on a
// free loopback port. Returns deterministic e2eEmbedDim-dim vectors so
// the boot embedding.Probe and any runtime text-embed call land cleanly.
//
// Failure injection: when FOTOBANK_E2E_EMBED_FAIL_EVERY_N is set to a
// positive integer N, every Nth POST to /embeddings returns 503. The
// boot Probe sends two requests (one image, one text) so a small N
// would fail the boot — the env var defaults to "0" (disabled) and
// the query_embedding_failed Playwright test sets it to a value high
// enough to clear the boot probe (e.g. 100) and uses a separate
// trip-now URL to flip the failure flag synchronously between the
// boot probe and the test request. v1 keeps the mock simple and
// trips on every Nth call regardless of probe vs runtime; the e2e
// spec for query_embedding_failed is deferred (see search.spec.ts).
func startMockEmbed() (string, *http.Server, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", nil, fmt.Errorf("listen mock embed: %w", err)
	}
	mux := http.NewServeMux()
	// counter is incremented atomically per /embeddings hit so the 503
	// rate is deterministic regardless of test parallelism.
	var counter atomic.Int64
	failEveryN := embedFailEveryN()
	// /trip-next-failure flips an override that fails the next /embeddings
	// call regardless of the modulo gate. Used by tests that want a
	// single 503 without touching the env var (which would also affect
	// the boot probe). Hits to this path do not increment counter.
	var tripNext atomic.Bool
	mux.HandleFunc("/trip-next-failure", func(w http.ResponseWriter, _ *http.Request) {
		tripNext.Store(true)
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/v1/embeddings", func(w http.ResponseWriter, r *http.Request) {
		// Increment the counter first so the failure modulo is
		// computed against the post-increment ordinal — N=2 means
		// requests 2, 4, 6 fail, not 0, 2, 4.
		n := counter.Add(1)
		if tripNext.Swap(false) || (failEveryN > 0 && n%failEveryN == 0) {
			http.Error(w, `{"error":"injected 503"}`, http.StatusServiceUnavailable)
			return
		}
		var body struct {
			Input []string `json:"input"`
			Model string   `json:"model"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		// One vector per input, in input order. deterministicVec
		// derives a vector from the input index so the response is
		// stable across runs and the seed-side vectors collide with
		// the runtime-side query vectors during ANN search.
		out := struct {
			Data  []map[string]any `json:"data"`
			Model string           `json:"model"`
		}{Model: body.Model}
		for i := range body.Input {
			out.Data = append(out.Data, map[string]any{
				"embedding": deterministicVec(i, e2eEmbedDim),
				"index":     i,
			})
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(out)
	})
	srv := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() { _ = srv.Serve(ln) }()
	return "http://" + ln.Addr().String(), srv, nil
}

// embedFailEveryN reads the failure-modulo from FOTOBANK_E2E_EMBED_FAIL_EVERY_N.
// Defaults to 0 (no failures). Returns 0 on any parse error so a
// malformed env var doesn't accidentally enable failure injection.
func embedFailEveryN() int64 {
	raw := os.Getenv("FOTOBANK_E2E_EMBED_FAIL_EVERY_N")
	if raw == "" {
		return 0
	}
	var n int64
	if _, err := fmt.Sscan(raw, &n); err != nil || n < 0 {
		return 0
	}
	return n
}

// deterministicVec returns a stable e2eEmbedDim-length vector derived
// from i. Uses a simple sin/cos pattern so distinct i values produce
// distinct (and L2-comparable) vectors. The vectors are not normalized
// — the search backend's ANN MATCH on FLOAT[N] doesn't require unit
// length — but they are bounded in [-1, 1] so the per-element
// representation is well-behaved.
func deterministicVec(i, dim int) []float32 {
	v := make([]float32, dim)
	// scale i into a phase angle so neighbouring i values produce
	// neighbouring (but non-identical) vectors; the prime constant
	// breaks any aliasing against dim.
	phase := float64(i) * 0.6180339887
	for j := range dim {
		v[j] = float32(math.Sin(phase + float64(j)*1.5707963))
	}
	return v
}

// seedFixtures inserts an owner row and deterministic media rows used by
// the Playwright suite. The IDs are referenced verbatim by tests in
// frontend/tests/e2e/*.spec.ts.
//
// Hidden fixtures:
//   - auth_hidden_credential for the stub principal with passcode "e2e-passcode"
//     (Argon2id-hashed). Skipped when FOTOBANK_E2E_HIDDEN_UNCONFIGURED=1.
//   - hidden-prehidden-1: hidden_at = now (used by gate/grid tests)
//   - hidden-target-1:    hidden_at = NULL (used by hide-flow test)
func seedFixtures(dbPath, nasRoot, docbankRoot string) error {
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o700); err != nil {
		return fmt.Errorf("create db dir: %w", err)
	}
	d, err := db.Open(dbPath)
	if err != nil {
		return fmt.Errorf("open db: %w", err)
	}
	defer func() { _ = d.Close() }()

	ctx := context.Background()
	owner := owners.Principal{Hub: "local", UserID: "alice"}
	if _, err := d.WriteDB().ExecContext(ctx,
		`INSERT OR IGNORE INTO owners(hub, user_id, storage_key, created_at)
		 VALUES (?, ?, ?, ?)`,
		owner.Hub, owner.UserID, "550e8400-e29b-41d4-a716-44665544000e", time.Now().UTC(),
	); err != nil {
		return fmt.Errorf("seed owner: %w", err)
	}

	repo := media.NewRepo(d.WriteDB(), d.ReadDB())
	contentStore, err := content.Open(ctx, content.Config{Root: docbankRoot})
	if err != nil {
		return fmt.Errorf("open seed Docbank vault: %w", err)
	}
	defer contentStore.Close()
	insert := func(row media.Media) error {
		return insertFixtureAsset(ctx, d.WriteDB(), contentStore, "550e8400-e29b-41d4-a716-44665544000e", row)
	}
	lat, lon := 48.8566, 2.3522
	now := time.Now().UTC()
	gpsRow := media.Media{
		ID:                 "gps-fixture-1",
		Owner:              owner,
		Type:               media.TypePhoto,
		MimeType:           "image/jpeg",
		DocbankVirtualPath: "gps-fixture-1.jpg",
		ImportedAt:         now,
		Size:               1,
		SHA256:             "checksum-gps-fixture-1",
		Latitude:           &lat,
		Longitude:          &lon,
		LocationLabel:      "Paris, Île-de-France, France",
		ThumbStatus:        "pending",
	}
	if err := insert(gpsRow); err != nil {
		return fmt.Errorf("seed gps fixture: %w", err)
	}

	// Map clustering fixtures. The Bay Area pair sits ~13km apart
	// (downtown SF vs Oakland) — close enough to cluster at zoom 10 but
	// far enough to render as separate markers at zoom 12+, which is
	// what the marker-click e2e relies on. The NYC row is always a
	// separate marker. Default Leaflet markercluster radius is 80px;
	// at zoom 12 the SF↔Oakland pair lands well outside that radius.
	geoFixtures := []struct {
		id    string
		lat   float64
		lon   float64
		label string
	}{
		{"geo-photo-a", 37.7749, -122.4194, "San Francisco, California, USA"},
		{"geo-photo-b", 37.8044, -122.2712, "Oakland, California, USA"},
		{"geo-photo-c", 40.7128, -74.0060, "New York, New York, USA"},
	}
	for _, g := range geoFixtures {
		latP, lonP := g.lat, g.lon
		row := media.Media{
			ID:                 g.id,
			Owner:              owner,
			Type:               media.TypePhoto,
			MimeType:           "image/jpeg",
			DocbankVirtualPath: g.id + ".jpg",
			ImportedAt:         now,
			Size:               1,
			SHA256:             "checksum-" + g.id,
			Latitude:           &latP,
			Longitude:          &lonP,
			LocationLabel:      g.label,
			ThumbStatus:        "pending",
		}
		if err := insert(row); err != nil {
			return fmt.Errorf("seed map cluster fixture %s: %w", g.id, err)
		}
	}
	noGPSRow := media.Media{
		ID:                 "no-gps-fixture-1",
		Owner:              owner,
		Type:               media.TypePhoto,
		MimeType:           "image/jpeg",
		DocbankVirtualPath: "no-gps-fixture-1.jpg",
		ImportedAt:         now,
		Size:               1,
		SHA256:             "checksum-no-gps-fixture-1",
		ThumbStatus:        "pending",
	}
	if err := insert(noGPSRow); err != nil {
		return fmt.Errorf("seed no-gps fixture: %w", err)
	}

	// Multi-file asset used by the detail-page Files row.
	primaryRow := media.Media{
		ID:                 "pair-fixture-primary",
		Owner:              owner,
		Type:               media.TypePhoto,
		MimeType:           "image/jpeg",
		DocbankVirtualPath: "pair-fixture-primary.jpg",
		OriginalFilename:   "IMG_1.JPG",
		ImportedAt:         now,
		Size:               1,
		SHA256:             "checksum-pair-fixture-primary",
		ThumbStatus:        "pending",
	}
	if err := insert(primaryRow); err != nil {
		return fmt.Errorf("seed pair fixture primary: %w", err)
	}
	if err := insertFixtureFile(ctx, d.WriteDB(), contentStore,
		"550e8400-e29b-41d4-a716-44665544000e", primaryRow.ID,
		media.RoleOriginal, "image/x-adobe-dng", "IMG_1.DNG"); err != nil {
		return fmt.Errorf("seed pair fixture original: %w", err)
	}

	// F2.3 album + share seeds. Albums and shares for the Playwright
	// e2e suite — exercised by frontend/tests/e2e/albums.spec.ts and
	// shares.spec.ts. AlbumService/ShareService both enforce stub-mode
	// owner scoping; the seeds use the same owner Principal so the
	// Playwright caller (the SPA) can read them via the API.
	albumRepo := album.NewRepo(d.WriteDB(), d.ReadDB())
	shareRepo := share.NewRepo(d.WriteDB(), d.ReadDB())
	albumSvc := service.NewAlbumService(albumRepo, repo, shareRepo, d)
	shareSvc := service.NewShareService(shareRepo, albumRepo, repo)

	if _, err := albumSvc.Create(ctx, owner, "E2E Empty Album"); err != nil {
		return fmt.Errorf("seed empty album: %w", err)
	}
	seededAlbum, err := albumSvc.Create(ctx, owner, "E2E Italy 2025")
	if err != nil {
		return fmt.Errorf("seed populated album: %w", err)
	}
	if _, _, err := albumSvc.AddMedia(ctx, seededAlbum.ID, []string{
		"pair-fixture-primary", "gps-fixture-1",
	}, owner); err != nil {
		return fmt.Errorf("seed populated album members: %w", err)
	}

	if _, err := shareSvc.Create(ctx, service.CreateShareRequest{
		TargetType: share.TargetMediaSet,
		MediaIDs:   []string{"gps-fixture-1"},
		Grantee:    owners.Principal{Hub: "noop", UserID: "e2e"},
		Label:      "Active e2e share",
	}, owner); err != nil {
		return fmt.Errorf("seed active share: %w", err)
	}

	// Album-targeted share for the sharing-disabled e2e test, which
	// exercises the AlbumDetail delete-blocked CLI-aware copy: deleting
	// an album with active shares returns 409, and the SPA must surface
	// the CLI command (`fotobank shares list --album <id>`) regardless
	// of the sharing UI flag because the CLI works regardless of it.
	if _, err := shareSvc.Create(ctx, service.CreateShareRequest{
		TargetType: share.TargetAlbumLive,
		AlbumID:    seededAlbum.ID,
		Grantee:    owners.Principal{Hub: "noop", UserID: "e2e-album"},
		Label:      "Active album e2e share",
	}, owner); err != nil {
		return fmt.Errorf("seed active album share: %w", err)
	}

	// F2.4 hidden privacy fixtures.

	// hidden-prehidden-1: already hidden at seed time — used by gate-render
	// and /hidden grid tests (the grid must show this row when unlocked).
	prehidden := media.Media{
		ID:                 "hidden-prehidden-1",
		Owner:              owner,
		Type:               media.TypePhoto,
		MimeType:           "image/jpeg",
		DocbankVirtualPath: "hidden-prehidden-1.jpg",
		ImportedAt:         now,
		Size:               1,
		SHA256:             "checksum-hidden-prehidden-1",
		ThumbStatus:        "pending",
	}
	if err := insert(prehidden); err != nil {
		return fmt.Errorf("seed hidden-prehidden-1: %w", err)
	}
	if _, err := d.WriteDB().ExecContext(ctx,
		`UPDATE assets SET hidden_at = ? WHERE id = ?`, now, "hidden-prehidden-1",
	); err != nil {
		return fmt.Errorf("seed hidden-prehidden-1 hidden_at: %w", err)
	}

	// hidden-target-1: visible — used by the hide-flow test which triggers
	// the hide action via the UI (exercising the cascade and store paths).
	target := media.Media{
		ID:                 "hidden-target-1",
		Owner:              owner,
		Type:               media.TypePhoto,
		MimeType:           "image/jpeg",
		DocbankVirtualPath: "hidden-target-1.jpg",
		ImportedAt:         now,
		Size:               1,
		SHA256:             "checksum-hidden-target-1",
		ThumbStatus:        "pending",
	}
	if err := insert(target); err != nil {
		return fmt.Errorf("seed hidden-target-1: %w", err)
	}

	// hidden-album-target-1: visible, added to the Italy album — used by the
	// album hidden_count chip test so hiding this doesn't contaminate the
	// gps-fixture-1 fixture that the shares test relies on.
	albumTarget := media.Media{
		ID:                 "hidden-album-target-1",
		Owner:              owner,
		Type:               media.TypePhoto,
		MimeType:           "image/jpeg",
		DocbankVirtualPath: "hidden-album-target-1.jpg",
		ImportedAt:         now,
		Size:               1,
		SHA256:             "checksum-hidden-album-target-1",
		ThumbStatus:        "pending",
	}
	if err := insert(albumTarget); err != nil {
		return fmt.Errorf("seed hidden-album-target-1: %w", err)
	}
	// Add hidden-album-target-1 to the Italy album after inserting it.
	if _, _, err := albumSvc.AddMedia(ctx, seededAlbum.ID, []string{
		"hidden-album-target-1",
	}, owner); err != nil {
		return fmt.Errorf("seed hidden-album-target-1 album membership: %w", err)
	}

	// hidden-share-member-1: visible, included in a dedicated share
	// ("Hidden grantee e2e share") so scenario 14 can hide it without
	// contaminating gps-fixture-1 which is used by shares.spec.ts.
	shareMember := media.Media{
		ID:                 "hidden-share-member-1",
		Owner:              owner,
		Type:               media.TypePhoto,
		MimeType:           "image/jpeg",
		DocbankVirtualPath: "hidden-share-member-1.jpg",
		ImportedAt:         now,
		Size:               1,
		SHA256:             "checksum-hidden-share-member-1",
		ThumbStatus:        "pending",
	}
	if err := insert(shareMember); err != nil {
		return fmt.Errorf("seed hidden-share-member-1: %w", err)
	}
	if _, err := shareSvc.Create(ctx, service.CreateShareRequest{
		TargetType: share.TargetMediaSet,
		MediaIDs:   []string{"hidden-share-member-1"},
		Grantee:    owners.Principal{Hub: "noop", UserID: "e2e-hidden"},
		Label:      "Hidden grantee e2e share",
	}, owner); err != nil {
		return fmt.Errorf("seed hidden grantee share: %w", err)
	}

	// Seed the hidden credential unless running the "unconfigured" variant.
	// FOTOBANK_E2E_HIDDEN_UNCONFIGURED=1 skips this so the CTA test can
	// assert that the gate renders "Hidden privacy isn't set up."
	if os.Getenv("FOTOBANK_E2E_HIDDEN_UNCONFIGURED") != "1" {
		hash, err := hidden.HashPasscode("e2e-passcode")
		if err != nil {
			return fmt.Errorf("hash e2e passcode: %w", err)
		}
		hiddenRepo := hidden.NewRepo(d.WriteDB(), d.ReadDB())
		if err := hiddenRepo.UpsertCredential(ctx, owner, hash, now); err != nil {
			return fmt.Errorf("seed hidden credential: %w", err)
		}
	}

	if err := seedF2_5Fixtures(ctx, d, repo, albumRepo, albumSvc, owner, insert); err != nil {
		return fmt.Errorf("seed f2.5 fixtures: %w", err)
	}

	if err := seedAIFixtures(ctx, d, repo, owner, nasRoot, "550e8400-e29b-41d4-a716-44665544000e", insert); err != nil {
		return fmt.Errorf("seed ai fixtures: %w", err)
	}

	if err := seedSearchFixtures(ctx, d, repo, owner, insert); err != nil {
		return fmt.Errorf("seed search fixtures: %w", err)
	}

	if err := seedFacetFixtures(ctx, d, repo, owner, insert); err != nil {
		return fmt.Errorf("seed facet fixtures: %w", err)
	}

	return nil
}

func insertFixtureAsset(ctx context.Context, rw *sql.DB, store *content.Adapter, storageKey string, row media.Media) error {
	filename := row.OriginalFilename
	if filename == "" {
		filename = row.ID + ".jpg"
	}
	fileID := uuid.NewString()
	virtualPath, err := content.VirtualPath(storageKey, fileID, filename)
	if err != nil {
		return err
	}
	payload := []byte(row.ID)
	if row.MimeType == "image/jpeg" {
		payload, err = smallTestJPEG()
		if err != nil {
			return err
		}
		// JPEG decoders ignore bytes after the end marker. Appending the
		// fixture ID keeps every authoritative object distinct while still
		// exercising the real thumbnail decoder.
		payload = append(payload, row.ID...)
	}
	digest := sha256.Sum256(payload)
	identity := content.Identity{SHA256: hex.EncodeToString(digest[:]), Size: int64(len(payload))}
	receipt, err := store.Create(ctx, content.CreateRequest{
		VirtualPath: virtualPath, MediaType: row.MimeType, Expected: identity,
		Reader: bytes.NewReader(payload),
	})
	if err != nil {
		return err
	}
	if _, err := rw.ExecContext(ctx, `
		INSERT INTO assets (
			id, owner_hub, owner_user_id, state, media_type, imported_at, timestamp,
			make, model, lens_model, focal_length, shutter, width, height, iso, aperture,
			duration_ms, latitude, longitude, gps_at, location_label,
			thumb_status, thumb_version, thumb_updated_at, hidden_at
		) VALUES (?, ?, ?, 'pending', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		row.ID, row.Owner.Hub, row.Owner.UserID, row.Type, row.ImportedAt, row.Timestamp,
		row.Make, row.Model, row.LensModel, row.FocalLength, row.Shutter,
		row.Width, row.Height, row.ISO, row.Aperture, row.DurationMs,
		row.Latitude, row.Longitude, row.GPSAt, row.LocationLabel,
		row.ThumbStatus, row.ThumbVersion, row.ThumbUpdatedAt, row.HiddenAt,
	); err != nil {
		return err
	}
	if _, err := rw.ExecContext(ctx, `
		INSERT INTO media_files (
			id, asset_id, owner_hub, owner_user_id, role, mime_type,
			original_filename, size, docbank_node_id, docbank_virtual_path,
			current_version_id, sha256
		) VALUES (?, ?, ?, ?, 'primary', ?, ?, ?, ?, ?, ?, ?)`,
		fileID, row.ID, row.Owner.Hub, row.Owner.UserID, row.MimeType, filename,
		identity.Size, receipt.Node.ID, virtualPath, receipt.Version.ID, identity.SHA256,
	); err != nil {
		return err
	}
	_, err = rw.ExecContext(ctx, `UPDATE assets SET state = 'ready' WHERE id = ?`, row.ID)
	return err
}

func insertFixtureFile(
	ctx context.Context,
	rw *sql.DB,
	store *content.Adapter,
	storageKey, assetID string,
	role media.FileRole,
	mimeType, filename string,
) error {
	fileID := uuid.NewString()
	virtualPath, err := content.VirtualPath(storageKey, fileID, filename)
	if err != nil {
		return err
	}
	body := []byte(assetID + ":" + filename)
	digest := sha256.Sum256(body)
	identity := content.Identity{SHA256: hex.EncodeToString(digest[:]), Size: int64(len(body))}
	receipt, err := store.Create(ctx, content.CreateRequest{
		VirtualPath: virtualPath, MediaType: mimeType, Expected: identity,
		Reader: strings.NewReader(string(body)),
	})
	if err != nil {
		return err
	}
	var ownerHub, ownerUserID, primaryFileID string
	if err := rw.QueryRowContext(ctx, `
		SELECT a.owner_hub, a.owner_user_id, f.id
		  FROM assets a
		  JOIN media_files f ON f.asset_id = a.id AND f.role = 'primary'
		 WHERE a.id = ?`, assetID).Scan(&ownerHub, &ownerUserID, &primaryFileID); err != nil {
		return err
	}
	if _, err := rw.ExecContext(ctx, `
		INSERT INTO media_files (
			id, asset_id, owner_hub, owner_user_id, role, mime_type,
			original_filename, size, docbank_node_id, docbank_virtual_path,
			current_version_id, sha256
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		fileID, assetID, ownerHub, ownerUserID, role, mimeType, filename,
		identity.Size, receipt.Node.ID, virtualPath, receipt.Version.ID, identity.SHA256,
	); err != nil {
		return err
	}
	_, err = rw.ExecContext(ctx, `
		INSERT INTO media_file_relationships (source_file_id, target_file_id, kind)
		VALUES (?, ?, 'paired_with')`, fileID, primaryFileID)
	return err
}

// scaleModeRows reads FOTOBANK_E2E_SCALE_ROWS and returns (n, true) when
// the env var is a positive integer. (0, false) signals scale mode is
// off. Negative or non-numeric values fail closed (off) — better to
// boot in normal mode than misinterpret a typo as 0 rows.
func scaleModeRows() (int, bool) {
	raw := os.Getenv("FOTOBANK_E2E_SCALE_ROWS")
	if raw == "" {
		return 0, false
	}
	var n int
	if _, err := fmt.Sscanf(raw, "%d", &n); err != nil || n <= 0 {
		return 0, false
	}
	return n, true
}

// scaleRealThumbsEnabled returns true when FOTOBANK_E2E_SCALE_REAL_THUMBS
// is "1" or "true". Used by PS-3f's content-visibility re-evaluation:
// the default scale mode writes no thumb files (every /thumb 404s and
// the SPA renders the broken-image placeholder), which is what we want
// for measuring DOM/listener cost. Real-thumbs mode writes a tiny JPEG
// for every seeded row so img.decode work is actually present, which
// is what content-visibility's deferral can measurably defer.
func scaleRealThumbsEnabled() bool {
	v := os.Getenv("FOTOBANK_E2E_SCALE_REAL_THUMBS")
	return v == "1" || v == "true"
}

// scaleCacheDisabled returns true when FOTOBANK_E2E_SCALE_NO_CACHE is
// set to "1" or "true". Used by anyone debugging the seed itself —
// flipping this off forces every run to re-seed from scratch so a
// stale cache entry can't paper over a bug in the seed implementation.
func scaleCacheDisabled() bool {
	v := os.Getenv("FOTOBANK_E2E_SCALE_NO_CACHE")
	return v == "1" || v == "true"
}

// seedScaleFixtures replaces the curated seedFixtures path with a bulk
// seed of n media rows (and ~50% tagged) via mediaseed.SeedScaleLibrary.
// The owner matches the stub-identity principal (local/alice) so the
// SPA receives the same /api/v1/me payload it would in normal mode.
//
// No album/AI/search/facet fixtures. The /library scale spec measures
// DOM growth, request fan-out, and frame timing; none of those need
// album/search structure.
//
// realThumbs gates per-row thumb-blob writes:
//   - false (default): /thumb returns 404 for every cell, the SPA
//     renders the broken-image placeholder, and the request tally
//     includes those 404s — that's the realistic DOM/listener shape.
//   - true: writes a tiny grid-tier JPEG for every row so /thumb
//     returns 200 and the browser does img.decode work. PS-3f's
//     content-visibility A/B uses this so c-v's deferral can
//     measurably defer something instead of only bearing its cost.
//
// scalecache short-circuits the seed when a prior run with the same
// inputs has cached the output. FOTOBANK_E2E_SCALE_NO_CACHE=1 disables
// the cache; cache failures (read or write) are logged to stderr and
// the runtime falls back to a fresh seed without aborting the boot.
func seedScaleFixtures(dbPath, nasRoot string, n int, realThumbs bool) error {
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o700); err != nil {
		return fmt.Errorf("create db dir: %w", err)
	}

	opts := mediaseed.DefaultScaleOpts(n)
	cacheKey := scalecache.Key(opts, realThumbs)
	cacheOff := scaleCacheDisabled()
	if !cacheOff {
		hit, err := scalecache.Restore(cacheKey, dbPath, nasRoot, realThumbs)
		if err != nil {
			// A failed Restore can leave dbPath populated (the DB
			// copy succeeded but the hardlink walk failed) and/or
			// nasRoot half-mirrored. The fall-through reseed would
			// then hit "scale-NNNNNNN" duplicate-PK errors against
			// the partially-populated DB. Wipe both before falling
			// through so the seed sees a clean slate. nasRoot is
			// always under a per-run os.MkdirTemp dir (see e2e
			// boot path) so removal is safe.
			fmt.Fprintf(os.Stderr, "scale-cache: restore %s failed: %v; resetting and reseeding\n", cacheKey, err)
			if rmErr := os.RemoveAll(dbPath); rmErr != nil && !errors.Is(rmErr, os.ErrNotExist) {
				return fmt.Errorf("reset partial restored db: %w", rmErr)
			}
			if rmErr := os.RemoveAll(nasRoot); rmErr != nil && !errors.Is(rmErr, os.ErrNotExist) {
				return fmt.Errorf("reset partial restored nas tree: %w", rmErr)
			}
			if mkErr := os.MkdirAll(nasRoot, 0o700); mkErr != nil {
				return fmt.Errorf("recreate nas root after reset: %w", mkErr)
			}
		} else if hit {
			fmt.Fprintf(os.Stderr, "scale-cache: hit %s (n=%d real_thumbs=%t)\n", cacheKey, n, realThumbs)
			return nil
		} else {
			fmt.Fprintf(os.Stderr, "scale-cache: miss %s; seeding\n", cacheKey)
		}
	}

	d, err := db.Open(dbPath)
	if err != nil {
		return fmt.Errorf("open db: %w", err)
	}
	closeOnce := func() { _ = d.Close() }
	defer func() { closeOnce() }()

	// SeedScaleLibrary inserts the owner row itself (INSERT OR IGNORE
	// against owners.{hub,user_id}) with storage_key="550e8400-e29b-41d4-a716-446655440010";
	// override that to "550e8400-e29b-41d4-a716-44665544000e" so the SPA's stub identity (which
	// matches storage_key alice-sk per cli/server.go's stub wiring)
	// resolves to the seeded principal. We pre-insert the owner so the
	// IGNORE branch fires inside SeedScaleLibrary.
	owner := owners.Principal{Hub: e2eOwnerHub, UserID: e2eOwnerUserID}
	if _, err := d.WriteDB().ExecContext(context.Background(),
		`INSERT OR IGNORE INTO owners(hub, user_id, storage_key, created_at)
		 VALUES (?, ?, ?, ?)`,
		owner.Hub, owner.UserID, e2eOwnerStorageK, time.Now().UTC(),
	); err != nil {
		return fmt.Errorf("seed scale owner: %w", err)
	}

	ids, err := mediaseed.SeedScaleLibraryToDB(d.WriteDB(), owner, opts)
	if err != nil {
		return fmt.Errorf("seed scale library: %w", err)
	}
	if realThumbs {
		if err := writeScaleGridThumbs(nasRoot, e2eOwnerStorageK, ids); err != nil {
			return fmt.Errorf("seed scale thumbs: %w", err)
		}
	}

	// Checkpoint the WAL so the SQLite DB file we cache contains every
	// row the seed just wrote. Without this, the cached scale.db is
	// missing whatever pages still live in the .wal sidecar — Restore
	// would copy a torso of a DB and the next run would read truncated
	// data. PASSIVE checkpoint is enough since we hold the only writer
	// (no readers yet — the cli server hasn't started). TRUNCATE would
	// also work but PASSIVE doesn't churn the .wal file's metadata if
	// the OS is buffering writes.
	if _, err := d.WriteDB().ExecContext(context.Background(),
		`PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		return fmt.Errorf("checkpoint wal before cache write: %w", err)
	}
	// Close the DB before persisting so any buffered SQLite file
	// handles flush. Persist's copyFile reads dbPath; SQLite's page
	// cache holding pages back would mean we cache a stale snapshot.
	closeOnce()
	closeOnce = func() {} // disarm the deferred Close

	if !cacheOff {
		if err := scalecache.Persist(cacheKey, dbPath, nasRoot, realThumbs); err != nil {
			// Persist failures don't break the boot — the seeded
			// fixture is already on disk. The next run pays the seed
			// cost again, which is a perf regression, not a
			// correctness one.
			fmt.Fprintf(os.Stderr, "scale-cache: persist %s failed: %v\n", cacheKey, err)
		} else {
			fmt.Fprintf(os.Stderr, "scale-cache: persist %s ok\n", cacheKey)
		}
	}
	return nil
}

// writeScaleGridThumbs writes a single tiny JPEG to every row's
// SizeGrid thumb path. Sequential I/O dominates here: at 100k rows
// this takes a few seconds, well inside the playwright-e2e-scale
// webServer.timeout budget. The JPEG is encoded once and reused for
// every row — pixel data doesn't matter, only that the browser has a
// real image to decode.
func writeScaleGridThumbs(nasRoot, storageKey string, ids []string) error {
	jpg, err := smallTestJPEG()
	if err != nil {
		return fmt.Errorf("encode scale thumb: %w", err)
	}
	thumbsRoot := filepath.Join(nasRoot, storageKey, ".thumbs")
	if err := os.MkdirAll(thumbsRoot, 0o700); err != nil {
		return fmt.Errorf("mkdir thumbs root: %w", err)
	}
	for _, id := range ids {
		// Mirror thumb.ThumbKey(id, 0, SizeGrid) under the storage
		// root: <nasRoot>/<storageKey>/.thumbs/<id>/v0/grid.jpg.
		dir := filepath.Join(thumbsRoot, id, "v0")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("mkdir %s: %w", id, err)
		}
		if err := os.WriteFile(filepath.Join(dir, "grid.jpg"), jpg, 0o600); err != nil {
			return fmt.Errorf("write %s: %w", id, err)
		}
	}
	return nil
}

// seedF2_5Fixtures inserts scenario-dedicated rows for the F2.5 lightbox
// Playwright suites. The IDs are deterministic so the specs reference
// them verbatim:
//
//   - lightbox-album-30: an album with 30 visible photos. Used by
//     paginated scroll-restore (#3), deep scroll (#4), and paginated
//     source restore (#20). The album row uses a fixed ID
//     ("lightbox-album-30") inserted directly via the repo because
//     albumSvc.Create generates a UUID; tests need a stable URL path.
//   - lightbox-hidden-2: two hidden photos for hidden-grid walk +
//     unhide scenarios (#7).
//   - lightbox-select-5: five visible photos for selection-walk
//     scenarios (#5, #6) and the direct-entry tests in T21 which
//     reference lightbox-select-5-id-002.
//
// Timestamps walk backwards from a fixed base date in 1-hour increments
// so ordering is stable across runs.
func seedF2_5Fixtures(
	ctx context.Context,
	d *db.DB,
	mediaRepo *media.Repo,
	albumRepo *album.Repo,
	albumSvc *service.AlbumService,
	owner owners.Principal,
	insert func(media.Media) error,
) error {
	base := time.Date(2026, 4, 1, 12, 0, 0, 0, time.UTC)

	// Album: 30 visible photos. Insert media first, then add to the
	// fixed-ID album in one batch.
	const albumID = "lightbox-album-30"
	const albumCount = 30
	albumIDs := make([]string, 0, albumCount)
	for i := 1; i <= albumCount; i++ {
		id := fmt.Sprintf("lightbox-album-30-id-%03d", i)
		row := media.Media{
			ID:                 id,
			Owner:              owner,
			Type:               media.TypePhoto,
			MimeType:           "image/jpeg",
			DocbankVirtualPath: id + ".jpg",
			ImportedAt:         base.Add(-time.Duration(i) * time.Hour),
			Size:               1,
			SHA256:             "checksum-" + id,
			ThumbStatus:        "pending",
		}
		if err := insert(row); err != nil {
			return fmt.Errorf("seed %s: %w", id, err)
		}
		albumIDs = append(albumIDs, id)
	}
	deepAlbum := album.Album{
		ID:        albumID,
		Owner:     owner,
		Name:      "Lightbox Deep Album",
		CreatedAt: base,
		UpdatedAt: base,
	}
	if err := albumRepo.Insert(ctx, deepAlbum); err != nil {
		return fmt.Errorf("seed lightbox-album-30: %w", err)
	}
	if _, _, err := albumSvc.AddMedia(ctx, albumID, albumIDs, owner); err != nil {
		return fmt.Errorf("seed lightbox-album-30 members: %w", err)
	}

	// Hidden: two photos inserted visible, then marked hidden via direct
	// SQL — same pattern as hidden-prehidden-1.
	const hiddenCount = 2
	for i := 1; i <= hiddenCount; i++ {
		id := fmt.Sprintf("lightbox-hidden-2-id-%03d", i)
		row := media.Media{
			ID:                 id,
			Owner:              owner,
			Type:               media.TypePhoto,
			MimeType:           "image/jpeg",
			DocbankVirtualPath: id + ".jpg",
			ImportedAt:         base.Add(-time.Duration(albumCount+i) * time.Hour),
			Size:               1,
			SHA256:             "checksum-" + id,
			ThumbStatus:        "pending",
		}
		if err := insert(row); err != nil {
			return fmt.Errorf("seed %s: %w", id, err)
		}
		if _, err := d.WriteDB().ExecContext(ctx,
			`UPDATE assets SET hidden_at = ? WHERE id = ?`,
			row.ImportedAt, id,
		); err != nil {
			return fmt.Errorf("seed %s hidden_at: %w", id, err)
		}
	}

	// Selection: five visible rows. The "non-contiguous ids" requirement
	// is satisfied by the prefix — these ids don't collide with the
	// album/hidden series in the visible library, so selection walks
	// against this fixture set are isolated.
	const selectCount = 5
	for i := 1; i <= selectCount; i++ {
		id := fmt.Sprintf("lightbox-select-5-id-%03d", i)
		row := media.Media{
			ID:                 id,
			Owner:              owner,
			Type:               media.TypePhoto,
			MimeType:           "image/jpeg",
			DocbankVirtualPath: id + ".jpg",
			ImportedAt:         base.Add(-time.Duration(albumCount+hiddenCount+i) * time.Hour),
			Size:               1,
			SHA256:             "checksum-" + id,
			ThumbStatus:        "pending",
		}
		if err := insert(row); err != nil {
			return fmt.Errorf("seed %s: %w", id, err)
		}
	}

	return nil
}

// seedAIFixtures inserts media rows and ai_results / ai_failures rows
// the F4 AI Playwright spec uses. The fingerprints stamped on the
// seeded results match the active config (model id from the cfg
// heredoc, prompt versions from internal/ai/prompts, ProfileV1 from
// imginput) so the gap scanner skips them and the AI worker never
// re-processes them at e2e runtime.
//
//   - ai-fixture-tagged-1: visible photo with two active tags + an
//     active caption. Drives the lightbox tag/caption/provenance
//     assertions.
//   - ai-fixture-failed-1: visible photo with active tags but a
//     malformed caption failure row. Drives the lightbox per-photo
//     retry button + "Caption failed" copy.
//
// Both fixtures get a real preview JPEG written under nasRoot at the
// thumb storage path so a retry click triggers a real worker run that
// re-resolves the input bytes; without it the worker errors with
// "missing preview" and the malformed-caption fixture's failure row
// flips to a different kind, breaking the e2e copy assertion.
//
// FOTOBANK_E2E_AI_PRE_ACK=1 records the hidden-processing
// acknowledgement during seeding so a separate run can exercise the
// post-ack flows without driving the modal.
func seedAIFixtures(
	ctx context.Context,
	d *db.DB,
	mediaRepo *media.Repo,
	owner owners.Principal,
	nasRoot, storageKey string,
	insert func(media.Media) error,
) error {
	base := time.Date(2026, 4, 1, 12, 0, 0, 0, time.UTC)

	tagPrompt := aiprompts.Tag()
	captionPrompt := aiprompts.Caption()
	tagFP := ai.Fingerprint{
		ModelID:       e2eVisionModelID,
		PromptVersion: tagPrompt.Version,
		InputProfile:  imginput.ProfileV1,
	}
	captionFP := ai.Fingerprint{
		ModelID:       e2eVisionModelID,
		PromptVersion: captionPrompt.Version,
		InputProfile:  imginput.ProfileV1,
	}

	resultsRepo := results.NewRepo(d.WriteDB(), d.ReadDB())
	failuresRepo := failures.NewRepo(d.WriteDB(), d.ReadDB())

	// One small valid JPEG shared by both fixtures; the imginput resolver
	// just needs decodable bytes to scale and re-encode at ProfileV1's
	// 1024-edge target.
	previewJPEG, err := smallTestJPEG()
	if err != nil {
		return fmt.Errorf("encode ai fixture preview jpeg: %w", err)
	}

	// ai-fixture-tagged-1: full success.
	taggedID := "ai-fixture-tagged-1"
	if err := insert(media.Media{
		ID:                 taggedID,
		Owner:              owner,
		Type:               media.TypePhoto,
		MimeType:           "image/jpeg",
		DocbankVirtualPath: taggedID + ".jpg",
		ImportedAt:         base.Add(-time.Hour),
		Size:               1,
		SHA256:             "checksum-" + taggedID,
		ThumbStatus:        "ready",
	}); err != nil {
		return fmt.Errorf("seed %s: %w", taggedID, err)
	}
	if err := writePreviewBlob(nasRoot, storageKey, taggedID, previewJPEG); err != nil {
		return fmt.Errorf("write preview blob for %s: %w", taggedID, err)
	}
	tags := []parse.Tag{
		{Key: "e2e-tag-a", Label: "e2e-tag-a", Rank: 1},
		{Key: "e2e-tag-b", Label: "e2e-tag-b", Rank: 2},
	}
	if err := resultsRepo.WriteTagResult(ctx, taggedID, tagFP, tagPrompt.Hash, tags); err != nil {
		return fmt.Errorf("seed tag result for %s: %w", taggedID, err)
	}
	if err := resultsRepo.WriteCaptionResult(ctx, taggedID, captionFP, captionPrompt.Hash,
		"An e2e test photo of a small dog on a beach."); err != nil {
		return fmt.Errorf("seed caption result for %s: %w", taggedID, err)
	}

	// ai-fixture-failed-1: tags succeed, caption fails (malformed).
	failedID := "ai-fixture-failed-1"
	if err := insert(media.Media{
		ID:                 failedID,
		Owner:              owner,
		Type:               media.TypePhoto,
		MimeType:           "image/jpeg",
		DocbankVirtualPath: failedID + ".jpg",
		ImportedAt:         base.Add(-2 * time.Hour),
		Size:               1,
		SHA256:             "checksum-" + failedID,
		ThumbStatus:        "ready",
	}); err != nil {
		return fmt.Errorf("seed %s: %w", failedID, err)
	}
	if err := writePreviewBlob(nasRoot, storageKey, failedID, previewJPEG); err != nil {
		return fmt.Errorf("write preview blob for %s: %w", failedID, err)
	}
	if err := resultsRepo.WriteTagResult(ctx, failedID, tagFP, tagPrompt.Hash, tags); err != nil {
		return fmt.Errorf("seed tag result for %s: %w", failedID, err)
	}
	if err := failuresRepo.Record(ctx, failedID, ai.TaskCaption, captionFP,
		ai.ErrKindMalformed, "bad json", 2); err != nil {
		return fmt.Errorf("seed caption failure for %s: %w", failedID, err)
	}

	if os.Getenv("FOTOBANK_E2E_AI_PRE_ACK") == "1" {
		if err := ack.New(d.WriteDB(), d.ReadDB()).Acknowledge(ctx, owner); err != nil {
			return fmt.Errorf("seed ai acknowledgement: %w", err)
		}
	}

	return nil
}

// smallTestJPEG returns a minimal valid JPEG suitable as a stand-in
// preview blob. The mock VLM doesn't inspect pixel data, so any
// decodable JPEG works — we keep the dimensions just under ProfileV1's
// 1024 max edge so the resolver passes through without resampling.
func smallTestJPEG() ([]byte, error) {
	img := image.NewRGBA(image.Rect(0, 0, 64, 48))
	white := color.RGBA{R: 0xff, G: 0xff, B: 0xff, A: 0xff}
	for y := range 48 {
		for x := range 64 {
			img.Set(x, y, white)
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 80}); err != nil {
		return nil, fmt.Errorf("encode jpeg: %w", err)
	}
	return buf.Bytes(), nil
}

// writePreviewBlob writes the preview-tier thumbnail bytes for an AI
// fixture under <nasRoot>/<storageKey>/<thumb.ThumbKey(id, 0, preview)>.
// Bypasses storage.NewNASOnly so the seed function doesn't need to
// reconstruct a principal→storage_key map; the e2e server has exactly
// one owner.
func writePreviewBlob(nasRoot, storageKey, mediaID string, jpg []byte) error {
	key := thumb.ThumbKey(mediaID, 0, thumb.SizePreview)
	full := filepath.Join(nasRoot, storageKey, filepath.FromSlash(key))
	if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
		return fmt.Errorf("mkdir: %w", err)
	}
	if err := os.WriteFile(full, jpg, 0o600); err != nil {
		return fmt.Errorf("write %s: %w", full, err)
	}
	return nil
}

// seedSearchFixtures inserts the W1 search-suite fixtures: 30 visible
// + 5 hidden photos (search-fixture-vis-NNN, search-fixture-hid-NNN),
// active tag + caption results for each (so FTS picks them up), an
// active embedding generation matching cfg.AI.Embed, and 22/30
// media_embedding_ids mappings for the visible photos so embedding
// completeness lands at ≈73% — under the 80% banner threshold.
//
// Captions are seeded with a deterministic three-keyword pattern
// ("beach", "mountain", or "sunset") so the Playwright suite can
// search for any of them and get a non-empty result set without
// guessing which words FTS5's tokenizer will keep.
//
// The active generation row is inserted directly via SQL (rather than
// embedding.Generations.FindOrCreateBuilding + Promote) so the seed
// stamps state='active' atomically with the per-generation vec0 table.
// embedded_count is set to e2eMappedCount in lockstep with the mapping
// insertions so a downstream call to FindActive sees a consistent row.
func seedSearchFixtures(
	ctx context.Context,
	d *db.DB,
	mediaRepo *media.Repo,
	owner owners.Principal,
	insert func(media.Media) error,
) error {
	base := time.Date(2026, 4, 1, 12, 0, 0, 0, time.UTC)

	// Three caption keywords cycle across the visible set so a query
	// for any one of them returns a non-trivial slice. The cycle
	// frequency (every 3rd) means a "beach" search returns 10 of the
	// 30 visible — comfortably more than one page of cells without
	// dominating the corpus.
	keywords := []string{"beach", "mountain", "sunset"}

	// Visible photos. thumb_status='ready' so the embedding-completeness
	// query counts them as eligible. ImportedAt walks backwards from
	// base in 2-minute increments so the newest-first sort is stable
	// against the AI-fixture timestamps and the lightbox album rows.
	visibleIDs := make([]string, 0, e2eVisibleCount)
	for i := 1; i <= e2eVisibleCount; i++ {
		id := fmt.Sprintf("search-fixture-vis-%03d", i)
		visibleIDs = append(visibleIDs, id)
		if err := insert(media.Media{
			ID:                 id,
			Owner:              owner,
			Type:               media.TypePhoto,
			MimeType:           "image/jpeg",
			DocbankVirtualPath: id + ".jpg",
			ImportedAt:         base.Add(-time.Duration(i) * 2 * time.Minute),
			Size:               1,
			SHA256:             "checksum-" + id,
			ThumbStatus:        "ready",
		}); err != nil {
			return fmt.Errorf("seed %s: %w", id, err)
		}
		kw := keywords[(i-1)%len(keywords)]
		caption := fmt.Sprintf("A %s photo numbered %d.", kw, i)
		if err := writeSearchAIResults(ctx, d, id, kw, caption); err != nil {
			return fmt.Errorf("seed ai results for %s: %w", id, err)
		}
	}

	// Hidden photos. Inserted visible, then flipped via direct SQL
	// (same pattern as hidden-prehidden-1 above). Each hidden row gets
	// a tag/caption with a marker keyword "hiddencache" so a search
	// for that string verifies the hidden gate excludes them by
	// default.
	for i := 1; i <= e2eHiddenCount; i++ {
		id := fmt.Sprintf("search-fixture-hid-%03d", i)
		if err := insert(media.Media{
			ID:                 id,
			Owner:              owner,
			Type:               media.TypePhoto,
			MimeType:           "image/jpeg",
			DocbankVirtualPath: id + ".jpg",
			ImportedAt:         base.Add(-time.Duration(e2eVisibleCount+i) * 2 * time.Minute),
			Size:               1,
			SHA256:             "checksum-" + id,
			ThumbStatus:        "ready",
		}); err != nil {
			return fmt.Errorf("seed %s: %w", id, err)
		}
		if err := writeSearchAIResults(
			ctx, d, id, "hiddencache",
			fmt.Sprintf("A hiddencache photo numbered %d.", i),
		); err != nil {
			return fmt.Errorf("seed ai results for %s: %w", id, err)
		}
		if _, err := d.WriteDB().ExecContext(ctx,
			`UPDATE assets SET hidden_at = ? WHERE id = ?`, base, id,
		); err != nil {
			return fmt.Errorf("hide %s: %w", id, err)
		}
	}

	// Active embedding generation. The id is auto-incremented; we
	// derive the vec_table_name from it after the insert. Inserting
	// state='active' directly relies on the partial unique index
	// embedding_generations_one_active still permitting the row when
	// no other active row exists (which is the case at seed time —
	// this is the very first generation).
	fp := embedding.Fingerprint(ai.EmbedConfig{
		Model:     e2eEmbedModelID,
		InputEdge: e2eEmbedEdge,
	})
	res, err := d.WriteDB().ExecContext(ctx,
		`INSERT INTO embedding_generations
		   (fingerprint, fingerprint_hash, model_id, input_profile, vec_table_name,
		    dimension, state, embedded_count, threshold_pct, created_at, activated_at)
		 VALUES (?, ?, ?, ?, '', ?, 'active', ?, ?, ?, ?)`,
		fp.String(), embeddingFingerprintHash(fp),
		e2eEmbedModelID, e2eEmbedProfile,
		e2eEmbedDim, e2eMappedCount, 95, base, base,
	)
	if err != nil {
		return fmt.Errorf("insert active generation: %w", err)
	}
	genID, err := res.LastInsertId()
	if err != nil {
		return fmt.Errorf("active generation id: %w", err)
	}
	vecTable := fmt.Sprintf("media_embeddings_g%d", genID)
	if _, err := d.WriteDB().ExecContext(ctx,
		`UPDATE embedding_generations SET vec_table_name = ? WHERE id = ?`,
		vecTable, genID,
	); err != nil {
		return fmt.Errorf("set active gen vec_table_name: %w", err)
	}
	if _, err := d.WriteDB().ExecContext(ctx,
		fmt.Sprintf(
			`CREATE VIRTUAL TABLE %s USING vec0(vec_id INTEGER PRIMARY KEY, embedding FLOAT[%d])`,
			vecTable, e2eEmbedDim,
		),
	); err != nil {
		return fmt.Errorf("create vec0 table: %w", err)
	}

	// Mappings: 22 of 30 visible photos. Ordered by the visibleIDs
	// slice so the unmapped 8 are deterministic ("vis-023" through
	// "vis-030"). vec_id starts at 1 and walks up by 1 per row.
	for i := range e2eMappedCount {
		mediaID := visibleIDs[i]
		vecID := int64(i + 1)
		if _, err := d.WriteDB().ExecContext(ctx,
			`INSERT INTO media_embedding_ids (generation_id, media_id, vec_id) VALUES (?, ?, ?)`,
			genID, mediaID, vecID,
		); err != nil {
			return fmt.Errorf("insert mapping for %s: %w", mediaID, err)
		}
		blob := vecToBlob(deterministicVec(i, e2eEmbedDim))
		if _, err := d.WriteDB().ExecContext(ctx,
			fmt.Sprintf(`INSERT INTO %s (vec_id, embedding) VALUES (?, vec_f32(?))`, vecTable),
			vecID, blob,
		); err != nil {
			return fmt.Errorf("insert vec0 row for %s: %w", mediaID, err)
		}
	}

	return nil
}

// writeSearchAIResults seeds an active tag + caption for mediaID and
// refreshes the FTS row in the same tx. The fingerprint matches the
// e2e VLM mock's stamping so the gap scanner will skip these rows. The
// keyword goes into both the tag label and the caption text so a
// search query against either field surfaces this photo.
func writeSearchAIResults(ctx context.Context, d *db.DB, mediaID, keyword, caption string) error {
	tagPrompt := aiprompts.Tag()
	captionPrompt := aiprompts.Caption()
	tagFP := ai.Fingerprint{
		ModelID:       e2eVisionModelID,
		PromptVersion: tagPrompt.Version,
		InputProfile:  imginput.ProfileV1,
	}
	captionFP := ai.Fingerprint{
		ModelID:       e2eVisionModelID,
		PromptVersion: captionPrompt.Version,
		InputProfile:  imginput.ProfileV1,
	}
	resultsRepo := results.NewRepo(d.WriteDB(), d.ReadDB())
	tagRows := []parse.Tag{{Key: keyword, Label: keyword, Rank: 1}}
	if err := resultsRepo.WriteTagResult(ctx, mediaID, tagFP, tagPrompt.Hash, tagRows); err != nil {
		return fmt.Errorf("write tag result: %w", err)
	}
	if err := resultsRepo.WriteCaptionResult(ctx, mediaID, captionFP, captionPrompt.Hash, caption); err != nil {
		return fmt.Errorf("write caption result: %w", err)
	}
	// Both WriteTagResult and WriteCaptionResult refresh media_fts
	// inside their own tx (see internal/ai/results/repo.go), so the
	// FTS row is already in lockstep with the tag/caption writes by
	// the time we return. No follow-up RefreshMediaFTS needed.
	return nil
}

// embeddingFingerprintHash mirrors the unexported hash function in
// internal/ai/embedding/generations.go (sha256-hex of fp.String()).
// Re-derived here instead of exporting the internal so the seed doesn't
// force a public surface change just to satisfy the e2e harness; the
// duplication is a few lines of stdlib calls.
func embeddingFingerprintHash(fp ai.Fingerprint) string {
	sum := sha256.Sum256([]byte(fp.String()))
	return hex.EncodeToString(sum[:])
}

// vecToBlob mirrors embedding.VecToBlob — the seeded mappings need the
// same little-endian float32 packing the runtime expects. (Reproduced
// here rather than imported because embedding.VecToBlob is exported
// for the read side of search and the seed lives outside that path;
// the duplication is a few lines of stdlib calls.)
func vecToBlob(vec []float32) []byte {
	buf := make([]byte, 4*len(vec))
	for i, v := range vec {
		binary.LittleEndian.PutUint32(buf[i*4:], math.Float32bits(v))
	}
	return buf
}

// seedFacetFixtures inserts the SF-20 sidebar-facet fixtures: 10 rows
// across 3 cameras / 2 lenses / a "dog" + "cat" tag distribution / a
// mixed GPS posture / one video. Counts by group:
//
//   - Cameras:    Sony A7R IV (5), Canon EOS R5 (3), Apple iPhone 15 Pro (2)
//   - Lenses:     FE 24-70mm F2.8 GM (5), RF 50mm F1.2 L USM (3)
//   - Tag "dog":  3 rows;  Tag "cat":  2 rows
//   - GPS:        4 rows;  No GPS:     6 rows
//   - Photo:      9 rows;  Video:      1 row
//
// IDs use a "facet-fixture-" prefix so they don't collide with the
// existing search/hidden/album/AI fixtures, and ImportedAt walks
// backwards from a base date that's distinct from those fixtures so
// the newest-first sort stays deterministic.
func seedFacetFixtures(
	ctx context.Context,
	d *db.DB,
	mediaRepo *media.Repo,
	owner owners.Principal,
	insert func(media.Media) error,
) error {
	base := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	type facetSeed struct {
		id        string
		mediaType media.Type
		make      string
		model     string
		lens      string
		tag       string // "" → no tag
		gps       bool
		caption   string
	}
	type geoLabel struct {
		lat, lon float64
		label    string
	}
	// Rotate GPS coordinates so the geotagged rows land at distinct
	// locations — useful for /map and Places facet assertions, and
	// avoids any LocationLabel collisions with the search fixtures.
	gpsCoords := []geoLabel{
		{48.8566, 2.3522, "Paris, Île-de-France, France"},
		{40.7128, -74.0060, "New York, New York, USA"},
		{37.7749, -122.4194, "San Francisco, California, USA"},
		{51.5074, -0.1278, "London, England, United Kingdom"},
	}

	rows := []facetSeed{
		{"facet-fixture-sony-1", media.TypePhoto, "Sony", "A7R IV", "FE 24-70mm F2.8 GM", "dog", false, "A dog playing"},
		{"facet-fixture-sony-2", media.TypePhoto, "Sony", "A7R IV", "FE 24-70mm F2.8 GM", "dog", true, "A dog with city background"},
		{"facet-fixture-sony-3", media.TypePhoto, "Sony", "A7R IV", "FE 24-70mm F2.8 GM", "cat", false, "A cat sleeping"},
		{"facet-fixture-sony-4", media.TypePhoto, "Sony", "A7R IV", "FE 24-70mm F2.8 GM", "", true, "A landscape photo"},
		{"facet-fixture-sony-5", media.TypeVideo, "Sony", "A7R IV", "FE 24-70mm F2.8 GM", "", false, "A short video clip"},
		{"facet-fixture-canon-1", media.TypePhoto, "Canon", "EOS R5", "RF 50mm F1.2 L USM", "dog", true, "A dog portrait"},
		{"facet-fixture-canon-2", media.TypePhoto, "Canon", "EOS R5", "RF 50mm F1.2 L USM", "cat", false, "A cat closeup"},
		{"facet-fixture-canon-3", media.TypePhoto, "Canon", "EOS R5", "RF 50mm F1.2 L USM", "", false, "A still life"},
		{"facet-fixture-iphone-1", media.TypePhoto, "Apple", "iPhone 15 Pro", "", "", true, "A street photo"},
		{"facet-fixture-iphone-2", media.TypePhoto, "Apple", "iPhone 15 Pro", "", "", false, "A food photo"},
	}

	mimeFor := func(t media.Type) string {
		if t == media.TypeVideo {
			return "video/mp4"
		}
		return "image/jpeg"
	}
	pathFor := func(id string, t media.Type) string {
		if t == media.TypeVideo {
			return id + ".mp4"
		}
		return id + ".jpg"
	}

	gpsIdx := 0
	for i, fs := range rows {
		row := media.Media{
			ID:                 fs.id,
			Owner:              owner,
			Type:               fs.mediaType,
			MimeType:           mimeFor(fs.mediaType),
			DocbankVirtualPath: pathFor(fs.id, fs.mediaType),
			ImportedAt:         base.Add(-time.Duration(i) * time.Minute),
			Size:               1,
			SHA256:             "checksum-" + fs.id,
			Make:               fs.make,
			Model:              fs.model,
			LensModel:          fs.lens,
			// thumb_status='pending' so these rows don't perturb the
			// search-suite's embedding-completeness assertions, which
			// pin against an exact (22 / 32) ratio assuming only the W1
			// search fixtures + 2 AI fixtures count toward the
			// eligible denominator. Facets resolve off media columns
			// (make/model/lens) and AI tag/caption rows, neither of
			// which depends on thumb_status, so the facet e2e tests
			// don't care.
			ThumbStatus: "pending",
		}
		if fs.gps {
			g := gpsCoords[gpsIdx]
			row.Latitude = &g.lat
			row.Longitude = &g.lon
			row.LocationLabel = g.label
			gpsIdx = (gpsIdx + 1) % len(gpsCoords)
		}
		if err := insert(row); err != nil {
			return fmt.Errorf("seed %s: %w", fs.id, err)
		}
		if fs.tag != "" {
			if err := writeSearchAIResults(ctx, d, fs.id, fs.tag, fs.caption); err != nil {
				return fmt.Errorf("seed ai results for %s: %w", fs.id, err)
			}
		}
	}
	return nil
}
