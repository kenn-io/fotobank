package brokerexec

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"go.kenn.io/fotobank/internal/owners"
	"go.kenn.io/fotobank/internal/share"
)

func TestNewPublishRequestShape(t *testing.T) {
	r := require.New(t)
	expires := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	s := share.Scope{
		UUID:          "scope-uuid",
		Owner:         owners.Principal{Hub: "h", UserID: "alice"},
		Grantee:       owners.Principal{Hub: "h", UserID: "bob"},
		AllowDownload: true,
		Label:         "Trip",
		ExpiresAt:     &expires,
	}
	req := newPublishRequest(s)
	blob, err := json.Marshal(req)
	r.NoError(err)

	var got map[string]any
	r.NoError(json.Unmarshal(blob, &got))
	r.EqualValues(1, got["schema_version"])
	r.Equal("publish", got["operation"])

	scope := got["scope"].(map[string]any)
	r.Equal("scope-uuid", scope["uuid"])
	r.Equal(true, scope["allow_download"])
	r.Equal("Trip", scope["label"])
	r.Equal("2026-05-01T00:00:00Z", scope["expires_at"])

	owner := scope["owner"].(map[string]any)
	r.Equal("h", owner["hub"])
	r.Equal("alice", owner["user_id"])
	grantee := scope["grantee"].(map[string]any)
	r.Equal("bob", grantee["user_id"])

	// Membership must NOT be sent.
	for _, k := range []string{"target_type", "album_id", "media_ids"} {
		_, has := scope[k]
		r.Falsef(has, "scope must not contain %q", k)
	}
}

func TestNewPublishRequestOmitsExpiresAtWhenNil(t *testing.T) {
	s := share.Scope{
		UUID:    "x",
		Owner:   owners.Principal{Hub: "h", UserID: "a"},
		Grantee: owners.Principal{Hub: "h", UserID: "b"},
	}
	blob, err := json.Marshal(newPublishRequest(s))
	require.NoError(t, err)

	var got map[string]any
	require.NoError(t, json.Unmarshal(blob, &got))
	scope := got["scope"].(map[string]any)
	_, has := scope["expires_at"]
	require.False(t, has, "expires_at must be omitted when nil")
}

func TestNewRevokeRequestShape(t *testing.T) {
	r := require.New(t)
	blob, err := json.Marshal(newRevokeRequest("abc"))
	r.NoError(err)

	var got map[string]any
	r.NoError(json.Unmarshal(blob, &got))
	r.EqualValues(1, got["schema_version"])
	r.Equal("revoke", got["operation"])
	r.Equal("abc", got["uuid"])

	_, has := got["scope"]
	r.False(has, "revoke payload must not include a scope object")
}
