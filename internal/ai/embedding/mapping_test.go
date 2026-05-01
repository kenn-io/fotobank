package embedding_test

import (
	"context"
	"encoding/binary"
	"fmt"
	"math"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wesm/fotobank/internal/ai"
	"github.com/wesm/fotobank/internal/ai/embedding"
	"github.com/wesm/fotobank/internal/db"
	"github.com/wesm/fotobank/internal/testutil"
)

// mockVec returns a deterministic float32 vector of length dim. Each
// element is a small index-derived float so vector comparisons in
// tests stay readable when something goes wrong.
func mockVec(dim int) []float32 {
	out := make([]float32, dim)
	for i := range out {
		out[i] = float32(i) * 0.001
	}
	return out
}

// mustCreateBuildingGen drives the E1 Generations repo to insert a
// fresh building generation row plus its per-generation vec0 table.
// Using a unique fingerprint per call keeps every test independent.
func mustCreateBuildingGen(t *testing.T, d *db.DB, dim int) embedding.Row {
	t.Helper()
	g := embedding.NewGenerations(d.WriteDB(), d.ReadDB())
	fp := ai.Fingerprint{
		ModelID:      "siglip2",
		InputProfile: fmt.Sprintf("test-%s", t.Name()),
	}
	row, err := g.FindOrCreateBuilding(context.Background(), fp, dim)
	require.NoError(t, err)
	return row
}

func TestMapping_WriteVectorIsNetNew(t *testing.T) {
	r := require.New(t)
	ctx := context.Background()
	d := testutil.OpenTestDB(t)
	owner := testutil.SeedOwner(t, d.WriteDB(), "hub", "alice")
	mid := testutil.SeedPhoto(t, d.WriteDB(), owner, "p1")
	gen := mustCreateBuildingGen(t, d, 768)

	m := embedding.NewMapping(d.WriteDB())

	// First write: net-new → delta = +1.
	delta, err := m.WriteVector(ctx, gen, mid, mockVec(768))
	r.NoError(err)
	r.Equal(1, delta, "first write is net-new")

	// Replacement (same media+gen): delta = 0.
	delta2, err := m.WriteVector(ctx, gen, mid, mockVec(768))
	r.NoError(err)
	r.Equal(0, delta2, "second write is a replacement, not net-new")

	// The mapping table should still have exactly one row for this
	// (gen, media) pair — replacement, not duplication.
	var n int
	err = d.ReadDB().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM media_embedding_ids WHERE generation_id=? AND media_id=?`,
		gen.ID, mid,
	).Scan(&n)
	r.NoError(err)
	r.Equal(1, n)
}

func TestMapping_DeleteForGenerationMedia(t *testing.T) {
	r := require.New(t)
	ctx := context.Background()
	d := testutil.OpenTestDB(t)
	owner := testutil.SeedOwner(t, d.WriteDB(), "hub", "alice")
	mid := testutil.SeedPhoto(t, d.WriteDB(), owner, "p1")
	gen := mustCreateBuildingGen(t, d, 768)

	m := embedding.NewMapping(d.WriteDB())
	_, err := m.WriteVector(ctx, gen, mid, mockVec(768))
	r.NoError(err)

	// Delete in its own tx so we can observe the delta and the row counts.
	tx, err := d.WriteDB().BeginTx(ctx, nil)
	r.NoError(err)
	delta, err := embedding.DeleteForGenerationMediaTx(ctx, tx, gen, mid)
	r.NoError(err)
	r.Equal(-1, delta, "delete returns -1 when a mapping row was removed")
	r.NoError(tx.Commit())

	// Mapping row gone.
	var n int
	r.NoError(d.ReadDB().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM media_embedding_ids WHERE generation_id=? AND media_id=?`,
		gen.ID, mid,
	).Scan(&n))
	r.Equal(0, n, "mapping row removed")

	// Vec0 row gone.
	var vn int
	r.NoError(d.ReadDB().QueryRowContext(ctx,
		fmt.Sprintf(`SELECT COUNT(*) FROM %s`, gen.VecTableName),
	).Scan(&vn))
	r.Equal(0, vn, "vec0 row removed")

	// Idempotent: a second delete is a no-op and returns 0.
	tx2, err := d.WriteDB().BeginTx(ctx, nil)
	r.NoError(err)
	delta2, err := embedding.DeleteForGenerationMediaTx(ctx, tx2, gen, mid)
	r.NoError(err)
	r.Equal(0, delta2, "delete with no prior mapping returns 0")
	r.NoError(tx2.Commit())
}

func TestMapping_VecBlobRoundTrip(t *testing.T) {
	r := require.New(t)
	ctx := context.Background()
	d := testutil.OpenTestDB(t)
	owner := testutil.SeedOwner(t, d.WriteDB(), "hub", "alice")
	mid := testutil.SeedPhoto(t, d.WriteDB(), owner, "p1")
	gen := mustCreateBuildingGen(t, d, 4)

	// A small distinguishable vector so we can verify byte-level layout.
	vec := []float32{1.0, -2.5, 3.25, 0.125}

	m := embedding.NewMapping(d.WriteDB())
	_, err := m.WriteVector(ctx, gen, mid, vec)
	r.NoError(err)

	// Read the embedding column raw to confirm sqlite-vec stored the
	// little-endian float32 packing we sent in.
	var blob []byte
	err = d.ReadDB().QueryRowContext(ctx,
		fmt.Sprintf(`SELECT embedding FROM %s LIMIT 1`, gen.VecTableName),
	).Scan(&blob)
	r.NoError(err)
	r.Len(blob, 4*len(vec), "blob is 4 bytes per float32")

	got := make([]float32, len(vec))
	for i := range got {
		got[i] = math.Float32frombits(binary.LittleEndian.Uint32(blob[i*4 : (i+1)*4]))
	}
	r.Equal(vec, got, "round-tripped float32 values match input")
}

// TestMapping_VecIDAllocatorAvoidsOrphanedVec0Rows covers the
// orphan-collision regression: a media row hard-deleted via FK cascade
// removes the mapping row from media_embedding_ids but leaves the
// matching vec0 row in place (the K1 compactor is the only thing that
// drops orphan vec0 rows). A subsequent allocator call MUST query the
// vec0 table — not media_embedding_ids — so the new vec_id doesn't
// collide with the orphan and fail the vec0 INSERT.
func TestMapping_VecIDAllocatorAvoidsOrphanedVec0Rows(t *testing.T) {
	r := require.New(t)
	ctx := context.Background()
	d := testutil.OpenTestDB(t)
	owner := testutil.SeedOwner(t, d.WriteDB(), "hub", "alice")
	gen := mustCreateBuildingGen(t, d, 4)

	m := embedding.NewMapping(d.WriteDB())

	// Seed three vectors, one per media. Each gets vec_id 1..3.
	mids := []string{
		testutil.SeedPhoto(t, d.WriteDB(), owner, "p1"),
		testutil.SeedPhoto(t, d.WriteDB(), owner, "p2"),
		testutil.SeedPhoto(t, d.WriteDB(), owner, "p3"),
	}
	for _, mid := range mids {
		_, err := m.WriteVector(ctx, gen, mid, mockVec(4))
		r.NoError(err)
	}

	// Delete media[1]. The FK cascade removes its row from
	// media_embedding_ids but leaves vec0 row 2 in place — that's the
	// orphan the allocator must not collide with.
	_, err := d.WriteDB().ExecContext(ctx, `DELETE FROM media WHERE id = ?`, mids[1])
	r.NoError(err)

	// Confirm the orphan exists in vec0 and the mapping is gone.
	var mappingCount, vecCount int
	r.NoError(d.ReadDB().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM media_embedding_ids WHERE generation_id=?`, gen.ID,
	).Scan(&mappingCount))
	r.Equal(2, mappingCount, "mapping row 2 cascaded out")
	r.NoError(d.ReadDB().QueryRowContext(ctx,
		fmt.Sprintf(`SELECT COUNT(*) FROM %s`, gen.VecTableName),
	).Scan(&vecCount))
	r.Equal(3, vecCount, "vec0 still has the orphan row 2")

	// Write a fourth vector. The allocator must pick vec_id=4 (MAX+1
	// over the vec0 table), not 3 (which would be MAX+1 over the
	// orphan-pruned mapping table and would collide with vec0 row 3).
	mid4 := testutil.SeedPhoto(t, d.WriteDB(), owner, "p4")
	_, err = m.WriteVector(ctx, gen, mid4, mockVec(4))
	r.NoError(err, "WriteVector must succeed despite the orphan vec0 row")

	var newVecID int64
	r.NoError(d.ReadDB().QueryRowContext(ctx,
		`SELECT vec_id FROM media_embedding_ids WHERE generation_id=? AND media_id=?`,
		gen.ID, mid4,
	).Scan(&newVecID))
	r.Equal(int64(4), newVecID, "new vec_id must be 4, not 3 (orphan vec0 row 3 is still live)")

	// Read back the new vec0 row to confirm INSERT succeeded.
	var blobLen int
	r.NoError(d.ReadDB().QueryRowContext(ctx,
		fmt.Sprintf(`SELECT length(embedding) FROM %s WHERE vec_id=?`, gen.VecTableName),
		newVecID,
	).Scan(&blobLen))
	r.Equal(4*4, blobLen, "new vec row stored at the allocated id")
}

func TestMapping_WriteVector_AllocatesUniqueVecIDs(t *testing.T) {
	r := require.New(t)
	ctx := context.Background()
	d := testutil.OpenTestDB(t)
	owner := testutil.SeedOwner(t, d.WriteDB(), "hub", "alice")
	gen := mustCreateBuildingGen(t, d, 768)

	m := embedding.NewMapping(d.WriteDB())

	// Write 3 vectors for 3 distinct media in the same generation, then
	// assert vec_ids are 1, 2, 3 in insertion order — the per-generation
	// MAX(vec_id)+1 allocator's contract.
	mids := []string{
		testutil.SeedPhoto(t, d.WriteDB(), owner, "p1"),
		testutil.SeedPhoto(t, d.WriteDB(), owner, "p2"),
		testutil.SeedPhoto(t, d.WriteDB(), owner, "p3"),
	}
	for _, mid := range mids {
		delta, err := m.WriteVector(ctx, gen, mid, mockVec(768))
		r.NoError(err)
		r.Equal(1, delta)
	}

	rows, err := d.ReadDB().QueryContext(ctx,
		`SELECT media_id, vec_id FROM media_embedding_ids
		  WHERE generation_id = ? ORDER BY vec_id ASC`,
		gen.ID,
	)
	r.NoError(err)
	defer func() { _ = rows.Close() }()

	type pair struct {
		media string
		vecID int64
	}
	var got []pair
	for rows.Next() {
		var p pair
		r.NoError(rows.Scan(&p.media, &p.vecID))
		got = append(got, p)
	}
	r.NoError(rows.Err())

	r.Equal([]pair{
		{mids[0], 1},
		{mids[1], 2},
		{mids[2], 3},
	}, got, "vec_ids allocated 1,2,3 in insertion order")
}
