package share

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/wesm/fotobank/internal/identity"
	"github.com/wesm/fotobank/internal/owners"
)

// PrincipalDisplayRepo reads and writes the principal_display cache
// table. The table maps (hub, user_id) to the human-readable handle
// observed on the last request that carried one. Its rows are
// best-effort: missing / stale entries return empty handles rather
// than errors.
type PrincipalDisplayRepo struct {
	rw, ro *sql.DB
}

// NewPrincipalDisplayRepo constructs the repo.
func NewPrincipalDisplayRepo(rw, ro *sql.DB) *PrincipalDisplayRepo {
	return &PrincipalDisplayRepo{rw: rw, ro: ro}
}

// Upsert writes or updates the cached handle for p. The conflict-aware
// predicate keeps the row with the newest cached_at; callers that see
// a stale `now` will no-op against a fresher row.
func (r *PrincipalDisplayRepo) Upsert(ctx context.Context, p identity.Principal, now time.Time) error {
	const q = `
INSERT INTO principal_display (hub, user_id, handle, cached_at)
VALUES (?, ?, ?, ?)
ON CONFLICT (hub, user_id) DO UPDATE SET
    handle = excluded.handle,
    cached_at = excluded.cached_at
  WHERE principal_display.cached_at < excluded.cached_at;
`
	if _, err := r.rw.ExecContext(ctx, q, p.Hub, p.UserID, p.Handle, now); err != nil {
		return fmt.Errorf("upsert principal_display: %w", err)
	}
	return nil
}

// Get reads the cached handle for owner; returns ("", false, nil) when
// no row exists and (handle, true, nil) otherwise. A row with handle
// stored as NULL returns ("", true, nil) — ok=true to keep the caller
// from upserting over an intentionally cleared row.
func (r *PrincipalDisplayRepo) Get(ctx context.Context, owner owners.Principal) (string, bool, error) {
	var handle sql.NullString
	err := r.ro.QueryRowContext(ctx,
		`SELECT handle FROM principal_display WHERE hub = ? AND user_id = ?`,
		owner.Hub, owner.UserID).Scan(&handle)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("get principal_display: %w", err)
	}
	if !handle.Valid {
		return "", true, nil
	}
	return handle.String, true, nil
}

// GetBatch fetches handles for many principals in one query. Missing
// principals are absent from the returned map; present-but-null handles
// map to "".
func (r *PrincipalDisplayRepo) GetBatch(ctx context.Context, principals []owners.Principal) (map[owners.Principal]string, error) {
	out := make(map[owners.Principal]string, len(principals))
	if len(principals) == 0 {
		return out, nil
	}
	pairs := make([]string, 0, len(principals))
	args := make([]any, 0, len(principals)*2)
	for _, p := range principals {
		pairs = append(pairs, "(?, ?)")
		args = append(args, p.Hub, p.UserID)
	}
	q := `
WITH want(hub, user_id) AS (VALUES ` + strings.Join(pairs, ",") + `)
SELECT d.hub, d.user_id, d.handle
  FROM want
  JOIN principal_display d ON d.hub = want.hub AND d.user_id = want.user_id
`
	rows, err := r.ro.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("batch principal_display: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var (
			hub, user string
			handle    sql.NullString
		)
		if err := rows.Scan(&hub, &user, &handle); err != nil {
			return nil, fmt.Errorf("scan principal_display: %w", err)
		}
		key := owners.Principal{Hub: hub, UserID: user}
		if handle.Valid {
			out[key] = handle.String
		} else {
			out[key] = ""
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iter principal_display: %w", err)
	}
	return out, nil
}
