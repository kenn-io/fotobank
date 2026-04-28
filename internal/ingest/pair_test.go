package ingest_test

import (
	"path/filepath"
	"sort"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wesm/fotobank/internal/ingest"
)

func TestPairClassFromMime(t *testing.T) {
	r := require.New(t)
	r.Equal(ingest.PairClassJPEG, ingest.PairClassFromMime("image/jpeg"))
	r.Equal(ingest.PairClassRAW, ingest.PairClassFromMime("image/x-sony-arw"))
	r.Equal(ingest.PairClassRAW, ingest.PairClassFromMime("image/x-fuji-raf"))
	r.Equal(ingest.PairClassRAW, ingest.PairClassFromMime("image/x-adobe-dng"))
	r.Equal(ingest.PairClassRAW, ingest.PairClassFromMime("image/x-canon-cr2"))
	r.Equal(ingest.PairClassRAW, ingest.PairClassFromMime("image/x-nikon-nef"))
	r.Equal(ingest.PairClassOther, ingest.PairClassFromMime("image/png"))
	r.Equal(ingest.PairClassOther, ingest.PairClassFromMime("video/mp4"))
}

type byID []ingest.PairUpdate

func (a byID) Len() int           { return len(a) }
func (a byID) Less(i, j int) bool { return a[i].ID < a[j].ID }
func (a byID) Swap(i, j int)      { a[i], a[j] = a[j], a[i] }

func cand(id, dir, base, mime string, current *string) ingest.PairCandidate {
	return ingest.PairCandidate{
		ID:                id,
		ImportSourcePath:  filepath.Join(dir, base),
		Class:             ingest.PairClassFromMime(mime),
		MimeType:          mime,
		CurrentPairedWith: current,
	}
}

func TestPairComputeBasicOneJPEGOneRAW(t *testing.T) {
	r := require.New(t)
	rows := []ingest.PairCandidate{
		cand("p", "trip", "IMG_1.JPG", "image/jpeg", nil),
		cand("s", "trip", "IMG_1.DNG", "image/x-adobe-dng", nil),
	}
	updates := ingest.Compute(rows)
	r.Len(updates, 1)
	r.Equal("s", updates[0].ID)
	r.NotNil(updates[0].PairedWithID)
	r.Equal("p", *updates[0].PairedWithID)
}

func TestPairComputeIdempotency(t *testing.T) {
	r := require.New(t)
	primary := "p"
	rows := []ingest.PairCandidate{
		cand("p", "trip", "IMG_1.JPG", "image/jpeg", nil),
		cand("s", "trip", "IMG_1.DNG", "image/x-adobe-dng", &primary),
	}
	// Sidecar already paired correctly — Compute returns no updates.
	updates := ingest.Compute(rows)
	r.Empty(updates)
}

func TestPairComputeCommutativity(t *testing.T) {
	r := require.New(t)
	rowsA := []ingest.PairCandidate{
		cand("p", "trip", "IMG_1.JPG", "image/jpeg", nil),
		cand("s1", "trip", "IMG_1.DNG", "image/x-adobe-dng", nil),
		cand("s2", "trip", "IMG_1.ARW", "image/x-sony-arw", nil),
	}
	rowsB := []ingest.PairCandidate{
		cand("s2", "trip", "IMG_1.ARW", "image/x-sony-arw", nil),
		cand("p", "trip", "IMG_1.JPG", "image/jpeg", nil),
		cand("s1", "trip", "IMG_1.DNG", "image/x-adobe-dng", nil),
	}
	updatesA := ingest.Compute(rowsA)
	updatesB := ingest.Compute(rowsB)
	sort.Sort(byID(updatesA))
	sort.Sort(byID(updatesB))
	r.Equal(updatesA, updatesB)
}

func TestPairComputeOneJPEGTwoRAWPairsAll(t *testing.T) {
	r := require.New(t)
	rows := []ingest.PairCandidate{
		cand("p", "trip", "IMG_1.JPG", "image/jpeg", nil),
		cand("s1", "trip", "IMG_1.DNG", "image/x-adobe-dng", nil),
		cand("s2", "trip", "IMG_1.ARW", "image/x-sony-arw", nil),
	}
	updates := ingest.Compute(rows)
	sort.Sort(byID(updates))
	r.Len(updates, 2)
	for _, u := range updates {
		r.NotNil(u.PairedWithID)
		r.Equal("p", *u.PairedWithID)
	}
}

func TestPairComputeAmbiguousTwoJPEGsLeaveRAWUnpaired(t *testing.T) {
	r := require.New(t)
	rows := []ingest.PairCandidate{
		cand("p1", "trip", "IMG_1.JPG", "image/jpeg", nil),
		cand("p2", "trip", "IMG_1.JPEG", "image/jpeg", nil),
		cand("s", "trip", "IMG_1.DNG", "image/x-adobe-dng", nil),
	}
	updates := ingest.Compute(rows)
	// All three rows are already nil; an ambiguous group with no
	// existing pairings is a no-op (Compute suppresses nil → nil
	// updates via pairEqual). The clearing-from-existing-pair case
	// is covered by TestPairComputeAmbiguityFlipsExistingPairToNull.
	r.Empty(updates)
}

func TestPairComputeAmbiguityFlipsExistingPairToNull(t *testing.T) {
	r := require.New(t)
	primary := "p1"
	rows := []ingest.PairCandidate{
		cand("p1", "trip", "IMG_1.JPG", "image/jpeg", nil),
		cand("p2", "trip", "IMG_1.JPEG", "image/jpeg", nil),
		// Sidecar was already paired to p1. Adding p2 makes the
		// directory ambiguous; Compute must emit s → NULL.
		cand("s", "trip", "IMG_1.DNG", "image/x-adobe-dng", &primary),
	}
	updates := ingest.Compute(rows)
	r.Len(updates, 1)
	r.Equal("s", updates[0].ID)
	r.Nil(updates[0].PairedWithID)
}

func TestPairComputeCaseInsensitiveStem(t *testing.T) {
	r := require.New(t)
	rows := []ingest.PairCandidate{
		cand("p", "trip", "img_1.jpg", "image/jpeg", nil),
		cand("s", "trip", "IMG_1.DNG", "image/x-adobe-dng", nil),
	}
	updates := ingest.Compute(rows)
	r.Len(updates, 1)
	r.Equal("s", updates[0].ID)
	r.NotNil(updates[0].PairedWithID)
	r.Equal("p", *updates[0].PairedWithID)
}

func TestPairComputeIgnoresEmptySourcePath(t *testing.T) {
	r := require.New(t)
	rows := []ingest.PairCandidate{
		{ID: "x", ImportSourcePath: "", Class: ingest.PairClassJPEG, MimeType: "image/jpeg"},
		{ID: "y", ImportSourcePath: "", Class: ingest.PairClassRAW, MimeType: "image/x-adobe-dng"},
	}
	updates := ingest.Compute(rows)
	r.Empty(updates)
}

func TestPairComputeNFCNormalizesDirectory(t *testing.T) {
	r := require.New(t)
	// "café" in NFC vs NFD. Pairing must not split the directory by
	// normalization form — both rows are same dir, same stem. We build
	// the strings programmatically because some editors auto-normalize
	// literals to NFC, which would defeat the test.
	dirNFC := "café"           // é precomposed
	dirNFD := "café"          // e + combining acute
	r.NotEqual(dirNFC, dirNFD) // sanity: byte-distinct inputs
	rows := []ingest.PairCandidate{
		cand("p", dirNFC, "IMG_1.JPG", "image/jpeg", nil),
		cand("s", dirNFD, "IMG_1.DNG", "image/x-adobe-dng", nil),
	}
	updates := ingest.Compute(rows)
	r.Len(updates, 1)
	r.Equal("s", updates[0].ID)
	r.NotNil(updates[0].PairedWithID)
	r.Equal("p", *updates[0].PairedWithID)
}

func TestPairComputeMultipleStemsInSameDir(t *testing.T) {
	r := require.New(t)
	rows := []ingest.PairCandidate{
		cand("p1", "trip", "IMG_1.JPG", "image/jpeg", nil),
		cand("s1", "trip", "IMG_1.DNG", "image/x-adobe-dng", nil),
		cand("p2", "trip", "IMG_2.JPG", "image/jpeg", nil),
		cand("s2", "trip", "IMG_2.DNG", "image/x-adobe-dng", nil),
	}
	updates := ingest.Compute(rows)
	sort.Sort(byID(updates))
	r.Len(updates, 2)
	r.Equal("s1", updates[0].ID)
	r.NotNil(updates[0].PairedWithID)
	r.Equal("p1", *updates[0].PairedWithID)
	r.Equal("s2", updates[1].ID)
	r.NotNil(updates[1].PairedWithID)
	r.Equal("p2", *updates[1].PairedWithID)
}

func TestPairComputeSameStemDifferentDirsDoNotCrossPair(t *testing.T) {
	r := require.New(t)
	rows := []ingest.PairCandidate{
		cand("pA", "tripA", "IMG_1.JPG", "image/jpeg", nil),
		cand("sA", "tripA", "IMG_1.DNG", "image/x-adobe-dng", nil),
		cand("pB", "tripB", "IMG_1.JPG", "image/jpeg", nil),
		cand("sB", "tripB", "IMG_1.DNG", "image/x-adobe-dng", nil),
	}
	updates := ingest.Compute(rows)
	sort.Sort(byID(updates))
	r.Len(updates, 2)
	r.Equal("sA", updates[0].ID)
	r.Equal("pA", *updates[0].PairedWithID)
	r.Equal("sB", updates[1].ID)
	r.Equal("pB", *updates[1].PairedWithID)
}

func TestPairComputeMissingPrimaryClearsExistingPair(t *testing.T) {
	r := require.New(t)
	stalePrimary := "p-gone"
	rows := []ingest.PairCandidate{
		// Primary "p-gone" is not in the candidate set (e.g., deleted
		// or out of scope for this slice). The RAW points at it.
		// Compute must clear that pair since the bucket has 0 JPEGs.
		cand("s", "trip", "IMG_1.DNG", "image/x-adobe-dng", &stalePrimary),
	}
	updates := ingest.Compute(rows)
	r.Len(updates, 1)
	r.Equal("s", updates[0].ID)
	r.Nil(updates[0].PairedWithID)
}
