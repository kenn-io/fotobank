package album_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"go.kenn.io/fotobank/internal/album"
	"go.kenn.io/fotobank/internal/errs"
	"go.kenn.io/fotobank/internal/owners"
	"go.kenn.io/fotobank/internal/testutil"
)

// TestAddMediaCrossOwnerTrigger verifies the defence-in-depth path: if
// the service's pre-flight is bypassed and cross-owner IDs reach the
// repo, the SQLite trigger aborts the insert and the repo wraps the
// raw error as errs.ErrOwnerMismatch so errors.Is matches.
func TestAddMediaCrossOwnerTrigger(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	repo := album.NewRepo(d.WriteDB(), d.ReadDB())

	ownerA := owners.Principal{Hub: "h", UserID: "a"}
	ownerB := owners.Principal{Hub: "h", UserID: "b"}
	seedOwner(t, d.WriteDB(), ownerA, "550e8400-e29b-41d4-a716-446655440001")
	seedOwner(t, d.WriteDB(), ownerB, "550e8400-e29b-41d4-a716-446655440002")

	a := seedAlbum(t, repo, ownerA, "A-Trip") // album belongs to A
	mB := uuid.NewString()
	seedMediaRow(t, d.WriteDB(), ownerB, mB, "cs-b", "ready", 1) // media belongs to B

	_, _, err := repo.AddMedia(context.Background(), a.ID, []string{mB}, time.Now().UTC())
	r.Error(err)
	r.ErrorIs(err, errs.ErrOwnerMismatch)
}
