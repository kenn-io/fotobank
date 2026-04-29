// Package share defines the scope / scope_media domain types and the
// validation / state-machine sentinels used by the share repo, service,
// HTTP transport, CLI, and outbox worker.
package share

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/wesm/fotobank/internal/owners"
)

// TargetType is the binding category of a scope. Stored verbatim in
// scopes.target_type.
type TargetType string

const (
	TargetAlbumLive TargetType = "album_live"
	TargetMediaSet  TargetType = "media_set"
)

// BrokerStatus is the broker-side state of a scope. Stored verbatim in
// scopes.broker_status.
type BrokerStatus string

const (
	StatusPending       BrokerStatus = "pending"
	StatusActive        BrokerStatus = "active"
	StatusFailed        BrokerStatus = "failed"
	StatusRevoking      BrokerStatus = "revoking"
	StatusRevokedRemote BrokerStatus = "revoked_remote"
)

// Scope mirrors a row in the scopes table. Nullable DB columns are
// *-typed so "never set" stays distinguishable from "set to zero".
type Scope struct {
	UUID          string
	Owner         owners.Principal
	Grantee       owners.Principal
	TargetType    TargetType
	TargetAlbumID *string
	AllowDownload bool
	Label         string
	CreatedAt     time.Time
	ExpiresAt     *time.Time

	RevokedAt *time.Time

	BrokerStatus        BrokerStatus
	BrokerRegisteredAt  *time.Time
	BrokerGrantedAt     *time.Time
	BrokerRevokedAt     *time.Time
	BrokerLastError     string
	BrokerAttempts      int
	BrokerNextAttemptAt *time.Time
}

// ScopeDetail pairs a Scope with its frozen media_set membership.
// MediaIDs is always empty for TargetAlbumLive scopes.
type ScopeDetail struct {
	Scope
	MediaIDs []string
}

// TargetSummary is a UI-friendly summary of a Scope's target. The
// helper that builds these lives in the service tier
// (ShareService.PopulateTargetSummary) because it joins over album
// names and scope_media counts; the domain package owns only the type.
type TargetSummary struct {
	Label     string
	ItemCount *int
}

// ScopeFilter narrows Repo.ListByOwner / ShareService.List. An empty
// Status slice + IncludeSettled=false means "owner-actionable rows
// only"; the SQL filter hides broker_status = 'revoked_remote'. A
// non-empty Status slice is an exact broker_status IN (...) filter and
// ignores IncludeSettled.
type ScopeFilter struct {
	AlbumID        string
	Grantee        owners.Principal
	Status         []BrokerStatus
	IncludeSettled bool
	Limit          int
	Offset         int
}

const (
	// LabelMaxLen caps scope labels. Service validation enforces this.
	LabelMaxLen = 200
	// MediaSetMaxLen caps deduped media_set membership at mint.
	MediaSetMaxLen = 1000
	// MaxBrokerAttempts is the retry ceiling. After this many attempts
	// against the broker, the worker flips the row to StatusFailed and
	// leaves it there for the owner to Retry or Revoke.
	MaxBrokerAttempts = 10
	// PrincipalFieldMaxLen bounds the hub and user_id fields of a
	// grantee principal. Keeps the broker from receiving garbage.
	PrincipalFieldMaxLen = 255
)

var (
	ErrInvalidGrantee      = errors.New("share: grantee principal is empty, oversized, or equal to caller")
	ErrInvalidLabel        = errors.New("share: label exceeds 200 chars")
	ErrInvalidMediaSet     = errors.New("share: media_set must be 1..1000 unique media ids")
	ErrInvalidTargetCombo  = errors.New("share: target_type does not match payload")
	ErrAlbumEmpty          = errors.New("share: cannot share an empty album_live")
	ErrScopeAlreadyRevoked = errors.New("share: scope is already revoked")
	ErrRetryNotApplicable  = errors.New("share: retry only applies to failed scopes")
	ErrAlbumHasLiveScopes  = errors.New("share: album has outstanding broker grants; revoke them first")
)

// ParseStatusFilter parses a comma-separated BrokerStatus list.
// Whitespace-only tokens are skipped; empty input returns (nil, nil).
// Unknown statuses return an error naming the offending token —
// callers wrap with their transport-specific error type (huma.Error400,
// CLI newUsageError, etc.).
func ParseStatusFilter(raw string) ([]BrokerStatus, error) {
	if raw == "" {
		return nil, nil
	}
	parts := strings.Split(raw, ",")
	out := make([]BrokerStatus, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		switch BrokerStatus(p) {
		case StatusPending, StatusActive, StatusFailed,
			StatusRevoking, StatusRevokedRemote:
			out = append(out, BrokerStatus(p))
		default:
			return nil, fmt.Errorf("unknown status: %s", p)
		}
	}
	return out, nil
}
