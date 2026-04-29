// Command e2e-server boots a fotobank server against a freshly created
// temp-file SQLite database so Playwright can run smoke tests against
// the embedded SPA. It writes a minimal TOML config under a temp dir,
// then delegates to internal/cli the same way the production fotobank
// binary does.
//
// The server listens on 127.0.0.1:8080 deterministically so the
// Playwright webServer config in the frontend package can target a
// fixed URL.
package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/wesm/fotobank/internal/album"
	"github.com/wesm/fotobank/internal/cli"
	"github.com/wesm/fotobank/internal/db"
	"github.com/wesm/fotobank/internal/media"
	"github.com/wesm/fotobank/internal/owners"
	"github.com/wesm/fotobank/internal/service"
	"github.com/wesm/fotobank/internal/share"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
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
listen_address = "127.0.0.1:8080"
[imports]
file_lock_path = "%s"
[backup]
enabled = false
[observability]
admin_listen = "127.0.0.1:0"
`, nasRoot, flashRoot, filepath.Join(tmp, "import.lock"))
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		return fmt.Errorf("writing config: %w", err)
	}

	// Seed deterministic media rows so the Playwright MediaDetail GPS
	// tests can navigate to known IDs without env-var plumbing. The
	// server reopens the DB on startup; SQLite + golang-migrate
	// migrations are idempotent, so this is safe.
	dbPath := filepath.Join(flashRoot, "fotobank.sqlite")
	if err := seedFixtures(dbPath); err != nil {
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

// seedFixtures inserts an owner row and four media rows with deterministic
// IDs. The IDs are referenced verbatim by the Playwright MediaDetail GPS
// tests and the F2.2 sidecar tests in frontend/tests/e2e/library.spec.ts.
func seedFixtures(dbPath string) error {
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
	return nil
}
