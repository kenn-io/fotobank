package brokertest_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wesm/fotobank/internal/broker"
	"github.com/wesm/fotobank/internal/broker/brokertest"
	"github.com/wesm/fotobank/internal/share"
)

func TestFakeZeroValueBehavesLikeNoop(t *testing.T) {
	r := require.New(t)
	f := &brokertest.Fake{}
	r.NoError(f.PublishScope(context.Background(), share.Scope{UUID: "u1"}))
	r.NoError(f.RevokeScope(context.Background(), "u1"))
	r.Equal([]string{"u1"}, f.ObservedPublishes())
	r.Equal([]string{"u1"}, f.ObservedRevokes())
}

func TestFakeQueuedErrorIsReturnedOnce(t *testing.T) {
	r := require.New(t)
	f := &brokertest.Fake{}
	f.QueuePublishError("u1", broker.ErrBrokerTransient)
	err := f.PublishScope(context.Background(), share.Scope{UUID: "u1"})
	r.ErrorIs(err, broker.ErrBrokerTransient)
	// Second call falls back to success — queue is consumed once.
	err = f.PublishScope(context.Background(), share.Scope{UUID: "u1"})
	r.NoError(err)
}

func TestFakePermanentThenTransientQueue(t *testing.T) {
	r := require.New(t)
	f := &brokertest.Fake{}
	f.QueueRevokeError("u1", broker.ErrBrokerPermanent)
	f.QueueRevokeError("u1", broker.ErrBrokerTransient)
	err := f.RevokeScope(context.Background(), "u1")
	r.ErrorIs(err, broker.ErrBrokerPermanent)
	err = f.RevokeScope(context.Background(), "u1")
	r.ErrorIs(err, broker.ErrBrokerTransient)
	err = f.RevokeScope(context.Background(), "u1")
	r.NoError(err)
}

func TestFakeUnrelatedUUIDIsSuccess(t *testing.T) {
	r := require.New(t)
	f := &brokertest.Fake{}
	f.QueuePublishError("u1", errors.New("boom"))
	err := f.PublishScope(context.Background(), share.Scope{UUID: "u2"})
	r.NoError(err)
}

func TestFakeImplementsBrokerClient(t *testing.T) {
	var _ broker.BrokerClient = &brokertest.Fake{}
}
