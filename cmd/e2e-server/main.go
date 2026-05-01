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
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/wesm/fotobank/internal/ai"
	"github.com/wesm/fotobank/internal/ai/ack"
	"github.com/wesm/fotobank/internal/ai/failures"
	"github.com/wesm/fotobank/internal/ai/imginput"
	"github.com/wesm/fotobank/internal/ai/parse"
	aiprompts "github.com/wesm/fotobank/internal/ai/prompts"
	"github.com/wesm/fotobank/internal/ai/results"
	"github.com/wesm/fotobank/internal/album"
	"github.com/wesm/fotobank/internal/auth/hidden"
	"github.com/wesm/fotobank/internal/cli"
	"github.com/wesm/fotobank/internal/db"
	"github.com/wesm/fotobank/internal/media"
	"github.com/wesm/fotobank/internal/owners"
	"github.com/wesm/fotobank/internal/service"
	"github.com/wesm/fotobank/internal/share"
	"github.com/wesm/fotobank/internal/thumb"
)

// e2eVisionModelID matches the [ai.tag] / [ai.caption] model entries
// the cfg heredoc writes — the AI fingerprints persisted on seeded
// results must use the same model id so the gap scanner skips them.
const e2eVisionModelID = "qwen2.5-vl:3b"

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
	for _, d := range []string{nasRoot, flashRoot} {
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

	cfg := fmt.Sprintf(`
[nas]
root = "%s"
[flash]
root = "%s"
[identity]
mode = "stub"
[identity.stub]
hub = "local"
user_id = "alice"
handle = "Alice"
storage_key = "alice-sk"
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
`, nasRoot, flashRoot, e2ePort(), filepath.Join(tmp, "import.lock"),
		vlmURL, e2eVisionModelID, e2eVisionModelID)
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		return fmt.Errorf("writing config: %w", err)
	}

	// Seed deterministic media rows so the Playwright MediaDetail GPS
	// tests can navigate to known IDs without env-var plumbing. The
	// server reopens the DB on startup; SQLite + golang-migrate
	// migrations are idempotent, so this is safe.
	dbPath := filepath.Join(flashRoot, "fotobank.sqlite")
	if err := seedFixtures(dbPath, nasRoot); err != nil {
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

// seedFixtures inserts an owner row and deterministic media rows used by
// the Playwright suite. The IDs are referenced verbatim by tests in
// frontend/tests/e2e/*.spec.ts.
//
// Hidden fixtures:
//   - auth_hidden_credential for the stub principal with passcode "e2e-passcode"
//     (Argon2id-hashed). Skipped when FOTOBANK_E2E_HIDDEN_UNCONFIGURED=1.
//   - hidden-prehidden-1: hidden_at = now (used by gate/grid tests)
//   - hidden-target-1:    hidden_at = NULL (used by hide-flow test)
func seedFixtures(dbPath, nasRoot string) error {
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
		owner.Hub, owner.UserID, "alice-sk", time.Now().UTC(),
	); err != nil {
		return fmt.Errorf("seed owner: %w", err)
	}

	repo := media.NewRepo(d.WriteDB(), d.ReadDB())
	lat, lon := 48.8566, 2.3522
	now := time.Now().UTC()
	gpsRow := media.Media{
		ID:            "gps-fixture-1",
		Owner:         owner,
		Type:          media.TypePhoto,
		MimeType:      "image/jpeg",
		Path:          "gps-fixture-1.jpg",
		ImportedAt:    now,
		Size:          1,
		Checksum:      "checksum-gps-fixture-1",
		Latitude:      &lat,
		Longitude:     &lon,
		LocationLabel: "Paris, Île-de-France, France",
		ThumbStatus:   "pending",
	}
	if err := repo.Insert(ctx, gpsRow); err != nil {
		return fmt.Errorf("seed gps fixture: %w", err)
	}
	noGPSRow := media.Media{
		ID:          "no-gps-fixture-1",
		Owner:       owner,
		Type:        media.TypePhoto,
		MimeType:    "image/jpeg",
		Path:        "no-gps-fixture-1.jpg",
		ImportedAt:  now,
		Size:        1,
		Checksum:    "checksum-no-gps-fixture-1",
		ThumbStatus: "pending",
	}
	if err := repo.Insert(ctx, noGPSRow); err != nil {
		return fmt.Errorf("seed no-gps fixture: %w", err)
	}

	// F2.2 RAW + JPEG pairing fixtures. The primary JPEG and a DNG
	// sidecar pointing at it via paired_with_id; referenced by the
	// Playwright tests that exercise the Files row, sidecar direct
	// page, and library list filtering. Insert the primary first so
	// the media_paired_with_owner_consistency_insert trigger can
	// resolve the FK owner.
	primaryRow := media.Media{
		ID:               "pair-fixture-primary",
		Owner:            owner,
		Type:             media.TypePhoto,
		MimeType:         "image/jpeg",
		Path:             "pair-fixture-primary.jpg",
		OriginalFilename: "IMG_1.JPG",
		ImportedAt:       now,
		Size:             1,
		Checksum:         "checksum-pair-fixture-primary",
		ImportSourcePath: "fixtures/IMG_1.JPG",
		ThumbStatus:      "pending",
	}
	if err := repo.Insert(ctx, primaryRow); err != nil {
		return fmt.Errorf("seed pair fixture primary: %w", err)
	}
	primaryID := primaryRow.ID
	sidecarRow := media.Media{
		ID:               "pair-fixture-sidecar",
		Owner:            owner,
		Type:             media.TypePhoto,
		MimeType:         "image/x-adobe-dng",
		Path:             "pair-fixture-sidecar.dng",
		OriginalFilename: "IMG_1.DNG",
		ImportedAt:       now,
		Size:             1,
		Checksum:         "checksum-pair-fixture-sidecar",
		ImportSourcePath: "fixtures/IMG_1.DNG",
		PairedWithID:     &primaryID,
		ThumbStatus:      "pending",
	}
	if err := repo.Insert(ctx, sidecarRow); err != nil {
		return fmt.Errorf("seed pair fixture sidecar: %w", err)
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

	// F2.4 hidden privacy fixtures.

	// hidden-prehidden-1: already hidden at seed time — used by gate-render
	// and /hidden grid tests (the grid must show this row when unlocked).
	prehidden := media.Media{
		ID:          "hidden-prehidden-1",
		Owner:       owner,
		Type:        media.TypePhoto,
		MimeType:    "image/jpeg",
		Path:        "hidden-prehidden-1.jpg",
		ImportedAt:  now,
		Size:        1,
		Checksum:    "checksum-hidden-prehidden-1",
		ThumbStatus: "pending",
	}
	if err := repo.Insert(ctx, prehidden); err != nil {
		return fmt.Errorf("seed hidden-prehidden-1: %w", err)
	}
	if _, err := d.WriteDB().ExecContext(ctx,
		`UPDATE media SET hidden_at = ? WHERE id = ?`, now, "hidden-prehidden-1",
	); err != nil {
		return fmt.Errorf("seed hidden-prehidden-1 hidden_at: %w", err)
	}

	// hidden-target-1: visible — used by the hide-flow test which triggers
	// the hide action via the UI (exercising the cascade and store paths).
	target := media.Media{
		ID:          "hidden-target-1",
		Owner:       owner,
		Type:        media.TypePhoto,
		MimeType:    "image/jpeg",
		Path:        "hidden-target-1.jpg",
		ImportedAt:  now,
		Size:        1,
		Checksum:    "checksum-hidden-target-1",
		ThumbStatus: "pending",
	}
	if err := repo.Insert(ctx, target); err != nil {
		return fmt.Errorf("seed hidden-target-1: %w", err)
	}

	// hidden-cascade-primary + hidden-cascade-sidecar: a dedicated pair for
	// the sidecar-cascade e2e test so hiding the primary doesn't contaminate
	// the shared pair-fixture-primary/sidecar fixtures used by other tests.
	cascadePrimary := media.Media{
		ID:               "hidden-cascade-primary",
		Owner:            owner,
		Type:             media.TypePhoto,
		MimeType:         "image/jpeg",
		Path:             "hidden-cascade-primary.jpg",
		OriginalFilename: "HIDDEN_C1.JPG",
		ImportedAt:       now,
		Size:             1,
		Checksum:         "checksum-hidden-cascade-primary",
		ImportSourcePath: "fixtures/HIDDEN_C1.JPG",
		ThumbStatus:      "pending",
	}
	if err := repo.Insert(ctx, cascadePrimary); err != nil {
		return fmt.Errorf("seed hidden-cascade-primary: %w", err)
	}
	cascadePrimaryID := cascadePrimary.ID
	cascadeSidecar := media.Media{
		ID:               "hidden-cascade-sidecar",
		Owner:            owner,
		Type:             media.TypePhoto,
		MimeType:         "image/x-adobe-dng",
		Path:             "hidden-cascade-sidecar.dng",
		OriginalFilename: "HIDDEN_C1.DNG",
		ImportedAt:       now,
		Size:             1,
		Checksum:         "checksum-hidden-cascade-sidecar",
		ImportSourcePath: "fixtures/HIDDEN_C1.DNG",
		PairedWithID:     &cascadePrimaryID,
		ThumbStatus:      "pending",
	}
	if err := repo.Insert(ctx, cascadeSidecar); err != nil {
		return fmt.Errorf("seed hidden-cascade-sidecar: %w", err)
	}

	// hidden-album-target-1: visible, added to the Italy album — used by the
	// album hidden_count chip test so hiding this doesn't contaminate the
	// gps-fixture-1 fixture that the shares test relies on.
	albumTarget := media.Media{
		ID:          "hidden-album-target-1",
		Owner:       owner,
		Type:        media.TypePhoto,
		MimeType:    "image/jpeg",
		Path:        "hidden-album-target-1.jpg",
		ImportedAt:  now,
		Size:        1,
		Checksum:    "checksum-hidden-album-target-1",
		ThumbStatus: "pending",
	}
	if err := repo.Insert(ctx, albumTarget); err != nil {
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
		ID:          "hidden-share-member-1",
		Owner:       owner,
		Type:        media.TypePhoto,
		MimeType:    "image/jpeg",
		Path:        "hidden-share-member-1.jpg",
		ImportedAt:  now,
		Size:        1,
		Checksum:    "checksum-hidden-share-member-1",
		ThumbStatus: "pending",
	}
	if err := repo.Insert(ctx, shareMember); err != nil {
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

	if err := seedF2_5Fixtures(ctx, d, repo, albumRepo, albumSvc, owner); err != nil {
		return fmt.Errorf("seed f2.5 fixtures: %w", err)
	}

	if err := seedAIFixtures(ctx, d, repo, owner, nasRoot, "alice-sk"); err != nil {
		return fmt.Errorf("seed ai fixtures: %w", err)
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
			ID:          id,
			Owner:       owner,
			Type:        media.TypePhoto,
			MimeType:    "image/jpeg",
			Path:        id + ".jpg",
			ImportedAt:  base.Add(-time.Duration(i) * time.Hour),
			Size:        1,
			Checksum:    "checksum-" + id,
			ThumbStatus: "pending",
		}
		if err := mediaRepo.Insert(ctx, row); err != nil {
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
			ID:          id,
			Owner:       owner,
			Type:        media.TypePhoto,
			MimeType:    "image/jpeg",
			Path:        id + ".jpg",
			ImportedAt:  base.Add(-time.Duration(albumCount+i) * time.Hour),
			Size:        1,
			Checksum:    "checksum-" + id,
			ThumbStatus: "pending",
		}
		if err := mediaRepo.Insert(ctx, row); err != nil {
			return fmt.Errorf("seed %s: %w", id, err)
		}
		if _, err := d.WriteDB().ExecContext(ctx,
			`UPDATE media SET hidden_at = ? WHERE id = ?`,
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
			ID:          id,
			Owner:       owner,
			Type:        media.TypePhoto,
			MimeType:    "image/jpeg",
			Path:        id + ".jpg",
			ImportedAt:  base.Add(-time.Duration(albumCount+hiddenCount+i) * time.Hour),
			Size:        1,
			Checksum:    "checksum-" + id,
			ThumbStatus: "pending",
		}
		if err := mediaRepo.Insert(ctx, row); err != nil {
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
	if err := mediaRepo.Insert(ctx, media.Media{
		ID:          taggedID,
		Owner:       owner,
		Type:        media.TypePhoto,
		MimeType:    "image/jpeg",
		Path:        taggedID + ".jpg",
		ImportedAt:  base.Add(-time.Hour),
		Size:        1,
		Checksum:    "checksum-" + taggedID,
		ThumbStatus: "ready",
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
	if err := mediaRepo.Insert(ctx, media.Media{
		ID:          failedID,
		Owner:       owner,
		Type:        media.TypePhoto,
		MimeType:    "image/jpeg",
		Path:        failedID + ".jpg",
		ImportedAt:  base.Add(-2 * time.Hour),
		Size:        1,
		Checksum:    "checksum-" + failedID,
		ThumbStatus: "ready",
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
