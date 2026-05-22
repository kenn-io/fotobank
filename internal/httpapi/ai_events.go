package httpapi

import (
	"encoding/json"

	"go.kenn.io/fotobank/internal/owners"
)

// EmitAICompleted publishes a tag/caption completion event to p. The
// event name is derived from ev.Task: "caption" maps to
// EventNameAICaptionCompleted; everything else (currently only "tag")
// maps to EventNameAITagCompleted. Wired into the worker's
// success/failure paths via the AIEvents adapter (Section O).
//
// A marshal error here is silently dropped — the only realistic cause
// is a payload field that has been renamed to an unmarshalable type,
// which would surface in tests long before production. The
// completion is durable in ai_results / ai_failures regardless of the
// SSE delivery; the SPA falls back to polling on reconnect.
func (b *EventBus) EmitAICompleted(p owners.Principal, ev AICompletedEvent) {
	name := EventNameAITagCompleted
	if ev.Task == "caption" {
		name = EventNameAICaptionCompleted
	}
	data, err := json.Marshal(ev)
	if err != nil {
		return
	}
	b.PublishAutoID(p, name, data)
}

// EmitAIHealthChanged publishes a health-delta event to p. Q1
// throttles emission on the producer side so the SSE stream does not
// thrash on every queue-depth tick.
func (b *EventBus) EmitAIHealthChanged(p owners.Principal, ev AIHealthChangedEvent) {
	data, err := json.Marshal(ev)
	if err != nil {
		return
	}
	b.PublishAutoID(p, EventNameAIHealthChanged, data)
}

// EmitAIEmbedCompleted publishes an ai.embed.completed event to p.
// Wired into the embed worker via the AIEmbedEvents adapter — fires
// once per claim after commitBatch persists the mapping. A marshal
// error here is silently dropped: the mapping row is durable in
// media_embedding_ids regardless of SSE delivery, and the SPA falls
// back to polling on reconnect (same posture as EmitAICompleted).
func (b *EventBus) EmitAIEmbedCompleted(p owners.Principal, ev AIEmbedCompletedEvent) {
	data, err := json.Marshal(ev)
	if err != nil {
		return
	}
	b.PublishAutoID(p, EventNameAIEmbedCompleted, data)
}

// EmitAIEmbedFailed publishes an ai.embed.failed event to p. Wired
// into the embed worker's recordTerminalFailure path; only fires
// after MarkFailed succeeds so a reclaimed lease (jobs.ErrClaimLost)
// doesn't surface a phantom failure under a claim some other worker
// now owns.
func (b *EventBus) EmitAIEmbedFailed(p owners.Principal, ev AIEmbedFailedEvent) {
	data, err := json.Marshal(ev)
	if err != nil {
		return
	}
	b.PublishAutoID(p, EventNameAIEmbedFailed, data)
}

// EmitAIEmbedGenerationCreated publishes an ai.embed.generation_created
// event to p. Fires from Generations.FindOrCreateBuilding only on the
// actual INSERT path — the fast-path lookup that returns an existing
// row does not emit (the row was created by a prior call).
func (b *EventBus) EmitAIEmbedGenerationCreated(p owners.Principal, ev AIEmbedGenerationEvent) {
	data, err := json.Marshal(ev)
	if err != nil {
		return
	}
	b.PublishAutoID(p, EventNameAIEmbedGenerationCreated, data)
}

// EmitAIEmbedGenerationActivated publishes an ai.embed.generation_activated
// event to p. Fires from the activator's Tick after a successful
// PromoteFromBuilding (Plan H1). The event payload carries the
// promoted row's id and fingerprint so listeners can route by either.
func (b *EventBus) EmitAIEmbedGenerationActivated(p owners.Principal, ev AIEmbedGenerationEvent) {
	data, err := json.Marshal(ev)
	if err != nil {
		return
	}
	b.PublishAutoID(p, EventNameAIEmbedGenerationActivated, data)
}

// EmitAIEmbedGenerationRetired publishes an ai.embed.generation_retired
// event to p. Fires from Generations.Promote / PromoteFromBuilding
// only when the retire-prior-active UPDATE actually changed a row —
// "no prior active" (the normal first-promotion case) doesn't emit
// because there's no retired row to announce.
func (b *EventBus) EmitAIEmbedGenerationRetired(p owners.Principal, ev AIEmbedGenerationEvent) {
	data, err := json.Marshal(ev)
	if err != nil {
		return
	}
	b.PublishAutoID(p, EventNameAIEmbedGenerationRetired, data)
}
