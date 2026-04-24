// Package broker defines the BrokerClient interface that share.Repo's
// outbox worker uses to register / revoke scopes with an external
// identity-and-grant broker, and ships the production default
// implementation (NoopBroker). Tests use the scripted fake under
// brokertest/.
package broker

import (
	"context"
	"errors"

	"github.com/wesm/fotobank/internal/share"
)

// BrokerClient is the external broker's wire API as seen by fotobank.
// Implementations MUST be idempotent: the outbox worker may retry any
// method after crash or transient failure, and may call RevokeScope on
// a UUID the broker has never seen (success is the correct response).
//
// Error semantics:
//   - errors.Is(err, ErrBrokerTransient) → the worker retries with
//     exponential backoff; the row stays in its current phase.
//   - errors.Is(err, ErrBrokerPermanent) → the worker flips the row to
//     StatusFailed; the owner must Retry or Revoke.
//   - context errors pass through unchanged; the worker aborts this
//     iteration without touching the row.
//   - any other error is treated as transient (err on the side of
//     retry; a permanent misclassification would orphan the row).
type BrokerClient interface {
	// PublishScope registers the scope with the broker and creates the
	// grantee-side grant. Implementations must not inspect scope_media
	// membership; the broker is deliberately ignorant of fotobank's
	// per-scope media set.
	PublishScope(ctx context.Context, s share.Scope) error

	// RevokeScope revokes the grant at the broker. Must treat "never
	// heard of this UUID" as success so the worker can drive pending
	// rows to revoked_remote without first confirming registration.
	RevokeScope(ctx context.Context, uuid string) error
}

var (
	ErrBrokerTransient = errors.New("broker: transient error")
	ErrBrokerPermanent = errors.New("broker: permanent error")
)
