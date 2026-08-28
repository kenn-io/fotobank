package assetfixture

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"go.kenn.io/fotobank/internal/content"
	"go.kenn.io/fotobank/internal/media"
)

var syntheticNodeSequence atomic.Int64

// Insert adds a ready asset with a synthetic, structurally valid Docbank
// mapping. Tests that read original bytes should use InsertContent instead.
func Insert(t testing.TB, repo *media.Repo, item media.Media) media.Media {
	t.Helper()
	return insert(t, repo, nil, nil, item)
}

// InsertContent adds a ready asset whose exact version exists in store.
func InsertContent(t testing.TB, repo *media.Repo, store *content.Adapter, body []byte, item media.Media) media.Media {
	t.Helper()
	return insert(t, repo, store, body, item)
}

func insert(t testing.TB, repo *media.Repo, store *content.Adapter, body []byte, item media.Media) media.Media {
	t.Helper()
	if item.ID == "" {
		item.ID = uuid.NewString()
	}
	if item.Type == "" {
		item.Type = media.TypePhoto
	}
	if item.MimeType == "" {
		item.MimeType = "image/jpeg"
	}
	if item.OriginalFilename == "" {
		item.OriginalFilename = item.ID + ".jpg"
	}
	if item.ImportedAt.IsZero() {
		item.ImportedAt = time.Now().UTC()
	}
	if item.ThumbStatus == "" {
		item.ThumbStatus = "pending"
	}
	if body == nil {
		body = []byte(item.ID)
	}
	digest := sha256.Sum256(body)
	item.SHA256 = hex.EncodeToString(digest[:])
	item.Size = int64(len(body))
	fileID := uuid.NewString()
	versionID := uuid.NewString()
	// Keep synthetic mappings outside Docbank's ordinary low, sequential ID
	// range. An atomic sequence is unique even on platforms whose wall clock
	// returns the same nanosecond value to concurrent tests.
	var nodeID = int64(1<<62) + syntheticNodeSequence.Add(1)

	err := repo.WithWriteTx(context.Background(), func(tx *sql.Tx) error {
		var storageKey string
		if err := tx.QueryRow(`SELECT storage_key FROM owners WHERE hub=? AND user_id=?`,
			item.Owner.Hub, item.Owner.UserID).Scan(&storageKey); err != nil {
			return err
		}
		virtualPath, err := content.VirtualPath(storageKey, fileID, item.OriginalFilename)
		if err != nil {
			return err
		}
		if store != nil {
			receipt, err := store.Create(context.Background(), content.CreateRequest{
				VirtualPath: virtualPath, MediaType: item.MimeType,
				Expected: content.Identity{SHA256: item.SHA256, Size: item.Size},
				Reader:   bytes.NewReader(body),
			})
			if err != nil {
				return err
			}
			nodeID, versionID = receipt.Node.ID, receipt.Version.ID
		}
		item.PrimaryFileID, item.CurrentVersionID, item.DocbankVirtualPath = fileID, versionID, virtualPath
		_, err = tx.Exec(`INSERT INTO assets (
			id, owner_hub, owner_user_id, state, media_type, imported_at, timestamp,
			make, model, lens_model, focal_length, shutter, width, height, iso, aperture,
			duration_ms, latitude, longitude, gps_at, location_label,
			thumb_status, thumb_version, thumb_updated_at, hidden_at
		) VALUES (?,?,?,'pending',?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			item.ID, item.Owner.Hub, item.Owner.UserID, item.Type, item.ImportedAt, item.Timestamp,
			item.Make, item.Model, item.LensModel, item.FocalLength, item.Shutter,
			item.Width, item.Height, item.ISO, item.Aperture, item.DurationMs,
			item.Latitude, item.Longitude, item.GPSAt, item.LocationLabel,
			item.ThumbStatus, item.ThumbVersion, item.ThumbUpdatedAt, item.HiddenAt)
		if err != nil {
			return err
		}
		if _, err = tx.Exec(`INSERT INTO media_files (
			id, asset_id, owner_hub, owner_user_id, role, mime_type, original_filename,
			size, docbank_node_id, docbank_virtual_path, current_version_id, sha256
		) VALUES (?,?,?,?, 'primary',?,?,?,?,?,?,?)`,
			fileID, item.ID, item.Owner.Hub, item.Owner.UserID, item.MimeType,
			item.OriginalFilename, item.Size, nodeID, virtualPath, versionID, item.SHA256); err != nil {
			return err
		}
		_, err = tx.Exec(`UPDATE assets SET state='ready' WHERE id=?`, item.ID)
		return err
	})
	require.NoError(t, err)
	return item
}
