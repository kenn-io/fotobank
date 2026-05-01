// Package ack persists per-principal acknowledgements that gate AI
// worker activity. v1 has one kind: hidden-photo processing.
package ack

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/wesm/fotobank/internal/owners"
)

// SettingKey is the canonical key in user_settings for the
// hidden-processing acknowledgement.
const SettingKey = "ai.hidden_processing_acknowledged_at"

// Store reads and writes the per-principal hidden-processing
// acknowledgement via the user_settings table.
type Store struct {
	rw *sql.DB
	ro *sql.DB
}

// New constructs a Store. rw must be the writer pool and ro the
// reader pool; lookups are routed to ro.
func New(rw, ro *sql.DB) *Store { return &Store{rw: rw, ro: ro} }

// IsAcknowledged returns true if any acknowledgement row exists for p.
func (s *Store) IsAcknowledged(ctx context.Context, p owners.Principal) (bool, error) {
	row := s.ro.QueryRowContext(ctx, `
		SELECT 1 FROM user_settings
		 WHERE principal_hub=? AND principal_user_id=? AND key=?`,
		p.Hub, p.UserID, SettingKey)
	var n int
	switch err := row.Scan(&n); {
	case errors.Is(err, sql.ErrNoRows):
		return false, nil
	case err != nil:
		return false, fmt.Errorf("scan: %w", err)
	}
	return true, nil
}

// Acknowledge persists the acknowledgement for p. Idempotent — re-ack
// updates updated_at but doesn't error.
func (s *Store) Acknowledge(ctx context.Context, p owners.Principal) error {
	now := time.Now().UTC()
	value := now.Format(time.RFC3339)
	_, err := s.rw.ExecContext(ctx, `
		INSERT INTO user_settings(principal_hub, principal_user_id, key, value, updated_at)
		VALUES (?,?,?,?,?)
		ON CONFLICT(principal_hub, principal_user_id, key) DO UPDATE SET
		  value=excluded.value, updated_at=excluded.updated_at`,
		p.Hub, p.UserID, SettingKey, value, now)
	if err != nil {
		return fmt.Errorf("ack upsert: %w", err)
	}
	return nil
}
