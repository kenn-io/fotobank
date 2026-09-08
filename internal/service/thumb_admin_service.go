package service

import (
	"context"
	"fmt"

	"go.kenn.io/fotobank/internal/errs"
	"go.kenn.io/fotobank/internal/owners"
	"go.kenn.io/fotobank/internal/thumb"
)

// ThumbAdminService is available only to authenticated host operators.
// It queues existing visible, ready assets; workers produce the thumbnails.
type ThumbAdminService struct {
	queue  *thumb.Queue
	owners *OwnerService
}

func NewThumbAdminService(queue *thumb.Queue, owners *OwnerService) *ThumbAdminService {
	return &ThumbAdminService{queue: queue, owners: owners}
}

type ThumbOwnerResult struct {
	Hub      string `json:"hub"`
	UserID   string `json:"user_id"`
	Enqueued int    `json:"enqueued"`
}

func (s *ThumbAdminService) Regenerate(ctx context.Context, caller owners.Principal, allOwners bool, filter thumb.EnqueueFilter) ([]ThumbOwnerResult, error) {
	results := []ThumbOwnerResult{}
	if !allOwners && (caller.Hub == "" || caller.UserID == "") {
		return results, fmt.Errorf("%w: an owner is required", errs.ErrInvalidArgument)
	}
	principals := []owners.Principal{caller}
	if allOwners {
		registered, err := s.owners.List(ctx)
		if err != nil {
			return results, err
		}
		principals = nil
		for _, owner := range registered {
			principals = append(principals, owner.Principal)
		}
	}
	for _, principal := range principals {
		filter.Owner = principal
		n, err := s.queue.Enqueue(ctx, filter)
		if err != nil {
			return results, err
		}
		results = append(results, ThumbOwnerResult{Hub: principal.Hub, UserID: principal.UserID, Enqueued: n})
	}
	return results, nil
}
