package content

import (
	"errors"
	"fmt"
	"io"

	"go.kenn.io/docbank"
	"go.kenn.io/fotobank/internal/errs"
)

func translateReaderError(err error) error {
	if err == nil || errors.Is(err, io.EOF) {
		return err
	}
	return fmt.Errorf("%w: %w", errs.ErrContentUnavailable, translateError(err))
}

func translateError(err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, docbank.ErrNotFound):
		return fmt.Errorf("%w: %w", errs.ErrNotFound, err)
	case errors.Is(err, docbank.ErrContentConflict):
		return fmt.Errorf("%w: %w", errs.ErrContentConflict, err)
	case errors.Is(err, docbank.ErrStaleRevision):
		return fmt.Errorf("%w: %w", errs.ErrContentConflict, err)
	case errors.Is(err, docbank.ErrDigestMismatch),
		errors.Is(err, docbank.ErrSizeMismatch):
		return fmt.Errorf("%w: %w", errs.ErrContentIdentityMismatch, err)
	case errors.Is(err, docbank.ErrContentUnavailable),
		errors.Is(err, docbank.ErrVisualPreviewUnavailable),
		errors.Is(err, docbank.ErrClosed):
		return fmt.Errorf("%w: %w", errs.ErrContentUnavailable, err)
	case errors.Is(err, docbank.ErrInvalidContentRange):
		return fmt.Errorf("%w: %w", errs.ErrInvalidArgument, err)
	case errors.Is(err, docbank.ErrBackupLastSnapshot), errors.Is(err, docbank.ErrBackupSnapshotRequired):
		return fmt.Errorf("%w: %w", errs.ErrInvalidArgument, err)
	case errors.Is(err, docbank.ErrBackupRepositoryLocked):
		return fmt.Errorf("%w: %w", errs.ErrBackupRepositoryLocked, err)
	case errors.Is(err, docbank.ErrBackupRestoreTargetActive):
		return fmt.Errorf("%w: %w", errs.ErrBackupRestoreTargetActive, err)
	case errors.Is(err, docbank.ErrBackupRestoreTargetChanged):
		return fmt.Errorf("%w: %w", errs.ErrBackupRestoreTargetChanged, err)
	case errors.Is(err, docbank.ErrBackupRestoreTargetNotEmpty):
		return fmt.Errorf("%w: %w", errs.ErrBackupRestoreTargetNotEmpty, err)
	case errors.Is(err, docbank.ErrBackupRestoreTargetOverlap):
		return fmt.Errorf("%w: %w", errs.ErrBadConfiguration, err)
	default:
		return err
	}
}
