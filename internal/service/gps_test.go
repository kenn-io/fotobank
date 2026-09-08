package service_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"go.kenn.io/fotobank/internal/media"
	"go.kenn.io/fotobank/internal/service"
	"go.kenn.io/fotobank/internal/testutil"
	"go.kenn.io/fotobank/internal/testutil/assetfixture"
)

type gpsPlaceFunc func(float64, float64) (string, bool)

func (f gpsPlaceFunc) Resolve(lat, lon float64) (string, bool) { return f(lat, lon) }

func TestGPSBackfillStopsOnCancellation(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	owner := testutil.SeedOwner(t, d.WriteDB(), "h", "u")
	repo := media.NewRepo(d.WriteDB(), d.ReadDB())
	for range 2 {
		assetfixture.Insert(t, repo, media.Media{Owner: owner, Type: media.TypePhoto, MimeType: "image/jpeg", ImportedAt: time.Now().UTC(), Latitude: new(48.8566), Longitude: new(2.3522), LocationLabel: "original", ThumbStatus: "pending"})
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	places := gpsPlaceFunc(func(float64, float64) (string, bool) { cancel(); return "changed", true })
	svc := service.NewGPSService(d, nil, nil, places)
	result, err := svc.Backfill(ctx, owner, service.GPSOptions{Mode: media.GPSBackfillModeRelabel})
	r.ErrorIs(err, context.Canceled)
	r.Zero(result.Processed)
	r.Zero(result.Failed, "canceled and unstarted work is not a photo failure")
	var changed int
	r.NoError(d.ReadDB().QueryRowContext(t.Context(), `SELECT COUNT(*) FROM assets WHERE location_label <> 'original'`).Scan(&changed))
	r.Zero(changed)
}

func TestGPSBackfillPreservesNewerVersion(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	owner := testutil.SeedOwner(t, d.WriteDB(), "h", "u")
	repo := media.NewRepo(d.WriteDB(), d.ReadDB())
	row := assetfixture.Insert(t, repo, media.Media{Owner: owner, Type: media.TypePhoto, MimeType: "image/jpeg", ImportedAt: time.Now().UTC(), Latitude: new(48.8566), Longitude: new(2.3522), LocationLabel: "original", ThumbStatus: "pending"})
	places := gpsPlaceFunc(func(float64, float64) (string, bool) {
		_, err := d.WriteDB().ExecContext(t.Context(), `UPDATE media_files SET current_version_id='550e8400-e29b-41d4-a716-446655440001' WHERE id=?`, row.PrimaryFileID)
		r.NoError(err)
		return "stale label", true
	})
	svc := service.NewGPSService(d, nil, nil, places)
	result, err := svc.Backfill(t.Context(), owner, service.GPSOptions{Mode: media.GPSBackfillModeRelabel})
	r.Error(err)
	r.Equal(1, result.Processed)
	r.Equal(1, result.Failed)
	r.Zero(result.Updated)
	r.Len(result.Failures, 1)
	r.Equal(row.ID, result.Failures[0].ID)
	current, err := repo.GetByID(t.Context(), row.ID)
	r.NoError(err)
	r.Equal("original", current.LocationLabel)
}
