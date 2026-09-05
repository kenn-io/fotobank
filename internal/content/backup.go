package content

import (
	"context"
	"errors"
	"fmt"

	"go.kenn.io/docbank"
)

// BackupRepository is an initialized immutable Docbank snapshot repository.
// The Docbank implementation remains private to the content boundary.
type BackupRepository struct {
	repository *docbank.BackupRepository
}

func (r *BackupRepository) ID() string {
	if r == nil || r.repository == nil {
		return ""
	}
	return r.repository.ID()
}

func (r *BackupRepository) Root() string {
	if r == nil || r.repository == nil {
		return ""
	}
	return r.repository.Root()
}

type BackupOptions struct {
	Tag         string
	ZstdLevel   int
	Jobs        int
	ForceUnlock bool
	// AllowPlaintextSecrets permits ExtraFiles marked Sensitive to be stored
	// in the current plaintext backup repository.
	AllowPlaintextSecrets bool
	// Prepare runs during Docbank's short content freeze. It may create the
	// immutable host files declared by ExtraFiles; those files must remain
	// unchanged until CreateBackup returns.
	Prepare    func(context.Context) error
	ExtraFiles []BackupExtraFile
	Progress   func(BackupProgress)
}

type BackupExtraFile struct {
	Path      string
	RecordAs  string
	Sensitive bool
}

type BackupProgress struct {
	Stage      string
	Done       int64
	Total      int64
	BytesDone  int64
	BytesTotal int64
	Final      bool
}

type BackupSnapshot struct {
	ID              string  `json:"id"`
	ParentID        string  `json:"parent_id"`
	CreatedAt       string  `json:"created_at"`
	Tag             string  `json:"tag"`
	Nodes           int64   `json:"nodes"`
	Files           int64   `json:"files"`
	Blobs           int64   `json:"blobs"`
	BlobBytes       int64   `json:"blob_bytes"`
	BytesAdded      int64   `json:"bytes_added"`
	DurationSeconds float64 `json:"duration_seconds"`
}

type BackupVerifyOptions struct {
	SnapshotID  string
	All         bool
	Quick       bool
	Jobs        int
	ForceUnlock bool
	Progress    func(BackupProgress)
}

type BackupVerifyProblem struct {
	SnapshotID string `json:"snapshot_id"`
	Detail     string `json:"detail"`
}

type BackupVerifyReport struct {
	Snapshots    []string              `json:"snapshots"`
	BlobsChecked int64                 `json:"blobs_checked"`
	BytesRead    int64                 `json:"bytes_read"`
	Problems     []BackupVerifyProblem `json:"problems"`
}

type BackupRestoreOptions struct {
	ProtectedRoots []string
	SnapshotID     string
	Target         string
	Overwrite      bool
	Jobs           int
	ForceUnlock    bool
	Progress       func(BackupProgress)
}

type BackupRestoreReport struct {
	SnapshotID              string
	Target                  string
	DatabasePath            string
	DatabaseBytes           int64
	ContentBlobs            int64
	ContentBytes            int64
	ExtraFiles              int
	ContentVerified         bool
	SQLiteIntegrityVerified bool
}

func InitBackupRepository(root string) (*BackupRepository, error) {
	repository, err := docbank.InitBackupRepository(root)
	if err != nil {
		return nil, fmt.Errorf("initialize content backup repository: %w", translateError(err))
	}
	return &BackupRepository{repository: repository}, nil
}

func OpenBackupRepository(root string) (*BackupRepository, error) {
	repository, err := docbank.OpenBackupRepository(root)
	if err != nil {
		return nil, fmt.Errorf("open content backup repository: %w", translateError(err))
	}
	return &BackupRepository{repository: repository}, nil
}

func (r *BackupRepository) Snapshots() ([]BackupSnapshot, error) {
	if r == nil || r.repository == nil {
		return nil, errors.New("content backup repository is required")
	}
	snapshots, err := r.repository.Snapshots()
	if err != nil {
		return nil, fmt.Errorf("list content backups: %w", translateError(err))
	}
	result := make([]BackupSnapshot, 0, len(snapshots))
	for _, snapshot := range snapshots {
		result = append(result, projectBackupSnapshot(snapshot))
	}
	return result, nil
}

func (r *BackupRepository) Verify(
	ctx context.Context,
	options BackupVerifyOptions,
) (BackupVerifyReport, error) {
	if r == nil || r.repository == nil {
		return BackupVerifyReport{}, errors.New("content backup repository is required")
	}
	report, err := r.repository.Verify(ctx, docbank.BackupVerifyOptions{
		SnapshotID:  options.SnapshotID,
		All:         options.All,
		Quick:       options.Quick,
		Jobs:        options.Jobs,
		ForceUnlock: options.ForceUnlock,
		Progress:    projectBackupProgressCallback(options.Progress),
	})
	if err != nil {
		return BackupVerifyReport{}, fmt.Errorf("verify content backup: %w", translateError(err))
	}
	result := BackupVerifyReport{
		Snapshots:    append([]string(nil), report.Snapshots...),
		BlobsChecked: report.BlobsChecked,
		BytesRead:    report.BytesRead,
		Problems:     make([]BackupVerifyProblem, 0, len(report.Problems)),
	}
	for _, problem := range report.Problems {
		result.Problems = append(result.Problems, BackupVerifyProblem{
			SnapshotID: problem.SnapshotID,
			Detail:     problem.Detail,
		})
	}
	return result, nil
}

// CreateBackup captures one Docbank recovery point together with any declared
// host files. Docbank runs Prepare inside its metadata freeze and releases
// content writers before snapshot bytes stream to the repository.
func (a *Adapter) CreateBackup(
	ctx context.Context,
	repository *BackupRepository,
	options BackupOptions,
) (BackupSnapshot, error) {
	if a == nil || a.vault == nil {
		return BackupSnapshot{}, errors.New("content adapter is required")
	}
	if repository == nil || repository.repository == nil {
		return BackupSnapshot{}, errors.New("content backup repository is required")
	}

	snapshot, err := a.vault.CreateBackup(ctx, repository.repository, docbank.BackupOptions{
		Tag:                   options.Tag,
		ZstdLevel:             options.ZstdLevel,
		Jobs:                  options.Jobs,
		ForceUnlock:           options.ForceUnlock,
		AllowPlaintextSecrets: options.AllowPlaintextSecrets,
		Prepare:               options.Prepare,
		ExtraFiles:            projectBackupExtraFiles(options.ExtraFiles),
		Progress:              projectBackupProgressCallback(options.Progress),
	})
	if err != nil {
		return BackupSnapshot{}, fmt.Errorf("create content backup: %w", translateError(err))
	}
	return projectBackupSnapshot(snapshot), nil
}

func (a *Adapter) RestoreBackup(
	ctx context.Context,
	repository *BackupRepository,
	options BackupRestoreOptions,
) (BackupRestoreReport, error) {
	if a == nil || a.vault == nil {
		return BackupRestoreReport{}, errors.New("content adapter is required")
	}
	if repository == nil || repository.repository == nil {
		return BackupRestoreReport{}, errors.New("content backup repository is required")
	}
	report, err := a.vault.RestoreBackup(ctx, repository.repository, docbank.BackupRestoreOptions{
		SnapshotID:     options.SnapshotID,
		Target:         options.Target,
		ProtectedRoots: append(append([]string(nil), a.managedRoots...), options.ProtectedRoots...),
		Overwrite:      options.Overwrite,
		Jobs:           options.Jobs,
		ForceUnlock:    options.ForceUnlock,
		Progress:       projectBackupProgressCallback(options.Progress),
	})
	if err != nil {
		return BackupRestoreReport{}, fmt.Errorf("restore content backup: %w", translateError(err))
	}
	return projectBackupRestoreReport(report), nil
}

// Restore recovers from the repository without opening the original vault.
// Callers declare live or offline application storage in ProtectedRoots.
func (r *BackupRepository) Restore(ctx context.Context, options BackupRestoreOptions) (BackupRestoreReport, error) {
	if r == nil || r.repository == nil {
		return BackupRestoreReport{}, errors.New("content backup repository is required")
	}
	report, err := r.repository.Restore(ctx, docbank.BackupRestoreOptions{
		SnapshotID: options.SnapshotID, Target: options.Target,
		ProtectedRoots: options.ProtectedRoots, Overwrite: options.Overwrite,
		Jobs: options.Jobs, ForceUnlock: options.ForceUnlock,
		Progress: projectBackupProgressCallback(options.Progress),
	})
	if err != nil {
		return BackupRestoreReport{}, fmt.Errorf("restore content backup: %w", translateError(err))
	}
	return projectBackupRestoreReport(report), nil
}

func projectBackupRestoreReport(report docbank.BackupRestoreReport) BackupRestoreReport {
	return BackupRestoreReport{
		SnapshotID:              report.SnapshotID,
		Target:                  report.Target,
		DatabasePath:            report.DatabasePath,
		DatabaseBytes:           report.DatabaseBytes,
		ContentBlobs:            report.ContentBlobs,
		ContentBytes:            report.ContentBytes,
		ExtraFiles:              report.ExtrasFiles,
		ContentVerified:         report.Proof.ContentVerified,
		SQLiteIntegrityVerified: report.Proof.SQLiteIntegrity,
	}
}

func projectBackupExtraFiles(files []BackupExtraFile) []docbank.BackupExtraFile {
	if len(files) == 0 {
		return nil
	}
	result := make([]docbank.BackupExtraFile, len(files))
	for i, file := range files {
		result[i] = docbank.BackupExtraFile{
			Path: file.Path, RecordAs: file.RecordAs, Sensitive: file.Sensitive,
		}
	}
	return result
}

func projectBackupSnapshot(snapshot docbank.BackupSnapshot) BackupSnapshot {
	return BackupSnapshot{
		ID: snapshot.ID, ParentID: snapshot.ParentID, CreatedAt: snapshot.CreatedAt,
		Tag: snapshot.Tag, Nodes: snapshot.Nodes, Files: snapshot.Files,
		Blobs: snapshot.Blobs, BlobBytes: snapshot.BlobBytes,
		BytesAdded: snapshot.BytesAdded, DurationSeconds: snapshot.DurationSeconds,
	}
}

func projectBackupProgress(progress docbank.BackupProgress) BackupProgress {
	return BackupProgress{
		Stage: progress.Stage, Done: progress.Done, Total: progress.Total,
		BytesDone: progress.BytesDone, BytesTotal: progress.BytesTotal, Final: progress.Final,
	}
}

func projectBackupProgressCallback(callback func(BackupProgress)) func(docbank.BackupProgress) {
	if callback == nil {
		return nil
	}
	return func(progress docbank.BackupProgress) { callback(projectBackupProgress(progress)) }
}
