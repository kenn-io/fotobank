// Package owners defines the Principal / Owner types and a SQLite-backed
// repository for managing the principals who own media on this deployment.
package owners

import "time"

// Principal identifies a user by their hub (identity provider) and user ID
// within that hub. The zero value represents "no principal".
type Principal struct {
	Hub    string
	UserID string
}

// String returns a stable textual representation of the principal.
func (p Principal) String() string { return p.Hub + ":" + p.UserID }

// IsZero reports whether the principal is the zero value.
func (p Principal) IsZero() bool { return p.Hub == "" && p.UserID == "" }

// Owner is a principal registered as an owner of media on this deployment,
// along with their storage key and optional display handle.
type Owner struct {
	Principal     Principal
	StorageKey    string
	DisplayHandle string
	CreatedAt     time.Time
}
