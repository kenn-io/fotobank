package httpapi

import (
	"encoding/json"

	"github.com/wesm/fotobank/internal/owners"
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
