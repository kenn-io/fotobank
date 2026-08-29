package service

import (
	"context"

	"go.kenn.io/fotobank/internal/checkout"
	"go.kenn.io/fotobank/internal/contentresolver"
	"go.kenn.io/fotobank/internal/owners"
)

// CheckoutService is the caller-scoped boundary for creating working copies.
type CheckoutService struct {
	materializer *checkout.Materializer
}

func NewCheckoutService(
	repo *checkout.Repo,
	resolver *contentresolver.Resolver,
	creationLockPath string,
	databaseLockPath string,
) *CheckoutService {
	return &CheckoutService{materializer: checkout.NewMaterializer(
		repo, resolver, creationLockPath, databaseLockPath)}
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
