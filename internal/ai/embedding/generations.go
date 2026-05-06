package embedding

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/wesm/fotobank/internal/ai"
	"github.com/wesm/fotobank/internal/errs"
)

// Row mirrors a row in the embedding_generations table.
//
// State is one of "building" / "active" / "retired". ActivatedAt and
// RetiredAt are nullable in the schema (a building row has neither
// timestamp set; a retired row has both); represented as *time.Time so
// the caller can distinguish "unset" from a zero-value timestamp.
type Row struct {
	ID              int64
	Fingerprint     string
	FingerprintHash string
	ModelID         string
	InputProfile    string
	VecTableName    string
	Dimension       int
	State           string
	EmbeddedCount   int
	ThresholdPct    int
	CreatedAt       time.Time
	ActivatedAt     *time.Time
	RetiredAt       *time.Time
}

// Generations is the embedding_generations registry. The repo manages
// the registry table and the per-generation vec0 virtual tables that
// hang off each row.
//
// Writes go through rw (the single-writer pool); reads go through ro
// (the parallel read-only pool). FindOrCreateBuilding's fast path uses
// the ro pool; the slow path (insert + CREATE VIRTUAL TABLE) sequences
// on the rw pool inside one transaction so a rollback unwinds the row
// and the vec0 table together.
//
// events is the post-commit lifecycle hook (Task P1): created fires
// from FindOrCreateBuilding's insert branch, retired fires from
// Promote / PromoteFromBuilding when the retire-prior-active UPDATE
// actually changed a row. activated lives on the activator (Task H1)
// because it's the activator's tick that decides when to promote.
// Defaults to NoopEmitter so the simplest test wiring stays terse;
// production wiring uses SetEmitter to bind the SSE adapter.
type Generations struct {
	rw     *sql.DB
	ro     *sql.DB
	events EventEmitter
}

// NewGenerations builds a Generations registry over the supplied pools.
// Events default to NoopEmitter; production callers should follow up
// with SetEmitter to wire the SSE bus adapter.
func NewGenerations(rw, ro *sql.DB) *Generations {
	return &Generations{rw: rw, ro: ro, events: NoopEmitter{}}
}

// SetEmitter installs the post-commit event emitter on the registry.
// Callers in production wiring (cmd/fotobank server) bind the
// httpapi.AIEmbedEvents adapter here so generation lifecycle changes
// reach the SSE channel. Passing nil resets to the no-op default —
// useful for tests that want to exercise the registry with events
// disabled mid-run.
func (g *Generations) SetEmitter(e EventEmitter) {
	if e == nil {
		e = NoopEmitter{}
	}
	g.events = e
}

// generationColumns is the canonical SELECT-list for embedding_generations
// rows. Centralised so scanGeneration and every callsite stay in lockstep.
const generationColumns = `id, fingerprint, fingerprint_hash, model_id, input_profile,
	vec_table_name, dimension, state, embedded_count, threshold_pct,
	created_at, activated_at, retired_at`

// rowScanner is the minimal interface satisfied by both *sql.Row and
// *sql.Rows — lets scanGeneration handle single-row and iteration paths.
type rowScanner interface {
	Scan(dest ...any) error
}

// scanGeneration reads one embedding_generations row out of s. The
// nullable timestamp columns Scan into sql.NullTime, then are mapped
// to *time.Time on the Row.
func scanGeneration(s rowScanner) (Row, error) {
	var (
		row         Row
		activatedAt sql.NullTime
		retiredAt   sql.NullTime
	)
	if err := s.Scan(
		&row.ID, &row.Fingerprint, &row.FingerprintHash, &row.ModelID, &row.InputProfile,
		&row.VecTableName, &row.Dimension, &row.State, &row.EmbeddedCount, &row.ThresholdPct,
		&row.CreatedAt, &activatedAt, &retiredAt,
	); err != nil {
		return Row{}, err
	}
	if activatedAt.Valid {
		v := activatedAt.Time
		row.ActivatedAt = &v
	}
	if retiredAt.Valid {
		v := retiredAt.Time
		row.RetiredAt = &v
	}
	return row, nil
}

// queryRower is the minimal interface needed to QueryRow against either
// a *sql.DB or a *sql.Tx, used so findByHash works on either side of a
// transaction boundary.
type queryRower interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// findByHash looks up a generation row by fingerprint hash, returning
// sql.ErrNoRows when no match exists. db may be a *sql.DB (read-only
// pool, fast path) or a *sql.Tx (re-check inside the FindOrCreate tx).
func findByHash(ctx context.Context, db queryRower, hash string) (Row, error) {
	return scanGeneration(db.QueryRowContext(ctx,
		`SELECT `+generationColumns+` FROM embedding_generations WHERE fingerprint_hash = ?`,
		hash,
	))
}

func findByFingerprint(ctx context.Context, db queryRower, fp ai.Fingerprint) (Row, error) {
	return scanGeneration(db.QueryRowContext(ctx,
		`SELECT `+generationColumns+` FROM embedding_generations
		  WHERE fingerprint = ?
		  ORDER BY CASE state WHEN 'active' THEN 0 WHEN 'building' THEN 1 ELSE 2 END, id ASC
		  LIMIT 1`,
		fp.String(),
	))
}

// FindOrCreateBuilding returns the generation row matching fp. If a
// row with the same fingerprint already exists (in any state), it is
// returned unchanged. Otherwise a new building row is inserted and a
// per-generation vec0 virtual table named "media_embeddings_g{id}" is
// created in the same transaction, so a rollback removes both together.
//
// Idempotent: concurrent callers serialise on the rw pool's single
// connection; the loser re-finds the winner's row inside the tx
// re-check via the fingerprint_hash UNIQUE constraint.
func (g *Generations) FindOrCreateBuilding(ctx context.Context, fp ai.Fingerprint, dim int) (Row, error) {
	hash := fingerprintHash(fp, dim)

	// Fast path: an existing row for this fingerprint.
	if row, err := findByHash(ctx, g.ro, hash); err == nil {
		return row, nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return Row{}, fmt.Errorf("find by hash: %w", err)
	}
	// Compatibility path for existing queue workers: ai_jobs rows carry
	// only ai.Fingerprint, not the configured vector dimension yet. If a
	// prior generation exists for that raw fingerprint, return it so old
	// in-flight work keeps using the generation it was enqueued for. The
	// admin Apply path uses FindOrCreateBuildingTx directly and remains
	// dimension-defining.
	if row, err := findByFingerprint(ctx, g.ro, fp); err == nil {
		return row, nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return Row{}, fmt.Errorf("find by fingerprint: %w", err)
	}

	tx, err := g.rw.BeginTx(ctx, nil)
	if err != nil {
		return Row{}, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	row, created, err := g.findOrCreateBuildingTx(ctx, tx, fp, dim)
	if err != nil {
		return Row{}, err
	}

	if err := tx.Commit(); err != nil {
		return Row{}, fmt.Errorf("commit: %w", err)
	}

	// Emit the lifecycle event only on the actual INSERT path. The
	// fast-path lookup and the in-tx re-find both return an existing row;
	// every created=true return corresponds to one new
	// embedding_generations row hitting disk.
	if created {
		g.events.EmitAIEmbedGenerationCreated(row.ID, row.Fingerprint)
	}
	return row, nil
}

// FindOrCreateBuildingTx returns the generation row matching fp using
// the caller's transaction. If a row with the same fingerprint already
// exists in that transaction, it is returned unchanged. Otherwise a new
// building row and its per-generation vec0 table are created inside tx.
//
// The caller owns commit/rollback and any post-commit lifecycle event
// emission. This is used by admin Apply so app_settings updates and the
// generation registry change are atomic.
func (g *Generations) FindOrCreateBuildingTx(ctx context.Context, tx *sql.Tx, fp ai.Fingerprint, dim int) (Row, error) {
	row, _, err := g.findOrCreateBuildingTx(ctx, tx, fp, dim)
	return row, err
}

func (g *Generations) findOrCreateBuildingTx(ctx context.Context, tx *sql.Tx, fp ai.Fingerprint, dim int) (Row, bool, error) {
	hash := fingerprintHash(fp, dim)

	// Re-check inside the tx in case a concurrent caller inserted while
	// the public wrapper was on the fast path. Returning the existing row
	// here matches the contract: idempotent under concurrent calls.
	if row, err := findByHash(ctx, tx, hash); err == nil {
		return row, false, nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return Row{}, false, fmt.Errorf("re-find by hash: %w", err)
	}

	// Insert with an empty vec_table_name placeholder; the column is
	// NOT NULL UNIQUE so we can't insert the final name in one shot
	// (the id is unknown until LastInsertId). Patch in a second UPDATE.
	res, err := tx.ExecContext(ctx,
		`INSERT INTO embedding_generations
		   (fingerprint, fingerprint_hash, model_id, input_profile, vec_table_name,
		    dimension, state, created_at)
		 VALUES (?, ?, ?, ?, '', ?, 'building', ?)`,
		fp.String(), hash, fp.ModelID, fp.InputProfile, dim, time.Now().UTC(),
	)
	if err != nil {
		return Row{}, false, fmt.Errorf("insert generation: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return Row{}, false, fmt.Errorf("last insert id: %w", err)
	}

	// Vec table name is application-derived from the auto-incremented id.
	// Never user-supplied — safe to interpolate into the CREATE statement.
	tableName := "media_embeddings_g" + strconv.FormatInt(id, 10)
	if _, err := tx.ExecContext(ctx,
		`UPDATE embedding_generations SET vec_table_name = ? WHERE id = ?`,
		tableName, id,
	); err != nil {
		return Row{}, false, fmt.Errorf("set vec_table_name: %w", err)
	}

	// Create the vec0 virtual table inside the same tx so a rollback
	// removes both the row and the table together. dim flows from the
	// caller's Probe-validated configuration; the schema string itself
	// is fixed.
	createSQL := fmt.Sprintf(
		`CREATE VIRTUAL TABLE %s USING vec0(vec_id INTEGER PRIMARY KEY, embedding FLOAT[%d])`,
		tableName, dim,
	)
	if _, err := tx.ExecContext(ctx, createSQL); err != nil {
		return Row{}, false, fmt.Errorf("create vec table %s: %w", tableName, err)
	}

	row, err := findByHash(ctx, tx, hash)
	if err != nil {
		return Row{}, false, fmt.Errorf("read back: %w", err)
	}
	return row, true, nil
}

// retirePriorActiveTx captures any currently-active row, retires it
// inside tx, and returns (id, fingerprint, retired) where retired is
// true iff a prior active row existed and was actually flipped to
// 'retired'. The SELECT runs inside the tx so a concurrent admin
// retire can't make us emit a phantom retired event under an id that
// some other writer already moved.
//
// promotingID is the id about to be re-activated by the caller. When
// the prior active and the promotion target are the same row (an
// idempotent re-promote), the row is reactivated before commit and the
// retired event would be misleading — retired is forced to false in
// that case so no listener sees a "retired" announcement for an id that
// is once again active.
//
// Used by Promote and PromoteFromBuilding so the lifecycle event fires
// in lockstep with the schema change — emit only when n > 0, with the
// id/fingerprint captured before the row was retired.
func retirePriorActiveTx(ctx context.Context, tx *sql.Tx, now time.Time, promotingID int64) (int64, string, bool, error) {
	var (
		priorID int64
		priorFP string
	)
	row := tx.QueryRowContext(ctx,
		`SELECT id, fingerprint FROM embedding_generations WHERE state='active'`,
	)
	switch err := row.Scan(&priorID, &priorFP); {
	case errors.Is(err, sql.ErrNoRows):
		// "No prior active" — the normal state on first promotion. The
		// retire UPDATE is still issued for symmetry with prior
		// behaviour (it's a no-op), but no event fires.
	case err != nil:
		return 0, "", false, fmt.Errorf("find prior active: %w", err)
	}

	res, err := tx.ExecContext(ctx,
		`UPDATE embedding_generations
		    SET state='retired', retired_at=?
		  WHERE state='active'`,
		now,
	)
	if err != nil {
		return 0, "", false, fmt.Errorf("retire prior active: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, "", false, fmt.Errorf("retire prior active rows affected: %w", err)
	}
	// Only emit if both signals agree: a prior row was found AND the
	// UPDATE matched. Either side alone could mean a concurrent
	// transition snuck through under WAL between the SELECT and the
	// UPDATE, in which case the other writer owns the announcement.
	// Suppress the retired flag when the prior active is the same row
	// the caller is about to reactivate — the row will commit as
	// 'active' again, so emitting "retired" for it would be a lie.
	retired := priorID != 0 && n > 0 && priorID != promotingID
	return priorID, priorFP, retired, nil
}

// Promote retires any current active generation and promotes id to
// active. The two UPDATEs run in one transaction so the partial unique
// index embedding_generations_one_active never observes two active rows
// at once.
//
// The activation UPDATE additionally clears retired_at so a previously
// retired row that is being re-promoted lands in the canonical active
// shape (activated_at set, retired_at NULL). Returns errs.ErrNotFound
// when id does not match any row, rolling back so a prior active row
// remains active.
//
// Emits EmitAIEmbedGenerationRetired on commit when a prior active row
// was actually retired (RowsAffected > 0); emission after Commit means
// a listener observing the event sees a row that has already been
// retired in the DB.
func (g *Generations) Promote(ctx context.Context, id int64) error {
	tx, err := g.rw.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	now := time.Now().UTC()
	priorID, priorFP, retired, err := retirePriorActiveTx(ctx, tx, now, id)
	if err != nil {
		return err
	}
	// Activate the target row. Clearing retired_at keeps the row in the
	// canonical active shape even if it was previously retired and is
	// being re-promoted.
	res, err := tx.ExecContext(ctx,
		`UPDATE embedding_generations
		    SET state='active', activated_at=?, retired_at=NULL
		  WHERE id=?`,
		now, id,
	)
	if err != nil {
		return fmt.Errorf("promote: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("promote rows affected: %w", err)
	}
	if n == 0 {
		// Roll back so the just-retired prior active is restored. Returning
		// ErrNotFound here makes the caller's intent ("activate id X")
		// surface as a clean sentinel rather than an opaque success.
		return fmt.Errorf("promote id %d: %w", id, errs.ErrNotFound)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	// Post-commit: emit the retired-row event only if we actually
	// retired one. Emission outside the tx means a transient bus error
	// can't roll back the schema change — the lifecycle row is durable
	// in the DB regardless of SSE delivery.
	if retired {
		g.events.EmitAIEmbedGenerationRetired(priorID, priorFP)
	}
	return nil
}

// PromoteFromBuilding promotes id only if it is currently in the
// 'building' state. Returns errs.ErrNotFound if no row matches —
// either the id doesn't exist, or the row was retired or already
// promoted concurrently. The activator uses this instead of Promote()
// to defend against a race where an admin retires the building
// generation between the activator's FindBuilding and Promote calls;
// without the state filter, the unconditional UPDATE on Promote could
// undo that retirement.
//
// Identical to Promote in every other respect: any prior active row is
// retired in the same tx, retired_at is cleared on the activated row,
// and the partial unique index embedding_generations_one_active never
// observes two active rows at once.
func (g *Generations) PromoteFromBuilding(ctx context.Context, id int64) error {
	tx, err := g.rw.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	now := time.Now().UTC()
	priorID, priorFP, retired, err := retirePriorActiveTx(ctx, tx, now, id)
	if err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx,
		`UPDATE embedding_generations
		    SET state='active', activated_at=?, retired_at=NULL
		  WHERE id=? AND state='building'`,
		now, id,
	)
	if err != nil {
		return fmt.Errorf("promote from building: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("promote from building rows affected: %w", err)
	}
	if n == 0 {
		// Roll back so a concurrent retire is preserved and any prior
		// active row stays active. The caller treats ErrNotFound as a
		// no-op — the row is no longer a promotion candidate.
		return fmt.Errorf("promote from building id %d: %w", id, errs.ErrNotFound)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	// Post-commit retired emit, gated on RowsAffected — see Promote.
	if retired {
		g.events.EmitAIEmbedGenerationRetired(priorID, priorFP)
	}
	return nil
}

// Retire transitions id to the retired state and stamps retired_at.
// The vec0 table is left in place — the compactor drops it once the
// retain_retired_days window elapses (see Plan K1).
func (g *Generations) Retire(ctx context.Context, id int64) error {
	if _, err := g.rw.ExecContext(ctx,
		`UPDATE embedding_generations
		    SET state='retired', retired_at=?
		  WHERE id=?`,
		time.Now().UTC(), id,
	); err != nil {
		return fmt.Errorf("retire: %w", err)
	}
	return nil
}

// FindActive returns the currently-active generation, or nil if none.
// The partial unique index embedding_generations_one_active guarantees
// at most one row matches.
func (g *Generations) FindActive(ctx context.Context) (*Row, error) {
	row, err := scanGeneration(g.ro.QueryRowContext(ctx,
		`SELECT `+generationColumns+` FROM embedding_generations WHERE state = 'active'`,
	))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil //nolint:nilnil // sentinel "no active generation" — distinct from an error.
		}
		return nil, fmt.Errorf("find active: %w", err)
	}
	return &row, nil
}

// GetByID returns the generation row with the given id. Returns
// errs.ErrNotFound when no row matches. Reads through the ro pool so
// callers refreshing a row after a writer commit see the latest state
// (WAL guarantees the read sees the writer's prior commit).
func (g *Generations) GetByID(ctx context.Context, id int64) (*Row, error) {
	row, err := scanGeneration(g.ro.QueryRowContext(ctx,
		`SELECT `+generationColumns+` FROM embedding_generations WHERE id = ?`,
		id,
	))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("get generation %d: %w", id, errs.ErrNotFound)
		}
		return nil, fmt.Errorf("get generation %d: %w", id, err)
	}
	return &row, nil
}

// FindBuilding returns the oldest currently-building generation, or nil
// if none. Multiple building rows can coexist transiently — e.g. when an
// operator switches the embed model mid-rollout the prior building row
// keeps its mappings while the new one starts collecting — but the
// activator only ever advances one at a time. Ordering by id ASC picks
// the longest-running candidate so a stale building row is promoted (or
// retired) before a newer one can race ahead.
func (g *Generations) FindBuilding(ctx context.Context) (*Row, error) {
	row, err := scanGeneration(g.ro.QueryRowContext(ctx,
		`SELECT `+generationColumns+` FROM embedding_generations
		  WHERE state = 'building'
		  ORDER BY id ASC
		  LIMIT 1`,
	))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil //nolint:nilnil // sentinel "no building generation" — distinct from an error.
		}
		return nil, fmt.Errorf("find building: %w", err)
	}
	return &row, nil
}

// List returns all generation rows in the supplied state. Caller is
// responsible for passing a valid state ("building" / "active" /
// "retired"); the table CHECK constraint rejects anything else at
// insert/update time, and List simply filters by equality.
func (g *Generations) List(ctx context.Context, state string) ([]Row, error) {
	rows, err := g.ro.QueryContext(ctx,
		`SELECT `+generationColumns+` FROM embedding_generations
		  WHERE state = ?
		  ORDER BY id ASC`,
		state,
	)
	if err != nil {
		return nil, fmt.Errorf("list: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []Row
	for rows.Next() {
		row, err := scanGeneration(rows)
		if err != nil {
			return nil, fmt.Errorf("scan: %w", err)
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iter: %w", err)
	}
	return out, nil
}

// SetEmbeddedCount overwrites the embedded_count for id. Used by the
// activator (Task H1) when reconciling the count against a fresh
// JOIN-based recount; the worker's IncEmbeddedCount handles the steady
// state.
func (g *Generations) SetEmbeddedCount(ctx context.Context, id int64, n int) error {
	if _, err := g.rw.ExecContext(ctx,
		`UPDATE embedding_generations SET embedded_count = ? WHERE id = ?`,
		n, id,
	); err != nil {
		return fmt.Errorf("set embedded_count: %w", err)
	}
	return nil
}

// IncEmbeddedCount adjusts embedded_count by delta. delta may be
// negative (e.g. a thumb-regen invalidation deletes a mapping row) but
// the column is INTEGER NOT NULL DEFAULT 0; callers are expected not
// to drive the count below zero. The arithmetic is server-side so two
// concurrent writers serialise on the single rw connection.
func (g *Generations) IncEmbeddedCount(ctx context.Context, id int64, delta int) error {
	if _, err := g.rw.ExecContext(ctx,
		`UPDATE embedding_generations SET embedded_count = embedded_count + ? WHERE id = ?`,
		delta, id,
	); err != nil {
		return fmt.Errorf("inc embedded_count: %w", err)
	}
	return nil
}

// fingerprintHash returns the hex-encoded sha256 of fp.String() plus
// dimension. Dimension is generation-defining even though it does not
// live inside ai.Fingerprint: the vec0 table schema embeds the vector
// length, so a dimension change must allocate a distinct generation.
// The hash keeps the lookup index fixed-width independent of
// model/profile string length, and lets a future migration to a
// different encoding re-key by updating both columns atomically.
func fingerprintHash(fp ai.Fingerprint, dim int) string {
	sum := sha256.Sum256(fmt.Appendf(nil, "%s\x00dim=%d", fp.String(), dim))
	return hex.EncodeToString(sum[:])
}
