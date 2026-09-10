package config

import (
	"fmt"
	"path/filepath"

	"go.kenn.io/fotobank/internal/errs"
)

// ArchiveRestorePaths resolves an isolated recovery target without requiring
// the source storage to exist. Configured aliases are checked lexically here;
// resolved source roots are passed to Docbank for filesystem overlap checks.
// This is not a resolver for opening a database or deriving its lifetime lock.
func ArchiveRestorePaths(target, databasePath string, protected []string) (string, []string, error) {
	requestedTarget, err := absoluteConfiguredPath(filepath.FromSlash(target))
	if err != nil {
		return "", nil, err
	}
	resolvedTarget, err := canonicalConfigPath(requestedTarget)
	if err != nil {
		return "", nil, fmt.Errorf("resolve archive restore target: %w", err)
	}
	configuredDB, err := absoluteConfiguredPath(filepath.FromSlash(databasePath))
	if err != nil {
		return "", nil, err
	}
	resolvedDB, err := canonicalUnavailableConfigPath(configuredDB)
	if err != nil {
		return "", nil, fmt.Errorf("resolve archive source database: %w", err)
	}
	roots := append([]string{filepath.Dir(configuredDB), filepath.Dir(resolvedDB)}, protected...)
	resolvedRoots := make([]string, 0, len(roots))
	for _, root := range roots {
		if root == "" {
			continue
		}
		configuredRoot, err := absoluteConfiguredPath(filepath.FromSlash(root))
		if err != nil {
			return "", nil, err
		}
		if pathsOverlap(requestedTarget, configuredRoot) || pathsOverlap(resolvedTarget, configuredRoot) {
			return "", nil, fmt.Errorf("%w: archive restore target overlaps configured storage %q", errs.ErrBackupRestoreTargetOverlap, configuredRoot)
		}
		resolved, err := canonicalUnavailableConfigPath(configuredRoot)
		if err != nil {
			return "", nil, fmt.Errorf("resolve archive source storage: %w", err)
		}
		resolvedRoots = append(resolvedRoots, resolved)
	}
	return resolvedTarget, resolvedRoots, nil
}
