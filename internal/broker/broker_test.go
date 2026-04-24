package broker_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wesm/fotobank/internal/broker"
	"github.com/wesm/fotobank/internal/share"
)

func TestNoopBrokerImplementsInterface(t *testing.T) {
	r := require.New(t)
	var _ broker.BrokerClient = &broker.NoopBroker{}
	c := &broker.NoopBroker{}
	r.NoError(c.PublishScope(context.Background(), share.Scope{UUID: "u"}))
	r.NoError(c.RevokeScope(context.Background(), "u"))
}

func TestBrokerSentinelsAreDistinct(t *testing.T) {
	r := require.New(t)
	r.NotErrorIs(broker.ErrBrokerTransient, broker.ErrBrokerPermanent)
	r.NotErrorIs(broker.ErrBrokerPermanent, broker.ErrBrokerTransient)
}
