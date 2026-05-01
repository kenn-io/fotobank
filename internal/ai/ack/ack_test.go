package ack_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wesm/fotobank/internal/ai/ack"
	"github.com/wesm/fotobank/internal/owners"
	"github.com/wesm/fotobank/internal/testutil"
)

func TestNotAcknowledgedByDefault(t *testing.T) {
	rw, ro := testutil.OpenTestDBPair(t)
	a := ack.New(rw, ro)
	got, err := a.IsAcknowledged(context.Background(),
		owners.Principal{Hub: "local", UserID: "alice"})
	require.NoError(t, err)
	require.False(t, got)
}

func TestAcknowledgePersists(t *testing.T) {
	rw, ro := testutil.OpenTestDBPair(t)
	a := ack.New(rw, ro)
	p := owners.Principal{Hub: "local", UserID: "alice"}

	require.NoError(t, a.Acknowledge(context.Background(), p))

	got, err := a.IsAcknowledged(context.Background(), p)
	require.NoError(t, err)
	require.True(t, got)
}

func TestAcknowledgeIsIdempotent(t *testing.T) {
	rw, ro := testutil.OpenTestDBPair(t)
	a := ack.New(rw, ro)
	p := owners.Principal{Hub: "local", UserID: "alice"}
	require.NoError(t, a.Acknowledge(context.Background(), p))
	require.NoError(t, a.Acknowledge(context.Background(), p))
}

func TestAcknowledgementIsPerPrincipal(t *testing.T) {
	rw, ro := testutil.OpenTestDBPair(t)
	a := ack.New(rw, ro)
	require.NoError(t, a.Acknowledge(context.Background(),
		owners.Principal{Hub: "local", UserID: "alice"}))
	got, err := a.IsAcknowledged(context.Background(),
		owners.Principal{Hub: "local", UserID: "bob"})
	require.NoError(t, err)
	require.False(t, got)
}
