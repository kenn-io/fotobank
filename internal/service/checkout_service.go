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
	counts, err := s.repo.EntryCounts(ctx, checkoutID)
	if err != nil {
		return checkout.Status{}, err
	}
	problems, err := s.repo.ListProblemEntries(ctx, checkoutID)
	if err != nil {
		return checkout.Status{}, err
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
