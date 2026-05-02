package embedding

import (
	"context"
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
)

// Mapping is the media_embedding_ids repo. It writes the per-generation
// (media_id → vec_id) mapping rows in lockstep with the per-generation
// vec0 virtual table that holds the actual float vectors.
//
// vec0 does not support INSERT OR REPLACE, so writes follow the §5.4
// delete-then-insert pattern: take + drop the prior mapping (capturing
// any prior vec_id), drop the prior vec row if there was one, allocate
// a fresh per-generation vec_id, then insert mapping + vec row.
type Mapping struct {
	rw *sql.DB
}

// NewMapping builds a Mapping repo over the rw pool.
func NewMapping(rw *sql.DB) *Mapping {
	return &Mapping{rw: rw}
}

// WriteVector writes vec for (gen, mediaID) using the §5.4 delete-then-
// insert pattern. Returns +1 on net-new, 0 on replacement. The caller is
// responsible for incrementing embedded_count by the returned delta
// inside the same higher-level transaction (the worker batches mapping
// writes + ai_jobs status updates into one tx).
//
// This *sql.DB variant begins a tx internally and is intended for tests
// that don't span multiple writes. Production code that needs to bundle
// the mapping write with other state changes (e.g. the embed worker
// committing per-batch) must use WriteVectorTx instead.
func (m *Mapping) WriteVector(ctx context.Context, gen Row, mediaID string, vec []float32) (int, error) {
	tx, err := m.rw.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	delta, err := WriteVectorTx(ctx, tx, gen, mediaID, vec)
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit: %w", err)
	}
	return delta, nil
}

// WriteVectorTx is the transaction-bound variant of WriteVector. The
// caller owns the transaction lifecycle (Commit/Rollback). gen carries
// the application-derived VecTableName ("media_embeddings_g{id}"),
// which is safe to interpolate into SQL because it's never user-supplied.
func WriteVectorTx(ctx context.Context, tx *sql.Tx, gen Row, mediaID string, vec []float32) (int, error) {
	// Step 1: take + drop any prior mapping; capture vec_id if present.
	// DELETE ... RETURNING returns no rows when nothing matched, which
	// surfaces as sql.ErrNoRows from Scan — distinct from a real error.
	var priorVecID sql.NullInt64
	row := tx.QueryRowContext(ctx,
		`DELETE FROM media_embedding_ids WHERE generation_id=? AND media_id=? RETURNING vec_id`,
		gen.ID, mediaID,
	)
	switch err := row.Scan(&priorVecID); {
	case errors.Is(err, sql.ErrNoRows):
		priorVecID = sql.NullInt64{}
	case err != nil:
		return 0, fmt.Errorf("drop prior mapping: %w", err)
	}

	// Step 2: drop the prior vec0 row if there was one. vec0 has no
	// foreign-key relationship with media_embedding_ids, so we manage
	// the two tables in lockstep ourselves.
	if priorVecID.Valid {
		if _, err := tx.ExecContext(ctx,
			fmt.Sprintf(`DELETE FROM %s WHERE vec_id = ?`, gen.VecTableName),
			priorVecID.Int64,
		); err != nil {
			return 0, fmt.Errorf("drop prior vec row: %w", err)
		}
	}

	// Step 3: allocate the new vec_id. Query the vec0 table directly
	// rather than media_embedding_ids: vec0 is the authoritative source
	// of vec_id usage, and orphan vec0 rows can outlive their mapping
	// row (e.g. a media row was hard-deleted, FK-cascading the mapping
	// out from under us before the K1 compactor sweeps the orphan vec0
	// row). Querying media_embedding_ids would return MAX+1 that
	// collides with the orphan and the subsequent vec0 INSERT would
	// fail; using vec0 itself always picks an unused vec_id.
	//
	// Concurrent workers serialise on SQLite's single rw connection, so
	// the read of MAX inside this tx sees prior committed inserts.
	var nextVecID int64
	if err := tx.QueryRowContext(ctx,
		fmt.Sprintf(`SELECT COALESCE(MAX(vec_id), 0) + 1 FROM %s`, gen.VecTableName),
	).Scan(&nextVecID); err != nil {
		return 0, fmt.Errorf("allocate vec_id: %w", err)
	}

	// Step 4: insert the new mapping + vec row. vec_f32(?) takes a blob
	// of little-endian float32 bytes.
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO media_embedding_ids (generation_id, media_id, vec_id) VALUES (?, ?, ?)`,
		gen.ID, mediaID, nextVecID,
	); err != nil {
		return 0, fmt.Errorf("insert mapping: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		fmt.Sprintf(`INSERT INTO %s (vec_id, embedding) VALUES (?, vec_f32(?))`, gen.VecTableName),
		nextVecID, VecToBlob(vec),
	); err != nil {
		return 0, fmt.Errorf("insert vec row: %w", err)
	}

	if priorVecID.Valid {
		return 0, nil
	}
	return 1, nil
}

// DeleteForGenerationMediaTx removes the (gen, mediaID) mapping row and
// its vec0 row. Returns -1 if a mapping row was deleted, 0 otherwise
// (idempotent no-op when the mapping doesn't exist). The caller owns
// the transaction; counter adjustments to embedded_count are the
// caller's responsibility.
func DeleteForGenerationMediaTx(ctx context.Context, tx *sql.Tx, gen Row, mediaID string) (int, error) {
	var priorVecID sql.NullInt64
	row := tx.QueryRowContext(ctx,
		`DELETE FROM media_embedding_ids WHERE generation_id=? AND media_id=? RETURNING vec_id`,
		gen.ID, mediaID,
	)
	switch err := row.Scan(&priorVecID); {
	case errors.Is(err, sql.ErrNoRows):
		// No prior mapping — nothing to do, including no vec row to drop.
		return 0, nil
	case err != nil:
		return 0, fmt.Errorf("drop mapping: %w", err)
	}

	// Drop the matching vec0 row. The §5.4 invariant — every mapping
	// row has an accompanying vec row written in the same tx — means a
	// missing vec row here would indicate corruption rather than a
	// benign no-op, but we just issue the DELETE and let it report 0
	// rows affected (we don't surface that fact).
	if priorVecID.Valid {
		if _, err := tx.ExecContext(ctx,
			fmt.Sprintf(`DELETE FROM %s WHERE vec_id = ?`, gen.VecTableName),
			priorVecID.Int64,
		); err != nil {
			return 0, fmt.Errorf("drop vec row: %w", err)
		}
	}
	return -1, nil
}

// VecToBlob packs vec into a little-endian float32 byte slice for
// vec_f32(?). Mirrors the sqlite-vec FLOAT[N] storage layout: 4 bytes
// per element, IEEE-754 single precision, little-endian on disk.
//
// Exported so the read side (search/index) can pack a query vector for
// `embedding MATCH vec_f32(?)` using the same packing as the writer.
func VecToBlob(vec []float32) []byte {
	buf := make([]byte, 4*len(vec))
	for i, v := range vec {
		binary.LittleEndian.PutUint32(buf[i*4:], math.Float32bits(v))
	}
	return buf
}
