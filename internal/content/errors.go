package content

import (
	"errors"
	"fmt"

	"go.kenn.io/docbank"
	"go.kenn.io/fotobank/internal/errs"
)

func translateError(err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, docbank.ErrNotFound):
		return fmt.Errorf("%w: %w", errs.ErrNotFound, err)
	case errors.Is(err, docbank.ErrContentConflict):
		return fmt.Errorf("%w: %w", errs.ErrContentConflict, err)
	case errors.Is(err, docbank.ErrDigestMismatch),
		errors.Is(err, docbank.ErrSizeMismatch):
		return fmt.Errorf("%w: %w", errs.ErrContentIdentityMismatch, err)
	case errors.Is(err, docbank.ErrContentUnavailable),
		errors.Is(err, docbank.ErrClosed):
		return fmt.Errorf("%w: %w", errs.ErrContentUnavailable, err)
	default:
		return err
	}
}
