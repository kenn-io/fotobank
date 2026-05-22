// Package ingest — pair.go holds the F2.2 pairing classifier and the
// pure Compute function. The classifier maps mime to the JPEG / RAW /
// Other extension class used by §5.1 of the design doc. Compute is
// the deterministic, idempotent pure function consumed by both the
// importer's post-import barrier pass and the fotobank pair backfill
// CLI.
package ingest

import (
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/text/unicode/norm"

	"go.kenn.io/fotobank/internal/owners"
)

// PairClass enumerates the pairing role of a row's mime type.
type PairClass int

const (
	PairClassOther PairClass = iota
	PairClassJPEG
	PairClassRAW
)

// PairClassFromMime maps a media mime to its pair class. Unknown
// mimes are PairClassOther — Compute will skip them.
func PairClassFromMime(mime string) PairClass {
	switch mime {
	case "image/jpeg":
		return PairClassJPEG
	case "image/x-sony-arw",
		"image/x-fuji-raf",
		"image/x-adobe-dng",
		"image/x-canon-cr2",
		"image/x-nikon-nef":
		return PairClassRAW
	}
	return PairClassOther
}

// PairCandidate is the input row for Compute. The pairing pass
// projects media rows down to this minimal shape so the pure
// function has no dependency on internal/media.
//
// Owner is part of the bucket key so a buggy caller passing rows
// for multiple principals can never emit cross-owner pair updates;
// production callers (importer, CLI backfill) already invoke Compute
// per-principal but the function defends against future misuse.
type PairCandidate struct {
	ID               string
	Owner            owners.Principal
	ImportSourcePath string
	Class            PairClass
	MimeType         string
	// CurrentPairedWith is the row's existing paired_with_id. nil
	// means NULL. Compute uses this to emit only the updates needed
	// to reach the desired state.
	CurrentPairedWith *string
}

// PairUpdate describes a single paired_with_id mutation. nil
// PairedWithID means SET paired_with_id = NULL.
type PairUpdate struct {
	ID           string
	PairedWithID *string
}

// Compute returns the deterministic set of paired_with_id updates
// needed to bring rows into the F2.2 pairing contract (§5). The
// function is idempotent and commutative; running it twice on the
// same row set yields identical output, and reordering the input
// does not change the output.
//
// Rows with empty ImportSourcePath are skipped. Rows whose Class is
// PairClassOther never pair. Updates are sorted by ID so callers
// observe a stable order regardless of iteration nondeterminism.
func Compute(rows []PairCandidate) []PairUpdate {
	type bucketKey struct {
		ownerHub    string
		ownerUserID string
		dir         string
		stem        string
	}
	type bucket struct {
		jpegs []PairCandidate
		raws  []PairCandidate
	}
	buckets := make(map[bucketKey]*bucket)
	for _, row := range rows {
		if row.ImportSourcePath == "" {
			continue
		}
		if row.Class != PairClassJPEG && row.Class != PairClassRAW {
			continue
		}
		dir := norm.NFC.String(filepath.Dir(row.ImportSourcePath))
		stem := strings.ToLower(strings.TrimSuffix(
			filepath.Base(row.ImportSourcePath),
			filepath.Ext(row.ImportSourcePath),
		))
		stem = norm.NFC.String(stem)
		k := bucketKey{
			ownerHub:    row.Owner.Hub,
			ownerUserID: row.Owner.UserID,
			dir:         dir,
			stem:        stem,
		}
		b := buckets[k]
		if b == nil {
			b = &bucket{}
			buckets[k] = b
		}
		switch row.Class {
		case PairClassJPEG:
			b.jpegs = append(b.jpegs, row)
		case PairClassRAW:
			b.raws = append(b.raws, row)
		}
	}

	// Initialize: every covered (non-empty path, JPEG or RAW) row
	// desires nil. JPEGs are always primaries so they keep that nil;
	// RAWs only override below when their bucket has exactly one JPEG.
	// Initializing JPEGs ensures a stale paired_with_id on a JPEG (a
	// row that should always be a primary) is cleared on a full
	// recompute.
	desired := make(map[string]*string, len(rows))
	for _, row := range rows {
		if row.ImportSourcePath == "" {
			continue
		}
		if row.Class != PairClassJPEG && row.Class != PairClassRAW {
			continue
		}
		desired[row.ID] = nil
	}
	for _, b := range buckets {
		if len(b.jpegs) == 1 {
			primaryID := b.jpegs[0].ID
			for _, raw := range b.raws {
				p := primaryID
				desired[raw.ID] = &p
			}
		}
	}

	out := make([]PairUpdate, 0, len(desired))
	for _, row := range rows {
		want, hasDesired := desired[row.ID]
		if !hasDesired {
			continue
		}
		if pairEqual(row.CurrentPairedWith, want) {
			continue
		}
		out = append(out, PairUpdate{ID: row.ID, PairedWithID: want})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func pairEqual(a, b *string) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return *a == *b
}
