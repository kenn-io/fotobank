package ai_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wesm/fotobank/internal/owners"
	aiservice "github.com/wesm/fotobank/internal/service/ai"
	"github.com/wesm/fotobank/internal/testutil"
)

type stubProbe struct {
	err error
}

func (s stubProbe) Probe(_ context.Context) error { return s.err }

func TestHealthDisabled(t *testing.T) {
	svc, _ := makeServiceWithDB(t)
	h := svc.Health(context.Background(), owners.Principal{Hub: "local", UserID: "alice"},
		aiservice.HealthInput{Enabled: false, Probe: stubProbe{}})
	require.False(t, h.Enabled)
	require.Equal(t, "config_disabled", h.PausedReason)
}

func TestHealthAcknowledgementRequired(t *testing.T) {
	svc, _ := makeServiceWithDB(t)
	h := svc.Health(context.Background(), owners.Principal{Hub: "local", UserID: "alice"},
		aiservice.HealthInput{Enabled: true, Probe: stubProbe{}})
	require.True(t, h.Enabled)
	require.Equal(t, "acknowledgement_required", h.PausedReason)
}

func TestHealthReachable(t *testing.T) {
	r := require.New(t)
	svc, rw := makeServiceWithDB(t)
	owner := testutil.SeedOwner(t, rw, "local", "alice")
	r.NoError(svc.Acknowledge(context.Background(), owner))

	h := svc.Health(context.Background(), owner, aiservice.HealthInput{
		Enabled: true, Probe: stubProbe{},
	})
	r.Empty(h.PausedReason)
	r.True(h.Vision.Reachable)
	r.Equal("m|tags-v1|ip", h.Tag.ActiveFingerprint)
}

func TestHealthUnreachable(t *testing.T) {
	r := require.New(t)
	svc, rw := makeServiceWithDB(t)
	owner := testutil.SeedOwner(t, rw, "local", "alice")
	r.NoError(svc.Acknowledge(context.Background(), owner))

	h := svc.Health(context.Background(), owner, aiservice.HealthInput{
		Enabled: true, Probe: stubProbe{err: errors.New("connection refused")},
	})
	r.False(h.Vision.Reachable)
	r.Equal("connection refused", h.Vision.LastError)
}
