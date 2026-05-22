package ack_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"go.kenn.io/fotobank/internal/ai/ack"
	"go.kenn.io/fotobank/internal/owners"
	"go.kenn.io/fotobank/internal/testutil"
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
