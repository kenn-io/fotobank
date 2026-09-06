package content

import (
	"context"
	"fmt"

	"go.kenn.io/docbank"
)

type BackupForgetReport struct {
	Selected  []string
	Forgotten []string
}

type BackupPruneReport struct {
	PacksToRemove      []string
	PacksToRepack      []string
	BytesToRemove      int64
	LiveBytesToRewrite uint64
	RemovedPacks       []string
	BytesRemoved       int64
	BytesWritten       int64
}

// Forget never permits removing the last recovery point.
func (r *BackupRepository) Forget(ctx context.Context, ids []string, dryRun bool) (BackupForgetReport, error) {
	if r == nil || r.repository == nil {
		return BackupForgetReport{}, fmt.Errorf("backup repository is required")
	}
	result, err := r.repository.Forget(ctx, docbank.BackupForgetOptions{
		SnapshotIDs: ids, DryRun: dryRun,
	})
	return BackupForgetReport{Selected: result.Selected, Forgotten: result.Forgotten}, translateError(err)
}

func (r *BackupRepository) Prune(ctx context.Context, dryRun bool) (BackupPruneReport, error) {
	if r == nil || r.repository == nil {
		return BackupPruneReport{}, fmt.Errorf("backup repository is required")
	}
	result, err := r.repository.Prune(ctx, docbank.BackupPruneOptions{DryRun: dryRun})
	return BackupPruneReport{
		PacksToRemove: result.PacksToRemove, PacksToRepack: result.PacksToRepack,
		BytesToRemove: result.BytesToRemove, LiveBytesToRewrite: result.LiveBytesToRewrite,
		RemovedPacks: result.RemovedPacks, BytesRemoved: result.BytesRemoved, BytesWritten: result.BytesWritten,
	}, translateError(err)
}
