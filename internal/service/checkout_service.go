package service

import (
	"context"
	"fmt"

	"go.kenn.io/fotobank/internal/checkout"
	"go.kenn.io/fotobank/internal/content"
	"go.kenn.io/fotobank/internal/contentresolver"
	"go.kenn.io/fotobank/internal/errs"
	"go.kenn.io/fotobank/internal/media"
	"go.kenn.io/fotobank/internal/owners"
)

// CheckoutService is the caller-scoped boundary for inspecting, creating, and
// committing working copies.
type CheckoutService struct {
	repo         *checkout.Repo
	materializer *checkout.Materializer
	committer    *checkout.Committer
	contentStore *content.Adapter
}

func NewCheckoutService(
	repo *checkout.Repo,
	resolver *contentresolver.Resolver,
	contentStore *content.Adapter,
	creationLockPath string,
	places media.PlaceResolver,
) *CheckoutService {
	return &CheckoutService{
		repo:         repo,
		materializer: checkout.NewMaterializer(repo, resolver, creationLockPath),
		committer:    checkout.NewCommitter(repo, contentStore, places),
		contentStore: contentStore,
	}
}

func (s *CheckoutService) List(
	ctx context.Context,
	caller owners.Principal,
) ([]checkout.Summary, error) {
	return s.repo.ListByOwner(ctx, caller)
}

func (s *CheckoutService) Status(
	ctx context.Context,
	caller owners.Principal,
	checkoutID string,
) (checkout.Status, error) {
	row, err := s.repo.Get(ctx, checkoutID)
	if err != nil {
		return checkout.Status{}, err
	}
	if row.Owner != caller {
		return checkout.Status{}, fmt.Errorf("checkout status: %w", errs.ErrNotFound)
	}
	entries, err := s.repo.ListEntries(ctx, checkoutID)
	if err != nil {
		return checkout.Status{}, err
	}
	counts := checkout.EntryCounts{Total: len(entries)}
	problems := make([]checkout.Entry, 0, len(entries))
	for _, entry := range entries {
		switch entry.State {
		case checkout.EntryClean:
			counts.Clean++
		case checkout.EntryPending:
			counts.Pending++
		case checkout.EntryConflict:
			counts.Conflict++
		case checkout.EntryMissing:
			counts.Missing++
		case checkout.EntryError:
			counts.Error++
		}
		if entry.State != checkout.EntryClean {
			problems = append(problems, entry)
		}
	}
	return checkout.Status{
		Checkout: checkout.Summary{
			ID: row.ID, Root: row.Root, Layout: row.Layout, State: row.State,
			LastError: row.LastError, Entries: counts,
			CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
		},
		Selection: row.Selection,
		Problems:  problems,
	}, nil
}

func (s *CheckoutService) Commit(
	ctx context.Context,
	caller owners.Principal,
	checkoutID string,
) (checkout.CommitResult, error) {
	return s.committer.Commit(ctx, caller, checkoutID)
}

func (s *CheckoutService) Estimate(
	ctx context.Context,
	caller owners.Principal,
	selection checkout.Selection,
) (checkout.Estimate, error) {
	return s.materializer.Estimate(ctx, caller, selection)
}

func (s *CheckoutService) Create(
	ctx context.Context,
	caller owners.Principal,
	request checkout.CreateRequest,
) (checkout.CreateResult, error) {
	return s.materializer.Create(ctx, caller, request)
}

// CreateAt validates a local operator's destination using the server's storage
// configuration before handing the bound directory to the materializer.
func (s *CheckoutService) CreateAt(ctx context.Context, caller owners.Principal, root string, selection checkout.Selection, maxBytes int64) (checkout.CreateResult, error) {
	resolved, err := s.contentStore.ResolveCheckoutRoot(root)
	if err != nil {
		return checkout.CreateResult{}, err
	}
	return s.Create(ctx, caller, checkout.CreateRequest{
		Root: resolved, Selection: selection, CapacityLimit: maxBytes,
	})
}
