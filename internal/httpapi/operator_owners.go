package httpapi

import (
	"context"
	"errors"
	"net/http"
	"time"
	"uuid"

	"github.com/danielgtaylor/huma/v2"

	"go.kenn.io/fotobank/internal/errs"
	"go.kenn.io/fotobank/internal/owners"
	"go.kenn.io/fotobank/internal/service"
)

type RegisterOwnerRequest struct {
	Hub        string     `json:"hub" minLength:"1"`
	UserID     string     `json:"user_id" minLength:"1"`
	StorageKey *uuid.UUID `json:"storage_key,omitempty"`
	Handle     string     `json:"handle,omitempty"`
}

type OwnerResult struct {
	Hub        string    `json:"hub"`
	UserID     string    `json:"user_id"`
	StorageKey uuid.UUID `json:"storage_key"`
	Handle     string    `json:"handle"`
	CreatedAt  time.Time `json:"created_at"`
}

type OwnerListResult struct {
	Items []OwnerResult `json:"items"`
}

type RemoveOwnerRequest struct {
	Hub    string `query:"hub" required:"true" minLength:"1"`
	UserID string `query:"user_id" required:"true" minLength:"1"`
}

func ownerResult(owner owners.Owner) (OwnerResult, error) {
	key, err := uuid.Parse(owner.StorageKey)
	if err != nil {
		return OwnerResult{}, err
	}
	return OwnerResult{Hub: owner.Principal.Hub, UserID: owner.Principal.UserID, StorageKey: key, Handle: owner.DisplayHandle, CreatedAt: owner.CreatedAt}, nil
}

// The service is supplied only to the authenticated host-operator listener.
// Photo-user identity, including photo administrators, cannot grant this access.
func registerOperatorOwners(api huma.API, svc *service.OwnerService) {
	huma.Register(api, huma.Operation{
		OperationID: "register-owner", Method: http.MethodPost, Path: "/api/v1/operator/owners",
		Summary: "Register an owner or update their display handle", Tags: []string{"operator"},
		Security: []map[string][]string{{"localOperator": {}}}, MaxBodyBytes: 4096,
	}, func(ctx context.Context, in *struct{ Body RegisterOwnerRequest }) (*struct{ Body OwnerResult }, error) {
		if svc == nil {
			return nil, huma.Error403Forbidden("local operator authentication required")
		}
		request := in.Body
		key := ""
		if request.StorageKey != nil {
			key = request.StorageKey.String()
		}
		principal := owners.Principal{Hub: request.Hub, UserID: request.UserID}
		owner, err := svc.Ensure(ctx, principal, key)
		if err != nil {
			return nil, Translate(err)
		}
		if request.Handle != "" {
			if err := svc.UpdateDisplay(ctx, principal, request.Handle); err != nil {
				return nil, Translate(err)
			}
			owner.DisplayHandle = request.Handle
		}
		result, err := ownerResult(owner)
		if err != nil {
			return nil, Translate(err)
		}
		return &struct{ Body OwnerResult }{result}, nil
	})
	huma.Register(api, huma.Operation{
		OperationID: "list-owners", Method: http.MethodGet, Path: "/api/v1/operator/owners",
		Summary: "List registered owners", Tags: []string{"operator"},
		Security: []map[string][]string{{"localOperator": {}}},
	}, func(ctx context.Context, _ *struct{}) (*struct{ Body OwnerListResult }, error) {
		if svc == nil {
			return nil, huma.Error403Forbidden("local operator authentication required")
		}
		rows, err := svc.List(ctx)
		if err != nil {
			return nil, Translate(err)
		}
		result := OwnerListResult{Items: make([]OwnerResult, 0, len(rows))}
		for _, row := range rows {
			item, err := ownerResult(row)
			if err != nil {
				return nil, Translate(err)
			}
			result.Items = append(result.Items, item)
		}
		return &struct{ Body OwnerListResult }{result}, nil
	})
	huma.Register(api, huma.Operation{
		OperationID: "remove-owner", Method: http.MethodDelete, Path: "/api/v1/operator/owners",
		Summary: "Unregister an owner without deleting their content", Tags: []string{"operator"},
		Security: []map[string][]string{{"localOperator": {}}}, DefaultStatus: http.StatusNoContent,
	}, func(ctx context.Context, in *RemoveOwnerRequest) (*struct{}, error) {
		if svc == nil {
			return nil, huma.Error403Forbidden("local operator authentication required")
		}
		err := svc.Remove(ctx, owners.Principal{Hub: in.Hub, UserID: in.UserID}, false)
		if errors.Is(err, errs.ErrInvalidArgument) {
			// Host operators need the reason removal was refused.
			return nil, huma.Error400BadRequest(err.Error())
		}
		if err != nil {
			return nil, Translate(err)
		}
		return nil, nil
	})
}
