package exifread

import (
	"fmt"
	"os"
	"time"

	"github.com/abema/go-mp4"
)

// ExtractVideo reads container metadata from the given path based on
// mime. MP4/MOV/M4V are parsed via abema/go-mp4; other containers
// return an empty Metadata with a nil error (best-effort).
func ExtractVideo(path, mime string) (Metadata, error) {
	switch mime {
	case "video/mp4", "video/quicktime", "video/x-m4v":
		return extractMP4(path)
	default:
		return Metadata{}, nil
	}
}

// mp4Epoch is 1904-01-01 UTC; MP4 timestamps are seconds since this.
var mp4Epoch = time.Date(1904, 1, 1, 0, 0, 0, 0, time.UTC)

func extractMP4(path string) (Metadata, error) {
	f, err := os.Open(path)
	if err != nil {
		return Metadata{}, err
	}
	defer f.Close()

	boxes, err := mp4.ExtractBoxesWithPayload(f, nil, []mp4.BoxPath{
		{mp4.BoxTypeMoov(), mp4.BoxTypeMvhd()},
		{mp4.BoxTypeMoov(), mp4.BoxTypeTrak(), mp4.BoxTypeTkhd()},
	})
	if err != nil {
		return Metadata{}, fmt.Errorf("mp4 extract: %w", err)
	}

	var m Metadata
	for _, box := range boxes {
		switch b := box.Payload.(type) {
		case *mp4.Mvhd:
			if secs := b.GetCreationTime(); secs != 0 && !isMP4UnknownSentinel(b.Version, secs) {
				ts := mp4Epoch.Add(time.Duration(secs) * time.Second)
				m.Timestamp = &ts
			}
			if ts := b.Timescale; ts > 0 {
				dur := b.GetDuration()
				if !isMP4UnknownSentinel(b.Version, dur) {
					m.DurationMs = int64(dur) * 1000 / int64(ts)
				}
			}
		case *mp4.Tkhd:
			if w := int(b.GetWidthInt()); w > 0 && m.Width == 0 {
				m.Width = w
			}
			if h := int(b.GetHeightInt()); h > 0 && m.Height == 0 {
				m.Height = h
			}
		}
	}
	return m, nil
}

// isMP4UnknownSentinel reports whether a value should be treated as the
// MP4 "unknown" marker for a version-0 (uint32 all-ones) or version-1
// (uint64 all-ones) duration/creation-time field.
func isMP4UnknownSentinel(version uint8, v uint64) bool {
	if version == 0 {
		return v == 0xFFFFFFFF
	}
	return v == 0xFFFFFFFFFFFFFFFF
}
