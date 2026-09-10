package httpapi

import (
	"context"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"
)

// DaemonDeps is provided only behind the local operator credential gate.
// Lifecycle is independent of the configured photo identity mode.
type DaemonDeps struct {
	Status   DaemonStatus
	Shutdown func()
}

type DaemonStatus struct {
	Running   bool       `json:"running"`
	Recovery  bool       `json:"recovery,omitempty,omitzero"`
	PID       int        `json:"pid,omitempty,omitzero"`
	Version   string     `json:"version,omitempty"`
	Address   string     `json:"address,omitempty"`
	WebURL    string     `json:"web_url,omitempty"`
	StartedAt *time.Time `json:"started_at,omitempty"`
}

func registerDaemon(api huma.API, deps *DaemonDeps) {
	huma.Register(api, huma.Operation{
		OperationID: "daemon-status", Method: http.MethodGet, Path: "/api/v1/operator/daemon",
		Summary: "Inspect the running daemon", Tags: []string{"operator"},
		Security: []map[string][]string{{"localOperator": {}}},
	}, func(context.Context, *struct{}) (*struct{ Body DaemonStatus }, error) {
		if deps == nil {
			return nil, huma.Error403Forbidden("local operator authentication required")
		}
		return &struct{ Body DaemonStatus }{deps.Status}, nil
	})
	huma.Register(api, huma.Operation{
		OperationID: "stop-daemon", Method: http.MethodPost, Path: "/api/v1/operator/daemon/stop",
		Summary: "Request graceful daemon shutdown", Tags: []string{"operator"},
		Security: []map[string][]string{{"localOperator": {}}}, DefaultStatus: http.StatusNoContent,
		Middlewares: huma.Middlewares{func(ctx huma.Context, next func(huma.Context)) {
			next(ctx)
			if deps != nil && ctx.Status() == http.StatusNoContent {
				// Send the acknowledgement before cancellation can close the
				// listener. Shutdown still joins this handler before closing storage.
				_, writer := humago.Unwrap(ctx)
				_ = http.NewResponseController(writer).Flush()
				deps.Shutdown()
			}
		}},
	}, func(context.Context, *struct{}) (*struct{}, error) {
		if deps == nil {
			return nil, huma.Error403Forbidden("local operator authentication required")
		}
		return nil, nil
	})
}
