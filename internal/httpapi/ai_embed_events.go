package httpapi

import (
	"go.kenn.io/fotobank/internal/owners"
)

// AIEmbedEvents adapts the per-principal EventBus into the embedding
// package's EventEmitter contract, which is principal-free at the
// callsite (the worker, activator, and generations registry don't
// thread a principal through their hot paths). v1 is single-principal:
// every embed-related event lands on the configured stub-mode
// principal's channel. Multi-principal fan-out is a post-v1 concern,
// at which point this adapter would either become a fan-out helper or
// callers would acquire a per-principal adapter inline.
//
// The adapter is intentionally small (translate-then-forward); the
// per-event marshal-and-publish work lives on EventBus.EmitAIEmbed*
// so the unit tests can exercise the bus path independent of the
// embedding-package wiring.
type AIEmbedEvents struct {
	bus       *EventBus
	principal owners.Principal
}

// NewAIEmbedEvents binds bus + principal into an emitter the embedding
// package can consume. Caller is responsible for passing the right
// principal — in stub-mode v1 this is cfg.Identity.Stub's principal.
func NewAIEmbedEvents(bus *EventBus, p owners.Principal) *AIEmbedEvents {
	return &AIEmbedEvents{bus: bus, principal: p}
}

// EmitAIEmbedCompleted forwards a per-job completion event onto the
// principal's bus channel.
func (a *AIEmbedEvents) EmitAIEmbedCompleted(mediaID, fingerprint string) {
	a.bus.EmitAIEmbedCompleted(a.principal, AIEmbedCompletedEvent{
		MediaID:     mediaID,
		Fingerprint: fingerprint,
	})
}

// EmitAIEmbedFailed forwards a per-job failure event onto the
// principal's bus channel. errorKind is the string form of
// ai.LastErrorKind ("transient", "provider_4xx", "malformed", …).
func (a *AIEmbedEvents) EmitAIEmbedFailed(mediaID, fingerprint, errorKind string) {
	a.bus.EmitAIEmbedFailed(a.principal, AIEmbedFailedEvent{
		MediaID:     mediaID,
		Fingerprint: fingerprint,
		ErrorKind:   errorKind,
	})
}

// EmitAIEmbedGenerationCreated forwards a generation-created event
// onto the principal's bus channel. Fired only on the actual INSERT
// path inside FindOrCreateBuilding; the fast-path lookup that returns
// an existing row never reaches this hook.
func (a *AIEmbedEvents) EmitAIEmbedGenerationCreated(generationID int64, fingerprint string) {
	a.bus.EmitAIEmbedGenerationCreated(a.principal, AIEmbedGenerationEvent{
		ID:          generationID,
		Fingerprint: fingerprint,
	})
}

// EmitAIEmbedGenerationActivated forwards a generation-activated event
// onto the principal's bus channel. Fired by the activator after
// PromoteFromBuilding succeeds.
func (a *AIEmbedEvents) EmitAIEmbedGenerationActivated(generationID int64, fingerprint string) {
	a.bus.EmitAIEmbedGenerationActivated(a.principal, AIEmbedGenerationEvent{
		ID:          generationID,
		Fingerprint: fingerprint,
	})
}

// EmitAIEmbedGenerationRetired forwards a generation-retired event
// onto the principal's bus channel. Fired by Generations.Promote /
// PromoteFromBuilding only when the retire-prior-active UPDATE
// actually changed a row (RowsAffected > 0).
func (a *AIEmbedEvents) EmitAIEmbedGenerationRetired(generationID int64, fingerprint string) {
	a.bus.EmitAIEmbedGenerationRetired(a.principal, AIEmbedGenerationEvent{
		ID:          generationID,
		Fingerprint: fingerprint,
	})
}
