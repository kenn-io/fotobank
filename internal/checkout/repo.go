package checkout

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"go.kenn.io/fotobank/internal/errs"
	"go.kenn.io/fotobank/internal/owners"
)

type Repo struct {
	rw *sql.DB
	ro *sql.DB
}

func NewRepo(rw, ro *sql.DB) *Repo { return &Repo{rw: rw, ro: ro} }

func (r *Repo) ResolveSelection(
	ctx context.Context,
	owner owners.Principal,
	selection Selection,
) ([]Candidate, error) {
	if err := validateSelection(selection); err != nil {
		return nil, fmt.Errorf("resolve checkout selection: %w", err)
	}
	tx, err := r.ro.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, fmt.Errorf("resolve checkout selection: begin read: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := r.validateOwnedSelectors(ctx, tx, owner, selection); err != nil {
		return nil, err
	}

	conditions := make([]string, 0, 4)
	args := []any{owner.Hub, owner.UserID}
	if selection.All {
		conditions = append(conditions, "1")
	}
	if len(selection.AssetIDs) > 0 {
		conditions = append(conditions, "a.id IN ("+placeholders(len(selection.AssetIDs))+")")
		for _, id := range selection.AssetIDs {
			args = append(args, id)
		}
	}
	if len(selection.AlbumIDs) > 0 {
		conditions = append(conditions, `EXISTS (
			SELECT 1 FROM album_media am
			WHERE am.media_id = a.id AND am.album_id IN (`+placeholders(len(selection.AlbumIDs))+`)
		)`)
		for _, id := range selection.AlbumIDs {
			args = append(args, id)
		}
	}
	for _, years := range selection.Years {
		conditions = append(conditions,
			"(a.timestamp IS NOT NULL AND CAST(strftime('%Y', a.timestamp) AS INTEGER) BETWEEN ? AND ?)")
		args = append(args, years.Start, years.End)
	}
	query := `SELECT DISTINCT
		a.id, f.id, f.original_filename, f.current_version_id, f.sha256,
		f.size, a.timestamp
	FROM assets a
	JOIN media_files f ON f.asset_id = a.id
	WHERE a.owner_hub = ? AND a.owner_user_id = ? AND a.state = 'ready'
	  AND a.hidden_at IS NULL
	  AND (` + strings.Join(conditions, " OR ") + `)
	ORDER BY a.timestamp IS NULL, a.timestamp, a.id, f.role, f.id`
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("resolve checkout selection: query: %w", err)
	}
	defer rows.Close()
	var candidates []Candidate
	for rows.Next() {
		var candidate Candidate
		var captured sql.NullTime
		if err := rows.Scan(
			&candidate.AssetID, &candidate.FileID, &candidate.OriginalFilename,
			&candidate.VersionID, &candidate.SHA256, &candidate.Size, &captured,
		); err != nil {
			return nil, fmt.Errorf("resolve checkout selection: scan: %w", err)
		}
		if captured.Valid {
			candidate.CapturedAt = &captured.Time
		}
		candidates = append(candidates, candidate)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("resolve checkout selection: iterate: %w", err)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("resolve checkout selection: close rows: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("resolve checkout selection: commit read: %w", err)
	}
	return candidates, nil
}

func (r *Repo) validateOwnedSelectors(
	ctx context.Context,
	tx *sql.Tx,
	owner owners.Principal,
	selection Selection,
) error {
	for _, id := range selection.AssetIDs {
		var found int
		err := tx.QueryRowContext(ctx, `SELECT 1 FROM assets
			WHERE id = ? AND owner_hub = ? AND owner_user_id = ?
			  AND state = 'ready' AND hidden_at IS NULL`,
			id, owner.Hub, owner.UserID).Scan(&found)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("resolve checkout selection: %w: asset %s", errs.ErrNotFound, id)
		}
		if err != nil {
			return fmt.Errorf("resolve checkout selection: validate asset: %w", err)
		}
	}
	for _, id := range selection.AlbumIDs {
		var found int
		err := tx.QueryRowContext(ctx, `SELECT 1 FROM albums
			WHERE id = ? AND owner_hub = ? AND owner_user_id = ?`,
			id, owner.Hub, owner.UserID).Scan(&found)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("resolve checkout selection: %w: album %s", errs.ErrNotFound, id)
		}
		if err != nil {
			return fmt.Errorf("resolve checkout selection: validate album: %w", err)
		}
	}
	return nil
}

func (r *Repo) Insert(ctx context.Context, checkout Checkout) error {
	if err := validateSelection(checkout.Selection); err != nil {
		return fmt.Errorf("insert checkout: %w", err)
	}
	tx, err := r.rw.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("insert checkout: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	_, err = tx.ExecContext(ctx, `INSERT INTO checkouts (
		id, owner_hub, owner_user_id, root, layout, include_all, state,
		last_error, created_at, updated_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, NULL, ?, ?)`,
		checkout.ID, checkout.Owner.Hub, checkout.Owner.UserID, checkout.Root,
		checkout.Layout, checkout.Selection.All, string(checkout.State),
		checkout.CreatedAt, checkout.UpdatedAt)
	if err != nil {
		return fmt.Errorf("insert checkout: row: %w", err)
	}
	for _, id := range checkout.Selection.AssetIDs {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO checkout_asset_selections(checkout_id, asset_id) VALUES (?, ?)`,
			checkout.ID, id); err != nil {
			return fmt.Errorf("insert checkout: asset selection: %w", err)
		}
	}
	for _, id := range checkout.Selection.AlbumIDs {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO checkout_album_selections(checkout_id, album_id) VALUES (?, ?)`,
			checkout.ID, id); err != nil {
			return fmt.Errorf("insert checkout: album selection: %w", err)
		}
	}
	for _, years := range checkout.Selection.Years {
		if _, err := tx.ExecContext(ctx, `INSERT INTO checkout_year_selections(
			checkout_id, start_year, end_year) VALUES (?, ?, ?)`,
			checkout.ID, years.Start, years.End); err != nil {
			return fmt.Errorf("insert checkout: year selection: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("insert checkout: commit: %w", err)
	}
	return nil
}

func (r *Repo) LiveRoots(ctx context.Context) ([]string, error) {
	rows, err := r.ro.QueryContext(ctx, `SELECT root FROM checkouts
		WHERE state IN ('building', 'active') ORDER BY root`)
	if err != nil {
		return nil, fmt.Errorf("list live checkout roots: %w", err)
	}
	defer rows.Close()
	var roots []string
	for rows.Next() {
		var root string
		if err := rows.Scan(&root); err != nil {
			return nil, fmt.Errorf("list live checkout roots: scan: %w", err)
		}
		roots = append(roots, root)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list live checkout roots: iterate: %w", err)
	}
	return roots, nil
}

func (r *Repo) InsertEntry(ctx context.Context, entry Entry) error {
	_, err := r.rw.ExecContext(ctx, `INSERT INTO checkout_entries (
		checkout_id, file_id, relative_path, base_version_id, base_sha256,
		base_size, observed_size, observed_mtime, observed_sha256, state,
		last_error, created_at, updated_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULL, ?, ?)`,
		entry.CheckoutID, entry.FileID, entry.RelativePath, entry.BaseVersionID,
		entry.BaseSHA256, entry.BaseSize, entry.ObservedSize, entry.ObservedMTime,
		entry.ObservedSHA256, string(entry.State), entry.CreatedAt, entry.UpdatedAt)
	if err != nil {
		return fmt.Errorf("insert checkout entry: %w", err)
	}
	return nil
}

func (r *Repo) SetState(ctx context.Context, id string, state State, message string, now time.Time) error {
	res, err := r.rw.ExecContext(ctx,
		`UPDATE checkouts SET state = ?, last_error = NULLIF(?, ''), updated_at = ? WHERE id = ?`,
		string(state), message, now, id)
	if err != nil {
		return fmt.Errorf("set checkout state: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("set checkout state: rows affected: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("set checkout state: %w: checkout %s", errs.ErrNotFound, id)
	}
	return nil
}

func (r *Repo) markBuildingInterrupted(
	ctx context.Context,
	owner owners.Principal,
	message string,
	now time.Time,
) (int64, error) {
	res, err := r.rw.ExecContext(ctx, `UPDATE checkouts
		SET state = ?, last_error = ?, updated_at = ?
		WHERE owner_hub = ? AND owner_user_id = ? AND state = ?`,
		string(StateError), message, now, owner.Hub, owner.UserID, string(StateBuilding))
	if err != nil {
		return 0, fmt.Errorf("mark interrupted checkouts: %w", err)
	}
	count, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("mark interrupted checkouts: rows affected: %w", err)
	}
	return count, nil
}

func (r *Repo) Get(ctx context.Context, id string) (Checkout, error) {
	var checkout Checkout
	var state string
	var includeAll bool
	var lastError sql.NullString
	err := r.ro.QueryRowContext(ctx, `SELECT id, owner_hub, owner_user_id,
		root, layout, include_all, state, last_error, created_at, updated_at
		FROM checkouts WHERE id = ?`, id).Scan(
		&checkout.ID, &checkout.Owner.Hub, &checkout.Owner.UserID, &checkout.Root,
		&checkout.Layout, &includeAll, &state, &lastError,
		&checkout.CreatedAt, &checkout.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Checkout{}, fmt.Errorf("get checkout: %w: checkout %s", errs.ErrNotFound, id)
	}
	if err != nil {
		return Checkout{}, fmt.Errorf("get checkout: %w", err)
	}
	checkout.Selection.All = includeAll
	checkout.State = State(state)
	checkout.LastError = lastError.String
	assetRows, err := r.ro.QueryContext(ctx,
		`SELECT asset_id FROM checkout_asset_selections WHERE checkout_id = ? ORDER BY asset_id`, id)
	if err != nil {
		return Checkout{}, fmt.Errorf("get checkout: asset selections: %w", err)
	}
	for assetRows.Next() {
		var assetID string
		if err := assetRows.Scan(&assetID); err != nil {
			_ = assetRows.Close()
			return Checkout{}, fmt.Errorf("get checkout: scan asset selection: %w", err)
		}
		checkout.Selection.AssetIDs = append(checkout.Selection.AssetIDs, assetID)
	}
	if err := errors.Join(assetRows.Err(), assetRows.Close()); err != nil {
		return Checkout{}, fmt.Errorf("get checkout: iterate asset selections: %w", err)
	}
	albumRows, err := r.ro.QueryContext(ctx,
		`SELECT album_id FROM checkout_album_selections WHERE checkout_id = ? ORDER BY album_id`, id)
	if err != nil {
		return Checkout{}, fmt.Errorf("get checkout: album selections: %w", err)
	}
	for albumRows.Next() {
		var albumID string
		if err := albumRows.Scan(&albumID); err != nil {
			_ = albumRows.Close()
			return Checkout{}, fmt.Errorf("get checkout: scan album selection: %w", err)
		}
		checkout.Selection.AlbumIDs = append(checkout.Selection.AlbumIDs, albumID)
	}
	if err := errors.Join(albumRows.Err(), albumRows.Close()); err != nil {
		return Checkout{}, fmt.Errorf("get checkout: iterate album selections: %w", err)
	}
	yearRows, err := r.ro.QueryContext(ctx,
		`SELECT start_year, end_year FROM checkout_year_selections
		WHERE checkout_id = ? ORDER BY start_year, end_year`, id)
	if err != nil {
		return Checkout{}, fmt.Errorf("get checkout: year selections: %w", err)
	}
	for yearRows.Next() {
		var years YearRange
		if err := yearRows.Scan(&years.Start, &years.End); err != nil {
			_ = yearRows.Close()
			return Checkout{}, fmt.Errorf("get checkout: scan year selection: %w", err)
		}
		checkout.Selection.Years = append(checkout.Selection.Years, years)
	}
	if err := errors.Join(yearRows.Err(), yearRows.Close()); err != nil {
		return Checkout{}, fmt.Errorf("get checkout: iterate year selections: %w", err)
	}
	return checkout, nil
}

func (r *Repo) ListEntries(ctx context.Context, checkoutID string) ([]Entry, error) {
	rows, err := r.ro.QueryContext(ctx, `SELECT checkout_id, file_id,
		relative_path, base_version_id, base_sha256, base_size, observed_size,
		observed_mtime, observed_sha256, state, last_error, created_at, updated_at
		FROM checkout_entries WHERE checkout_id = ? ORDER BY relative_path`, checkoutID)
	if err != nil {
		return nil, fmt.Errorf("list checkout entries: %w", err)
	}
	defer rows.Close()
	var entries []Entry
	for rows.Next() {
		var entry Entry
		var state string
		var lastError sql.NullString
		if err := rows.Scan(
			&entry.CheckoutID, &entry.FileID, &entry.RelativePath,
			&entry.BaseVersionID, &entry.BaseSHA256, &entry.BaseSize,
			&entry.ObservedSize, &entry.ObservedMTime, &entry.ObservedSHA256,
			&state, &lastError, &entry.CreatedAt, &entry.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("list checkout entries: scan: %w", err)
		}
		entry.State = EntryState(state)
		entry.LastError = lastError.String
		entries = append(entries, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list checkout entries: iterate: %w", err)
	}
	return entries, nil
}

func placeholders(count int) string {
	return strings.TrimRight(strings.Repeat("?,", count), ",")
}
