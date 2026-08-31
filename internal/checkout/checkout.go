// Package checkout materializes writable working copies from immutable
// Docbank versions while retaining Docbank as content authority.
package checkout

import (
	"fmt"
	"time"

	"github.com/google/uuid"

	"go.kenn.io/fotobank/internal/errs"
	"go.kenn.io/fotobank/internal/owners"
)

type State string

const (
	StateBuilding State = "building"
	StateActive   State = "active"
	StateError    State = "error"
)

type EntryState string

const (
	// EntryClean means the last Fotobank observation matched the base version.
	// It is not a live lock or a claim that an external editor has not changed
	// the writable file since that observation.
	EntryClean    EntryState = "clean"
	EntryPending  EntryState = "pending"
	EntryConflict EntryState = "conflict"
	EntryMissing  EntryState = "missing"
	EntryError    EntryState = "error"
)

type ScanCandidateState string

const (
	ScanCandidateSettling ScanCandidateState = "settling"
	ScanCandidatePending  ScanCandidateState = "pending"
)

type YearRange struct {
	Start int
	End   int
}

type Selection struct {
	All      bool
	AssetIDs []string
	AlbumIDs []string
	Years    []YearRange
}

type Checkout struct {
	ID        string
	Owner     owners.Principal
	Root      string
	Layout    string
	Selection Selection
	State     State
	LastError string
	CreatedAt time.Time
	UpdatedAt time.Time
}

type Candidate struct {
	AssetID          string
	FileID           string
	OriginalFilename string
	VersionID        string
	SHA256           string
	Size             int64
	CapturedAt       *time.Time
}

type Estimate struct {
	Files int
	Bytes int64
}

type Entry struct {
	CheckoutID       string
	FileID           string
	RelativePath     string
	BaseVersionID    string
	BaseSHA256       string
	BaseSize         int64
	ObservedSize     int64
	ObservedMTime    time.Time
	ObservedIdentity string
	ObservedSHA256   string
	State            EntryState
	LastError        string
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

type CommitTarget struct {
	Entry       Entry
	AssetID     string
	Role        string
	MediaType   string
	NodeID      int64
	VirtualPath string
}

type CommitReceipt struct {
	NodeID    int64
	VersionID string
	SHA256    string
	Size      int64
}

type CommitResult struct {
	Pending   int
	Committed int
	Conflicts int
}

// ScanCandidate is a durable local-file observation. FileID is empty for a
// newly discovered working file and identifies an Entry for a tracked change.
type ScanCandidate struct {
	CheckoutID       string
	RelativePath     string
	FileID           string
	ObservedSize     int64
	ObservedMTime    time.Time
	ObservedIdentity string
	ObservedSHA256   string
	State            ScanCandidateState
	FirstObserved    time.Time
	LastObserved     time.Time
}

func validateSelection(selection Selection) error {
	count := len(selection.AssetIDs) + len(selection.AlbumIDs) + len(selection.Years)
	if selection.All {
		if count != 0 {
			return fmt.Errorf("%w: all cannot be combined with other checkout selectors", errs.ErrInvalidArgument)
		}
		return nil
	}
	if count == 0 {
		return fmt.Errorf("%w: at least one checkout selector is required", errs.ErrInvalidArgument)
	}
	ids := append(append([]string{}, selection.AssetIDs...), selection.AlbumIDs...)
	for _, id := range ids {
		parsed, err := uuid.Parse(id)
		if err != nil || parsed.String() != id {
			return fmt.Errorf("%w: checkout selectors require canonical UUIDs", errs.ErrInvalidArgument)
		}
	}
	for _, years := range selection.Years {
		if years.Start < 1 || years.End < years.Start || years.End > 9999 {
			return fmt.Errorf("%w: invalid checkout year range", errs.ErrInvalidArgument)
		}
	}
	return nil
}

func normalizeSelection(selection Selection) Selection {
	selection.AssetIDs = uniqueStrings(selection.AssetIDs)
	selection.AlbumIDs = uniqueStrings(selection.AlbumIDs)
	seenYears := make(map[YearRange]struct{}, len(selection.Years))
	var years []YearRange
	for _, yearRange := range selection.Years {
		if _, ok := seenYears[yearRange]; ok {
			continue
		}
		seenYears[yearRange] = struct{}{}
		years = append(years, yearRange)
	}
	selection.Years = years
	return selection
}

func uniqueStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}
