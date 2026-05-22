package brokerexec

import (
	"time"

	"go.kenn.io/fotobank/internal/owners"
	"go.kenn.io/fotobank/internal/share"
)

// schemaVersion is bumped on incompatible changes to the wire JSON.
// Kept as a single int so a broker CLI can fail fast on mismatch.
const schemaVersion = 1

type publishRequest struct {
	SchemaVersion int          `json:"schema_version"`
	Operation     string       `json:"operation"`
	Scope         publishScope `json:"scope"`
}

type publishScope struct {
	UUID          string           `json:"uuid"`
	Owner         publishPrincipal `json:"owner"`
	Grantee       publishPrincipal `json:"grantee"`
	AllowDownload bool             `json:"allow_download"`
	ExpiresAt     *time.Time       `json:"expires_at,omitempty"`
	Label         string           `json:"label"`
}

type publishPrincipal struct {
	Hub    string `json:"hub"`
	UserID string `json:"user_id"`
}

type revokeRequest struct {
	SchemaVersion int    `json:"schema_version"`
	Operation     string `json:"operation"`
	UUID          string `json:"uuid"`
}

func newPublishRequest(s share.Scope) publishRequest {
	return publishRequest{
		SchemaVersion: schemaVersion,
		Operation:     "publish",
		Scope: publishScope{
			UUID:          s.UUID,
			Owner:         toPublishPrincipal(s.Owner),
			Grantee:       toPublishPrincipal(s.Grantee),
			AllowDownload: s.AllowDownload,
			ExpiresAt:     s.ExpiresAt,
			Label:         s.Label,
		},
	}
}

func toPublishPrincipal(p owners.Principal) publishPrincipal {
	return publishPrincipal{Hub: p.Hub, UserID: p.UserID}
}

func newRevokeRequest(uuid string) revokeRequest {
	return revokeRequest{
		SchemaVersion: schemaVersion,
		Operation:     "revoke",
		UUID:          uuid,
	}
}
