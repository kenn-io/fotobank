package share_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wesm/fotobank/internal/share"
)

func TestBrokerStatusConstants(t *testing.T) {
	r := require.New(t)
	r.Equal(share.StatusPending, share.BrokerStatus("pending"))
	r.Equal(share.StatusActive, share.BrokerStatus("active"))
	r.Equal(share.StatusFailed, share.BrokerStatus("failed"))
	r.Equal(share.StatusRevoking, share.BrokerStatus("revoking"))
	r.Equal(share.StatusRevokedRemote, share.BrokerStatus("revoked_remote"))
}

func TestTargetTypeConstants(t *testing.T) {
	r := require.New(t)
	r.Equal(share.TargetAlbumLive, share.TargetType("album_live"))
	r.Equal(share.TargetMediaSet, share.TargetType("media_set"))
}

func TestSentinelsAreDistinct(t *testing.T) {
	r := require.New(t)
	all := []error{
		share.ErrInvalidGrantee,
		share.ErrInvalidLabel,
		share.ErrInvalidMediaSet,
		share.ErrInvalidTargetCombo,
		share.ErrAlbumEmpty,
		share.ErrScopeAlreadyRevoked,
		share.ErrRetryNotApplicable,
		share.ErrAlbumHasLiveScopes,
	}
	seen := map[string]bool{}
	for _, e := range all {
		r.False(seen[e.Error()], "duplicate sentinel: %s", e.Error())
		seen[e.Error()] = true
	}
}

func TestLimitsMatchSpec(t *testing.T) {
	r := require.New(t)
	r.Equal(200, share.LabelMaxLen)
	r.Equal(1000, share.MediaSetMaxLen)
	r.Equal(10, share.MaxBrokerAttempts)
	r.Equal(255, share.PrincipalFieldMaxLen)
}

func TestParseStatusFilter(t *testing.T) {
	r := require.New(t)
	cases := []struct {
		name string
		in   string
		want []share.BrokerStatus
	}{
		{"empty", "", nil},
		{"single", "pending", []share.BrokerStatus{share.StatusPending}},
		{"multi", "pending,failed,revoking",
			[]share.BrokerStatus{share.StatusPending, share.StatusFailed, share.StatusRevoking}},
		{"whitespace", "  pending  , , failed  ",
			[]share.BrokerStatus{share.StatusPending, share.StatusFailed}},
		{"whitespace-only", " , , ", []share.BrokerStatus{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := share.ParseStatusFilter(tc.in)
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}

	_, err := share.ParseStatusFilter("bogus")
	r.Error(err)
	r.Contains(err.Error(), "bogus")

	_, err = share.ParseStatusFilter("pending,bogus")
	r.Error(err)
	r.Contains(err.Error(), "bogus")
}
