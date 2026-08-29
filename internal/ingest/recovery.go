package ingest

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"go.kenn.io/fotobank/internal/content"
	"go.kenn.io/fotobank/internal/errs"
	"go.kenn.io/fotobank/internal/media"
	"go.kenn.io/fotobank/internal/owners"
)

// RecoveryResult describes the durable state observed and repaired for one
// owner. Orphans are authoritative Docbank files with no operation-ledger row;
// recovery reports them but never deletes or moves them.
type RecoveryResult struct {
	Adopted   int
	Finalized int
	Pending   int
	Conflicts int
	Orphans   []content.Node
}

// RecoverOwner adopts Docbank creates that crossed the storage boundary before
// a process interruption, finalizes complete assets, and reports unmatched
// authority. A pending operation whose path does not exist remains pending so
// re-running its source import can retry the idempotent create.
func (imp *Importer) RecoverOwner(ctx context.Context, owner owners.Principal) (RecoveryResult, error) {
	operations, err := imp.assets.ListContentOperations(ctx, owner)
	if err != nil {
		return RecoveryResult{}, err
	}

	knownPaths := make(map[string]struct{}, len(operations))
	assetOrder := make([]string, 0)
	byAsset := make(map[string][]media.ContentReservation)
	for _, operation := range operations {
		knownPaths[operation.VirtualPath] = struct{}{}
		assetID := operation.File.AssetID
		if _, seen := byAsset[assetID]; !seen {
			assetOrder = append(assetOrder, assetID)
		}
		byAsset[assetID] = append(byAsset[assetID], operation)
	}

	result := RecoveryResult{}
	for _, assetID := range assetOrder {
		group := byAsset[assetID]
		asset, err := imp.assets.GetAsset(ctx, assetID)
		if err != nil {
			return result, err
		}
		if asset.State == media.AssetConflict {
			continue
		}

		unresolved := 0
		conflicted := false
		for i := range group {
			operation := &group[i]
			if operation.Status != "pending" {
				continue
			}
			node, statErr := imp.content.Stat(ctx, operation.VirtualPath)
			if errors.Is(statErr, errs.ErrNotFound) {
				unresolved++
				continue
			}
			if statErr != nil {
				return result, fmt.Errorf("recover content operation %s: %w", operation.OperationID, statErr)
			}
			if node.Kind != "file" || node.SHA256 != operation.SHA256 || node.Size != operation.Size {
				cause := fmt.Errorf("%w: Docbank path %s does not match its reserved identity",
					errs.ErrContentConflict, operation.VirtualPath)
				marked, markErr := imp.assets.MarkContentConflict(ctx, assetID, cause)
				if markErr != nil {
					return result, errors.Join(cause, markErr)
				}
				if marked {
					result.Conflicts++
					conflicted = true
				}
				break
			}
			if err := imp.assets.ApplyContentReceipt(ctx, media.ContentReceipt{
				OperationID: operation.OperationID,
				NodeID:      node.ID,
				VersionID:   node.CurrentVersionID,
				SHA256:      node.SHA256,
				Size:        node.Size,
			}); err != nil {
				return result, fmt.Errorf("adopt content operation %s: %w", operation.OperationID, err)
			}
			operation.Status = "applied"
			result.Adopted++
		}
		if conflicted {
			continue
		}
		result.Pending += unresolved
		if unresolved > 0 {
			continue
		}
		if asset.State == media.AssetPending {
			if err := imp.assets.FinalizeReady(ctx, assetID); err != nil {
				return result, err
			}
			result.Finalized++
		}
		imp.refreshFTS(ctx, assetID)
		if asset.Type == media.TypePhoto {
			if err := imp.ai.EnqueueForPhoto(ctx, assetID); err != nil {
				return result, fmt.Errorf("enqueue recovered photo %s: %w", assetID, err)
			}
		} else if err := imp.ai.RecordVideoSkip(ctx, assetID); err != nil {
			return result, fmt.Errorf("record recovered video %s: %w", assetID, err)
		}
	}

	root, err := content.OwnerMediaRoot(imp.ownerStorageKey)
	if err != nil {
		return result, err
	}
	err = imp.content.Walk(ctx, root, func(node content.Node) error {
		if node.Kind != "file" {
			return nil
		}
		if _, known := knownPaths[node.VirtualPath]; !known {
			result.Orphans = append(result.Orphans, node)
		}
		return nil
	})
	if errors.Is(err, errs.ErrNotFound) {
		return result, nil
	}
	if err != nil {
		return result, fmt.Errorf("scan owner Docbank subtree: %w", err)
	}
	sort.Slice(result.Orphans, func(i, j int) bool {
		return result.Orphans[i].VirtualPath < result.Orphans[j].VirtualPath
	})
	return result, nil
}
