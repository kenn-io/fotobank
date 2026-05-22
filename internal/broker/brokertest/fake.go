// Package brokertest provides a scripted broker.BrokerClient used by
// share + shareworker tests. Lives under internal/broker/ so
// production code cannot accidentally import it.
package brokertest

import (
	"context"
	"sync"

	"go.kenn.io/fotobank/internal/broker"
	"go.kenn.io/fotobank/internal/share"
)

// Fake is a scripted BrokerClient. Tests queue per-UUID errors via
// QueuePublishError / QueueRevokeError; each queued error is returned
// once by the matching method call (FIFO per UUID), after which calls
// for that UUID fall back to success. Calls for UUIDs with no queue
// are always success. All calls are recorded so assertions can check
// order and count.
//
// The zero value is usable; no constructor is needed.
type Fake struct {
	mu            sync.Mutex
	publishQueue  map[string][]error
	revokeQueue   map[string][]error
	publishesSeen []string
	revokesSeen   []string
}

var _ broker.BrokerClient = (*Fake)(nil)

// QueuePublishError appends err to the publish queue for uuid. A
// subsequent PublishScope(s) where s.UUID == uuid returns err instead
// of success, and the queue entry is consumed. Call multiple times to
// script a sequence of responses.
func (f *Fake) QueuePublishError(uuid string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.publishQueue == nil {
		f.publishQueue = map[string][]error{}
	}
	f.publishQueue[uuid] = append(f.publishQueue[uuid], err)
}

// QueueRevokeError is the revoke-side equivalent.
func (f *Fake) QueueRevokeError(uuid string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.revokeQueue == nil {
		f.revokeQueue = map[string][]error{}
	}
	f.revokeQueue[uuid] = append(f.revokeQueue[uuid], err)
}

// ObservedPublishes returns a snapshot of every UUID PublishScope has
// been called with, in call order.
func (f *Fake) ObservedPublishes() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.publishesSeen))
	copy(out, f.publishesSeen)
	return out
}

// ObservedRevokes is the revoke-side equivalent.
func (f *Fake) ObservedRevokes() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.revokesSeen))
	copy(out, f.revokesSeen)
	return out
}

// PublishScope consumes one queued error for s.UUID if present and
// returns it; otherwise returns nil.
func (f *Fake) PublishScope(ctx context.Context, s share.Scope) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.publishesSeen = append(f.publishesSeen, s.UUID)
	return popFront(f.publishQueue, s.UUID)
}

// RevokeScope consumes one queued error for uuid if present and
// returns it; otherwise returns nil.
func (f *Fake) RevokeScope(ctx context.Context, uuid string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.revokesSeen = append(f.revokesSeen, uuid)
	return popFront(f.revokeQueue, uuid)
}

func popFront(queue map[string][]error, key string) error {
	if queue == nil {
		return nil
	}
	q, ok := queue[key]
	if !ok || len(q) == 0 {
		return nil
	}
	err := q[0]
	queue[key] = q[1:]
	if len(queue[key]) == 0 {
		delete(queue, key)
	}
	return err
}
