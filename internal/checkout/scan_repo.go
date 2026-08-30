package checkout

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"go.kenn.io/fotobank/internal/errs"
)

func (r *Repo) ListActive(ctx context.Context) ([]Checkout, error) {
	rows, err := r.ro.QueryContext(ctx, `SELECT id, owner_hub, owner_user_id,
		root, layout, state, created_at, updated_at
		FROM checkouts WHERE state = ? ORDER BY created_at, id`, string(StateActive))
	if err != nil {
		return nil, fmt.Errorf("list active checkouts: %w", err)
	}
	defer rows.Close()
	var checkouts []Checkout
	for rows.Next() {
		var checkout Checkout
		var state string
		if err := rows.Scan(
			&checkout.ID, &checkout.Owner.Hub, &checkout.Owner.UserID,
			&checkout.Root, &checkout.Layout, &state,
			&checkout.CreatedAt, &checkout.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("list active checkouts: scan: %w", err)
		}
		checkout.State = State(state)
		checkouts = append(checkouts, checkout)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list active checkouts: iterate: %w", err)
	}
	return checkouts, nil
}

func (r *Repo) ListScanCandidates(ctx context.Context, checkoutID string) ([]ScanCandidate, error) {
	rows, err := r.ro.QueryContext(ctx, `SELECT checkout_id, relative_path,
		file_id, observed_size, observed_mtime, observed_identity, observed_sha256, state,
		first_observed_at, last_observed_at
		FROM checkout_scan_candidates WHERE checkout_id = ? ORDER BY relative_path`, checkoutID)
	if err != nil {
		return nil, fmt.Errorf("list checkout scan candidates: %w", err)
	}
	defer rows.Close()
	var candidates []ScanCandidate
	for rows.Next() {
		candidate, err := scanScanCandidate(rows)
		if err != nil {
			return nil, fmt.Errorf("list checkout scan candidates: scan: %w", err)
		}
		candidates = append(candidates, candidate)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list checkout scan candidates: iterate: %w", err)
	}
	return candidates, nil
}

// ObserveScanCandidate records one metadata observation. It returns true only
// when the same file identity, size, and modification time have remained
// unchanged for at least settleInterval across separate scanner passes.
func (r *Repo) ObserveScanCandidate(
	ctx context.Context,
	candidate ScanCandidate,
	settleInterval time.Duration,
	now time.Time,
) (bool, error) {
	if settleInterval < 0 {
		return false, fmt.Errorf("observe checkout scan candidate: %w: settle interval is negative",
			errs.ErrInvalidArgument)
	}
	now = now.UTC()
	candidate.ObservedMTime = candidate.ObservedMTime.UTC()
	tx, err := r.rw.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("observe checkout scan candidate: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	existing, err := scanScanCandidate(tx.QueryRowContext(ctx, `SELECT checkout_id,
		relative_path, file_id, observed_size, observed_mtime, observed_identity, observed_sha256,
		state, first_observed_at, last_observed_at
		FROM checkout_scan_candidates WHERE checkout_id = ? AND relative_path = ?`,
		candidate.CheckoutID, candidate.RelativePath))
	switch {
	case errors.Is(err, sql.ErrNoRows):
		_, err = tx.ExecContext(ctx, `INSERT INTO checkout_scan_candidates (
			checkout_id, relative_path, file_id, observed_size, observed_mtime,
			observed_identity, observed_sha256, state, first_observed_at, last_observed_at
		) VALUES (?, ?, NULLIF(?, ''), ?, ?, ?, NULL, ?, ?, ?)`,
			candidate.CheckoutID, candidate.RelativePath, candidate.FileID,
			candidate.ObservedSize, candidate.ObservedMTime, candidate.ObservedIdentity,
			string(ScanCandidateSettling), now, now)
		if err != nil {
			return false, fmt.Errorf("observe checkout scan candidate: insert: %w", err)
		}
	case err != nil:
		return false, fmt.Errorf("observe checkout scan candidate: load: %w", err)
	case sameScanObservation(existing, candidate) && existing.State == ScanCandidateSettling:
		_, err = tx.ExecContext(ctx, `UPDATE checkout_scan_candidates
			SET last_observed_at = ?
			WHERE checkout_id = ? AND relative_path = ?`,
			now, candidate.CheckoutID, candidate.RelativePath)
		if err != nil {
			return false, fmt.Errorf("observe checkout scan candidate: refresh: %w", err)
		}
		if err := tx.Commit(); err != nil {
			return false, fmt.Errorf("observe checkout scan candidate: commit refresh: %w", err)
		}
		return !now.Before(existing.FirstObserved.Add(settleInterval)), nil
	case sameScanObservation(existing, candidate) && existing.State == ScanCandidatePending:
		if err := tx.Commit(); err != nil {
			return false, fmt.Errorf("observe checkout scan candidate: commit pending: %w", err)
		}
		return false, nil
	default:
		_, err = tx.ExecContext(ctx, `UPDATE checkout_scan_candidates SET
			file_id = NULLIF(?, ''), observed_size = ?, observed_mtime = ?,
			observed_identity = ?, observed_sha256 = NULL, state = ?, first_observed_at = ?,
			last_observed_at = ?
			WHERE checkout_id = ? AND relative_path = ?`,
			candidate.FileID, candidate.ObservedSize, candidate.ObservedMTime,
			candidate.ObservedIdentity,
			string(ScanCandidateSettling), now, now,
			candidate.CheckoutID, candidate.RelativePath)
		if err != nil {
			return false, fmt.Errorf("observe checkout scan candidate: reset: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("observe checkout scan candidate: commit: %w", err)
	}
	return false, nil
}

func (r *Repo) FinalizeTrackedScanCandidate(
	ctx context.Context,
	candidate ScanCandidate,
	sha256 string,
	now time.Time,
) (EntryState, error) {
	tx, err := r.rw.BeginTx(ctx, nil)
	if err != nil {
		return "", fmt.Errorf("finalize tracked checkout change: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	res, err := tx.ExecContext(ctx, `UPDATE checkout_entries SET
		observed_size = ?, observed_mtime = ?, observed_identity = ?, observed_sha256 = ?,
		state = CASE WHEN base_sha256 = ? THEN ? ELSE ? END,
		last_error = NULL, updated_at = ?
		WHERE checkout_id = ? AND file_id = ? AND relative_path = ?
		AND EXISTS (
			SELECT 1 FROM checkout_scan_candidates c
			WHERE c.checkout_id = checkout_entries.checkout_id
			  AND c.relative_path = checkout_entries.relative_path
			  AND c.file_id = checkout_entries.file_id
			  AND c.state = ?
		  AND c.observed_size = ? AND c.observed_mtime = ? AND c.observed_identity = ?
		)`,
		candidate.ObservedSize, candidate.ObservedMTime.UTC(), candidate.ObservedIdentity,
		sha256, sha256,
		string(EntryClean), string(EntryPending), now.UTC(),
		candidate.CheckoutID, candidate.FileID, candidate.RelativePath,
		string(ScanCandidateSettling), candidate.ObservedSize, candidate.ObservedMTime.UTC(),
		candidate.ObservedIdentity)
	if err != nil {
		return "", fmt.Errorf("finalize tracked checkout change: update entry: %w", err)
	}
	count, err := res.RowsAffected()
	if err != nil {
		return "", fmt.Errorf("finalize tracked checkout change: rows affected: %w", err)
	}
	if count == 0 {
		return "", fmt.Errorf("finalize tracked checkout change: %w: candidate changed",
			errs.ErrContentConflict)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM checkout_scan_candidates
		WHERE checkout_id = ? AND relative_path = ?`,
		candidate.CheckoutID, candidate.RelativePath); err != nil {
		return "", fmt.Errorf("finalize tracked checkout change: clear candidate: %w", err)
	}
	var state EntryState
	if err := tx.QueryRowContext(ctx, `SELECT state FROM checkout_entries
		WHERE checkout_id = ? AND file_id = ?`, candidate.CheckoutID, candidate.FileID).Scan(&state); err != nil {
		return "", fmt.Errorf("finalize tracked checkout change: read state: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return "", fmt.Errorf("finalize tracked checkout change: commit: %w", err)
	}
	return state, nil
}

func (r *Repo) FinalizeUntrackedScanCandidate(
	ctx context.Context,
	candidate ScanCandidate,
	sha256 string,
	now time.Time,
) error {
	res, err := r.rw.ExecContext(ctx, `UPDATE checkout_scan_candidates SET
		observed_sha256 = ?, state = ?, last_observed_at = ?
		WHERE checkout_id = ? AND relative_path = ? AND file_id IS NULL
		  AND state = ? AND observed_size = ? AND observed_mtime = ?
		  AND observed_identity = ?`,
		sha256, string(ScanCandidatePending), now.UTC(),
		candidate.CheckoutID, candidate.RelativePath, string(ScanCandidateSettling),
		candidate.ObservedSize, candidate.ObservedMTime.UTC(), candidate.ObservedIdentity)
	if err != nil {
		return fmt.Errorf("finalize untracked checkout change: %w", err)
	}
	count, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("finalize untracked checkout change: rows affected: %w", err)
	}
	if count == 0 {
		return fmt.Errorf("finalize untracked checkout change: %w: candidate changed",
			errs.ErrContentConflict)
	}
	return nil
}

func (r *Repo) MarkEntryMissing(
	ctx context.Context,
	checkoutID string,
	fileID string,
	now time.Time,
) error {
	tx, err := r.rw.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("mark checkout entry missing: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `DELETE FROM checkout_scan_candidates
		WHERE checkout_id = ? AND file_id = ?`, checkoutID, fileID); err != nil {
		return fmt.Errorf("mark checkout entry missing: clear candidate: %w", err)
	}
	res, err := tx.ExecContext(ctx, `UPDATE checkout_entries SET state = ?,
		last_error = NULL, updated_at = ? WHERE checkout_id = ? AND file_id = ?`,
		string(EntryMissing), now.UTC(), checkoutID, fileID)
	if err != nil {
		return fmt.Errorf("mark checkout entry missing: update: %w", err)
	}
	count, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("mark checkout entry missing: rows affected: %w", err)
	}
	if count == 0 {
		return fmt.Errorf("mark checkout entry missing: %w", errs.ErrNotFound)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("mark checkout entry missing: commit: %w", err)
	}
	return nil
}

func (r *Repo) MarkEntryScanError(
	ctx context.Context,
	checkoutID string,
	fileID string,
	message string,
	now time.Time,
) error {
	res, err := r.rw.ExecContext(ctx, `UPDATE checkout_entries SET state = ?,
		last_error = ?, updated_at = ? WHERE checkout_id = ? AND file_id = ?`,
		string(EntryError), message, now.UTC(), checkoutID, fileID)
	if err != nil {
		return fmt.Errorf("mark checkout entry scan error: %w", err)
	}
	count, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("mark checkout entry scan error: rows affected: %w", err)
	}
	if count == 0 {
		return fmt.Errorf("mark checkout entry scan error: %w", errs.ErrNotFound)
	}
	return nil
}

func (r *Repo) DeleteScanCandidate(ctx context.Context, checkoutID, relativePath string) error {
	if _, err := r.rw.ExecContext(ctx, `DELETE FROM checkout_scan_candidates
		WHERE checkout_id = ? AND relative_path = ?`, checkoutID, relativePath); err != nil {
		return fmt.Errorf("delete checkout scan candidate: %w", err)
	}
	return nil
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanScanCandidate(row rowScanner) (ScanCandidate, error) {
	var candidate ScanCandidate
	var fileID, sha256 sql.NullString
	var state string
	err := row.Scan(
		&candidate.CheckoutID, &candidate.RelativePath, &fileID,
		&candidate.ObservedSize, &candidate.ObservedMTime, &candidate.ObservedIdentity,
		&sha256, &state,
		&candidate.FirstObserved, &candidate.LastObserved,
	)
	candidate.FileID = fileID.String
	candidate.ObservedSHA256 = sha256.String
	candidate.State = ScanCandidateState(state)
	return candidate, err
}

func sameScanObservation(left, right ScanCandidate) bool {
	return left.FileID == right.FileID &&
		left.ObservedSize == right.ObservedSize &&
		left.ObservedMTime.Equal(right.ObservedMTime) &&
		left.ObservedIdentity == right.ObservedIdentity
}
