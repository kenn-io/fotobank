package httpapi

import (
	"context"
	"net/http"
	"strings"
	"time"
	"uuid"

	"github.com/danielgtaylor/huma/v2"
	"go.kenn.io/fotobank/internal/media"
	"go.kenn.io/fotobank/internal/owners"
	"go.kenn.io/fotobank/internal/service"
	"go.kenn.io/fotobank/internal/thumb"
)

type ThumbsOperatorDeps struct {
	Service      *service.ThumbAdminService
	DefaultOwner *owners.Principal
}

type RegenerateThumbsRequest struct {
	All       bool        `json:"all,omitempty"`
	IDs       []uuid.UUID `json:"ids,omitempty"`
	Type      string      `json:"type,omitempty" enum:"photo,video"`
	Status    string      `json:"status,omitempty" enum:"pending,working,ready,failed,no_preview"`
	Since     *time.Time  `json:"since,omitempty"`
	Owner     string      `json:"owner,omitempty" doc:"Target hub:user; mutually exclusive with all_owners."`
	AllOwners bool        `json:"all_owners,omitempty"`
}

type RegenerateThumbsResult struct {
	Items []service.ThumbOwnerResult `json:"items"`
	Error string                     `json:"error,omitempty" doc:"If present, earlier items succeeded but the operation did not finish. Do not blindly retry: regeneration increments thumbnail versions."`
}

func registerOperatorThumbs(api huma.API, deps *ThumbsOperatorDeps) {
	huma.Register(api, huma.Operation{
		OperationID: "regenerate-thumbnails", Method: http.MethodPost, Path: "/api/v1/operator/thumbs/regenerate",
		Summary: "Queue thumbnail regeneration", Tags: []string{"operator"},
		Security:    []map[string][]string{{"localOperator": {}}},
		Description: "Requires at least one selector. Selectors combine as filters, including when all is true. Only ready, visible assets are eligible. Defaults to the configured stub owner; host operators can select an owner or all owners. Reports queued work, not completed thumbnails.",
	}, func(ctx context.Context, in *struct{ Body RegenerateThumbsRequest }) (*struct{ Body RegenerateThumbsResult }, error) {
		if deps == nil {
			return nil, huma.Error403Forbidden("local operator authentication required")
		}
		request := in.Body
		if !request.All && len(request.IDs) == 0 && request.Type == "" && request.Status == "" && request.Since == nil {
			return nil, huma.Error400BadRequest("at least one selector is required")
		}
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
		filter := thumb.EnqueueFilter{All: request.All, MediaType: media.Type(request.Type), Status: request.Status, Since: request.Since}
		for _, id := range request.IDs {
			filter.IDs = append(filter.IDs, id.String())
		}
		items, err := deps.Service.Regenerate(ctx, owner, request.AllOwners, filter)
		result := RegenerateThumbsResult{Items: items}
		if err != nil {
			result.Error = err.Error()
		}
		return &struct{ Body RegenerateThumbsResult }{result}, nil
	})
}
