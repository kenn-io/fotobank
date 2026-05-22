package broker

import (
	"context"

	"go.kenn.io/fotobank/internal/share"
)

// NoopBroker is the production default for single-owner deployments
// without a real broker. Both methods return nil immediately. The
// outbox worker treats that as success, so scopes walk the state
// machine pending→active and revoking→revoked_remote cleanly even
// though nothing is happening remotely. Operators wiring a real
// broker replace this with another BrokerClient implementation.
type NoopBroker struct{}

func (NoopBroker) PublishScope(ctx context.Context, _ share.Scope) error { return nil }
func (NoopBroker) RevokeScope(ctx context.Context, _ string) error       { return nil }
