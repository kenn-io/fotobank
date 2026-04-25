package brokerexec

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wesm/fotobank/internal/owners"
	"github.com/wesm/fotobank/internal/share"
)

func TestNewPublishRequestShape(t *testing.T) {
	require := require.New(t)
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
	require.NoError(err)

	var got map[string]any
	require.NoError(json.Unmarshal(blob, &got))
	require.EqualValues(1, got["schema_version"])
	require.Equal("publish", got["operation"])

	scope := got["scope"].(map[string]any)
	require.Equal("scope-uuid", scope["uuid"])
	require.Equal(true, scope["allow_download"])
	require.Equal("Trip", scope["label"])
	require.Equal("2026-05-01T00:00:00Z", scope["expires_at"])

	owner := scope["owner"].(map[string]any)
	require.Equal("h", owner["hub"])
	require.Equal("alice", owner["user_id"])
	grantee := scope["grantee"].(map[string]any)
	require.Equal("bob", grantee["user_id"])

	// Membership must NOT be sent.
	for _, k := range []string{"target_type", "album_id", "media_ids"} {
		_, has := scope[k]
		require.Falsef(has, "scope must not contain %q", k)
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
	require := require.New(t)
	blob, err := json.Marshal(newRevokeRequest("abc"))
	require.NoError(err)

	var got map[string]any
	require.NoError(json.Unmarshal(blob, &got))
	require.EqualValues(1, got["schema_version"])
	require.Equal("revoke", got["operation"])
	require.Equal("abc", got["uuid"])

	_, has := got["scope"]
	require.False(has, "revoke payload must not include a scope object")
}
