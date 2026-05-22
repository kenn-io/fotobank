// Package httpapi — /api/v1/ai/* routes are split into this file so the
// AI surface can be reasoned about independently of the rest of the API.
// Routes are only registered when an AIService is wired into Deps; the
// OpenAPI dumper passes Deps{} so the AI surface is absent from the
// dumped spec until the runtime wires it in (Plan O).
package httpapi

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"go.kenn.io/fotobank/internal/ai"
	"go.kenn.io/fotobank/internal/ai/failures"
	"go.kenn.io/fotobank/internal/auth/hidden"
	"go.kenn.io/fotobank/internal/errs"
	aiservice "go.kenn.io/fotobank/internal/service/ai"
)

// registerAIRoutes mounts /api/v1/ai/* on api. svc==nil leaves the
// routes unregistered (no handlers, no schemas) so the OpenAPI dumper
// can pass Deps{} unchanged. probe==nil falls back to a never-reachable
// stub so the health endpoint still returns a structured response.
func registerAIRoutes(api huma.API, svc *aiservice.Service, probe aiservice.Probe, enabled bool) {
	if svc == nil {
		return
	}
	if probe == nil {
		probe = nilProbe{}
	}
	registerAIHealth(api, svc, probe, enabled)
	registerAIFailures(api, svc)
	registerAIBackfill(api, svc)
	registerAIRetryFailed(api, svc)
	registerAIRetryPhoto(api, svc)
	registerAIAcknowledge(api, svc)
	registerAIMediaView(api, svc)
}

func registerAIHealth(api huma.API, svc *aiservice.Service, probe aiservice.Probe, enabled bool) {
	huma.Register(api, huma.Operation{
		OperationID: "ai-health",
		Method:      http.MethodGet,
		Path:        "/api/v1/ai/health",
		Summary:     "AI status snapshot",
	}, func(ctx context.Context, _ *struct{}) (*aiHealthOutput, error) {
		id, ok := IdentityFromContext(ctx)
		if !ok {
			return nil, huma.Error401Unauthorized(errs.ErrIdentityMissing.Error())
		}
		h := svc.Health(ctx, id.Principal.OwnersPrincipal(), aiservice.HealthInput{
			Enabled: enabled,
			Probe:   probe,
		})
		return &aiHealthOutput{Body: h}, nil
	})
}

func registerAIFailures(api huma.API, svc *aiservice.Service) {
	huma.Register(api, huma.Operation{
		OperationID: "ai-failures",
		Method:      http.MethodGet,
		Path:        "/api/v1/ai/failures",
		Summary:     "Recent AI failures for the active fingerprint",
	}, func(ctx context.Context, in *aiFailuresInput) (*aiFailuresOutput, error) {
		id, ok := IdentityFromContext(ctx)
		if !ok {
			return nil, huma.Error401Unauthorized(errs.ErrIdentityMissing.Error())
		}
		limit := in.Limit
		if limit <= 0 {
			limit = aiFailuresDefaultLimit
		}
		rows, err := svc.ListFailures(ctx, id.Principal.OwnersPrincipal(), ai.Task(in.Task), limit)
		if err != nil {
			return nil, Translate(err)
		}
		return &aiFailuresOutput{Body: aiFailuresBody{Rows: toAIFailureDTOs(rows)}}, nil
	})
}

func registerAIBackfill(api huma.API, svc *aiservice.Service) {
	huma.Register(api, huma.Operation{
		OperationID: "ai-backfill",
		Method:      http.MethodPost,
		Path:        "/api/v1/ai/backfill",
		Summary:     "Enqueue missing-fingerprint AI jobs for the caller's library",
	}, func(ctx context.Context, in *aiBackfillInput) (*aiEnqueuedOutput, error) {
		id, ok := IdentityFromContext(ctx)
		if !ok {
			return nil, huma.Error401Unauthorized(errs.ErrIdentityMissing.Error())
		}
		n, err := svc.Backfill(ctx, id.Principal.OwnersPrincipal(), ai.Task(in.Body.Task), in.Body.Force)
		if err != nil {
			return nil, Translate(err)
		}
		return &aiEnqueuedOutput{Body: aiEnqueuedBody{Enqueued: n}}, nil
	})
}

func registerAIRetryFailed(api huma.API, svc *aiservice.Service) {
	huma.Register(api, huma.Operation{
		OperationID: "ai-retry-failed",
		Method:      http.MethodPost,
		Path:        "/api/v1/ai/retry-failed",
		Summary:     "Re-enqueue all current-fingerprint failures for a task",
	}, func(ctx context.Context, in *aiRetryFailedInput) (*aiEnqueuedOutput, error) {
		id, ok := IdentityFromContext(ctx)
		if !ok {
			return nil, huma.Error401Unauthorized(errs.ErrIdentityMissing.Error())
		}
		n, err := svc.RetryFailed(ctx, id.Principal.OwnersPrincipal(), ai.Task(in.Body.Task))
		if err != nil {
			return nil, Translate(err)
		}
		return &aiEnqueuedOutput{Body: aiEnqueuedBody{Enqueued: n}}, nil
	})
}

func registerAIRetryPhoto(api huma.API, svc *aiservice.Service) {
	huma.Register(api, huma.Operation{
		OperationID: "ai-retry-photo",
		Method:      http.MethodPost,
		Path:        "/api/v1/ai/retry-photo",
		Summary:     "Re-enqueue a single (media, task) job",
	}, func(ctx context.Context, in *aiRetryPhotoInput) (*aiAckOutput, error) {
		id, ok := IdentityFromContext(ctx)
		if !ok {
			return nil, huma.Error401Unauthorized(errs.ErrIdentityMissing.Error())
		}
		err := svc.RetryPhoto(ctx, id.Principal.OwnersPrincipal(), in.Body.MediaID, ai.Task(in.Body.Task))
		if err != nil {
			return nil, Translate(err)
		}
		return &aiAckOutput{Body: aiAckBody{OK: true}}, nil
	})
}

func registerAIAcknowledge(api huma.API, svc *aiservice.Service) {
	huma.Register(api, huma.Operation{
		OperationID: "ai-acknowledge",
		Method:      http.MethodPost,
		Path:        "/api/v1/ai/acknowledge",
		Summary:     "Record per-principal hidden-processing acknowledgement",
	}, func(ctx context.Context, in *aiAcknowledgeInput) (*aiAckOutput, error) {
		id, ok := IdentityFromContext(ctx)
		if !ok {
			return nil, huma.Error401Unauthorized(errs.ErrIdentityMissing.Error())
		}
		if in.Body.Kind != "hidden_processing" {
			return nil, huma.Error400BadRequest("unknown ack kind")
		}
		if err := svc.Acknowledge(ctx, id.Principal.OwnersPrincipal()); err != nil {
			return nil, Translate(err)
		}
		return &aiAckOutput{Body: aiAckBody{OK: true}}, nil
	})
}

func registerAIMediaView(api huma.API, svc *aiservice.Service) {
	huma.Register(api, huma.Operation{
		OperationID: "ai-media-view",
		Method:      http.MethodGet,
		Path:        "/api/v1/media/{media_id}/ai",
		Summary:     "Get AI artifacts (tags, caption, skip, failures) for a media",
	}, func(ctx context.Context, in *aiMediaViewInput) (*aiMediaViewOutput, error) {
		id, ok := IdentityFromContext(ctx)
		if !ok {
			return nil, huma.Error401Unauthorized(errs.ErrIdentityMissing.Error())
		}
		caller := id.Principal.OwnersPrincipal()
		// Mirror the direct media-detail route: a hidden-unlock claim
		// from the same principal lets MediaView return artifacts for a
		// hidden row; without it the row is treated as missing.
		includeHidden := false
		if claim, hasClaim := hidden.UnlockClaimFromContext(ctx); hasClaim && claim.Principal == caller {
			includeHidden = true
		}
		v, err := svc.MediaView(ctx, caller, in.MediaID, includeHidden)
		if err != nil {
			return nil, Translate(err)
		}
		return &aiMediaViewOutput{Body: v}, nil
	})
}

type aiMediaViewInput struct {
	MediaID string `path:"media_id"`
}

type aiMediaViewOutput struct {
	Body aiservice.MediaView
}

// aiFailuresDefaultLimit matches the panel's "Recent failures" pagination.
const aiFailuresDefaultLimit = 5

type aiHealthOutput struct {
	Body aiservice.Health
}

type aiFailuresInput struct {
	Task  string `query:"task" enum:"tag,caption" doc:"AI task to query"`
	Limit int    `query:"limit" minimum:"1" maximum:"100" doc:"max rows to return (default 5)"`
}

type aiFailuresOutput struct {
	Body aiFailuresBody
}

type aiFailuresBody struct {
	Rows []aiFailureDTO `json:"rows"`
}

// aiFailureDTO is the wire-shape for a single failure row. Mirrors
// failures.Row but with explicit JSON tags so the spec is stable
// against domain renames.
type aiFailureDTO struct {
	MediaID       string    `json:"media_id"`
	Task          string    `json:"task"`
	ModelID       string    `json:"model_id"`
	PromptVersion string    `json:"prompt_version"`
	InputProfile  string    `json:"input_profile"`
	LastError     string    `json:"last_error"`
	LastErrorKind string    `json:"last_error_kind"`
	AttemptCount  int       `json:"attempt_count"`
	FailedAt      time.Time `json:"failed_at"`
}

func toAIFailureDTOs(rows []failures.Row) []aiFailureDTO {
	out := make([]aiFailureDTO, 0, len(rows))
	for _, r := range rows {
		out = append(out, aiFailureDTO{
			MediaID:       r.MediaID,
			Task:          string(r.Task),
			ModelID:       r.ModelID,
			PromptVersion: r.PromptVersion,
			InputProfile:  r.InputProfile,
			LastError:     r.LastError,
			LastErrorKind: string(r.LastErrorKind),
			AttemptCount:  r.AttemptCount,
			FailedAt:      r.FailedAt,
		})
	}
	return out
}

type aiBackfillInput struct {
	Body struct {
		Task  string `json:"task" enum:"tag,caption" doc:"AI task to enqueue"`
		Force bool   `json:"force,omitempty" doc:"include media that already have an active result"`
		Scope string `json:"scope,omitempty" enum:"all" doc:"reserved for future scoping"`
	}
}

type aiRetryFailedInput struct {
	Body struct {
		Task string `json:"task" enum:"tag,caption"`
	}
}

type aiRetryPhotoInput struct {
	Body struct {
		MediaID string `json:"media_id"`
		Task    string `json:"task" enum:"tag,caption"`
	}
}

type aiAcknowledgeInput struct {
	Body struct {
		Kind string `json:"kind" enum:"hidden_processing"`
	}
}

type aiEnqueuedOutput struct {
	Body aiEnqueuedBody
}

type aiEnqueuedBody struct {
	Enqueued int `json:"enqueued"`
}

type aiAckOutput struct {
	Body aiAckBody
}

type aiAckBody struct {
	OK bool `json:"ok"`
}

// nilProbe is the fallback Probe used when no probe is wired. Reports
// the gateway as unreachable so the panel can render that fact rather
// than panicking on a nil dereference.
type nilProbe struct{}

// Probe always reports the gateway as unreachable.
func (nilProbe) Probe(_ context.Context) error {
	return errors.New("ai vision probe not configured")
}
