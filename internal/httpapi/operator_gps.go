package httpapi

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"go.kenn.io/fotobank/internal/media"
	"go.kenn.io/fotobank/internal/owners"
	"go.kenn.io/fotobank/internal/service"
)

// GPSOperatorDeps is supplied only behind the host-operator credential check,
// including header deployments, which have no default photo principal.
type GPSOperatorDeps struct {
	Service      *service.GPSService
	DefaultOwner *owners.Principal
}
type GPSBackfillRequest struct {
	Mode      string     `json:"mode,omitempty" default:"full" enum:"full,fill-missing,relabel"`
	Since     *time.Time `json:"since,omitempty" doc:"Only photos imported at or after this timestamp."`
	Owner     string     `json:"owner,omitempty" doc:"Host-admin target in hub:user form; mutually exclusive with all_owners."`
	AllOwners bool       `json:"all_owners,omitempty"`
}
type GPSBackfillResult struct {
	service.GPSResult
	Error string `json:"error,omitempty"`
}

func registerOperatorGPS(api huma.API, deps *GPSOperatorDeps) {
	huma.Register(api, huma.Operation{
		OperationID: "backfill-gps", Method: http.MethodPost, Path: "/api/v1/operator/gps/backfill",
		Summary: "Refresh photo coordinates or place names", Tags: []string{"operator"},
		Security: []map[string][]string{{"localOperator": {}}}, MaxBodyBytes: 4096,
		Description: "Host operators may target one owner or all owners. Without an explicit scope, uses the configured stub owner. Returns partial counts and per-photo failures; an error means the run did not fully succeed.",
	}, func(ctx context.Context, input *struct{ Body GPSBackfillRequest }) (*struct{ Body GPSBackfillResult }, error) {
		if deps == nil {
			return nil, huma.Error403Forbidden("local operator authentication required")
		}
		request := input.Body
		if request.Owner != "" && request.AllOwners {
			return nil, huma.Error400BadRequest("owner and all_owners are mutually exclusive")
		}
		var owner owners.Principal
		if request.Owner != "" {
			hub, user, ok := strings.Cut(request.Owner, ":")
			if !ok || hub == "" || user == "" {
				return nil, huma.Error400BadRequest("owner must be hub:user")
			}
			owner = owners.Principal{Hub: hub, UserID: user}
		} else if !request.AllOwners {
			if deps.DefaultOwner == nil {
				return nil, huma.Error400BadRequest("header mode requires owner or all_owners")
			}
			owner = *deps.DefaultOwner
		}
		mode := media.GPSBackfillModeFull
		switch request.Mode {
		case "fill-missing":
			mode = media.GPSBackfillModeFillMissing
		case "relabel":
			mode = media.GPSBackfillModeRelabel
		}
		result, err := deps.Service.Backfill(ctx, owner, service.GPSOptions{Mode: mode, Since: request.Since, AllOwners: request.AllOwners})
		out := GPSBackfillResult{GPSResult: result}
		if err != nil {
			out.Error = err.Error()
		}
		return &struct{ Body GPSBackfillResult }{out}, nil
	})
}
