package media

import (
	"fmt"
	"strconv"
	"time"

	"go.kenn.io/fotobank/internal/content"
	"go.kenn.io/fotobank/internal/errs"
)

type SourceMetadataProjection struct {
	VersionID            string
	ExtractorFingerprint string
	Checksum             string
	Timestamp            *time.Time
	Make                 string
	Model                string
	LensModel            string
	FocalLength          string
	Shutter              string
	Width                *int
	Height               *int
	ISO                  *int
	Aperture             *float64
	DurationMs           *int64
	Latitude             *float64
	Longitude            *float64
	GPSAt                *time.Time
	LocationLabel        string
}

type PlaceResolver interface {
	Resolve(lat, lon float64) (label string, ok bool)
}

// ProjectSourceMetadata selects Fotobank's queryable photo facts from one
// exact-version Docbank metadata record. The returned fence must be stored
// with the facts so a later content-version change can invalidate them.
func ProjectSourceMetadata(value content.SourceMetadata, places PlaceResolver) (SourceMetadataProjection, error) {
	projection := SourceMetadataProjection{
		VersionID: value.VersionID, ExtractorFingerprint: value.ExtractorFingerprint,
		Checksum: value.Checksum,
	}
	var err error
	if projection.Timestamp, err = metadataTimestamp(value.Fields, "created"); err != nil {
		return SourceMetadataProjection{}, err
	}
	for key, target := range map[string]*string{
		"image.exif.camera_make":  &projection.Make,
		"image.exif.camera_model": &projection.Model,
		"image.exif.lens_model":   &projection.LensModel,
	} {
		if *target, err = metadataString(value.Fields, key); err != nil {
			return SourceMetadataProjection{}, err
		}
	}
	if focalLength, found, fieldErr := metadataNumber(value.Fields, "image.exif.focal_length_mm"); fieldErr != nil {
		return SourceMetadataProjection{}, fieldErr
	} else if found {
		projection.FocalLength = strconv.FormatFloat(focalLength, 'f', -1, 64)
	}
	if shutter, found, fieldErr := metadataNumber(value.Fields, "image.exif.exposure_time_seconds"); fieldErr != nil {
		return SourceMetadataProjection{}, fieldErr
	} else if found {
		projection.Shutter = strconv.FormatFloat(shutter, 'f', -1, 64)
	}
	if projection.Width, err = metadataDimension(value.Fields,
		"image.exif.pixel_width", "media.container.width_px"); err != nil {
		return SourceMetadataProjection{}, err
	}
	if projection.Height, err = metadataDimension(value.Fields,
		"image.exif.pixel_height", "media.container.height_px"); err != nil {
		return SourceMetadataProjection{}, err
	}
	if projection.ISO, err = metadataPositiveInt(value.Fields, "image.exif.iso"); err != nil {
		return SourceMetadataProjection{}, err
	}
	if aperture, found, fieldErr := metadataNumber(value.Fields, "image.exif.f_number"); fieldErr != nil {
		return SourceMetadataProjection{}, fieldErr
	} else if found && aperture > 0 {
		projection.Aperture = &aperture
	}
	if duration, found, fieldErr := metadataInteger(value.Fields, "media.container.duration_ms"); fieldErr != nil {
		return SourceMetadataProjection{}, fieldErr
	} else if found && duration > 0 {
		projection.DurationMs = &duration
	}
	if err := projectGPS(&projection, value.Fields, places); err != nil {
		return SourceMetadataProjection{}, err
	}
	return projection, nil
}

func projectGPS(projection *SourceMetadataProjection, fields map[string]content.MetadataValue, places PlaceResolver) error {
	latitude, hasLatitude, err := metadataCoordinate(fields, "image.exif.gps_latitude")
	if err != nil {
		return err
	}
	longitude, hasLongitude, err := metadataCoordinate(fields, "image.exif.gps_longitude")
	if err != nil {
		return err
	}
	if hasLatitude != hasLongitude {
		return fmt.Errorf("%w: Docbank metadata contains incomplete GPS coordinates", errs.ErrInvalidArgument)
	}
	if hasLatitude {
		if latitude < -90 || latitude > 90 || longitude < -180 || longitude > 180 {
			return fmt.Errorf("%w: Docbank metadata contains invalid GPS coordinates", errs.ErrInvalidArgument)
		}
		projection.Latitude, projection.Longitude = &latitude, &longitude
		if places != nil {
			if label, ok := places.Resolve(latitude, longitude); ok {
				projection.LocationLabel = label
			}
		}
	}
	projection.GPSAt, err = metadataTimestamp(fields, "image.exif.gps_timestamp")
	return err
}

func metadataString(fields map[string]content.MetadataValue, key string) (string, error) {
	value, found := fields[key]
	if !found {
		return "", nil
	}
	if value.Kind != "string" {
		return "", metadataKindError(key, value.Kind, "string")
	}
	return value.String, nil
}

func metadataInteger(fields map[string]content.MetadataValue, key string) (int64, bool, error) {
	value, found := fields[key]
	if !found {
		return 0, false, nil
	}
	if value.Kind != "integer" || value.Integer == nil {
		return 0, false, metadataKindError(key, value.Kind, "integer")
	}
	return *value.Integer, true, nil
}

func metadataNumber(fields map[string]content.MetadataValue, key string) (float64, bool, error) {
	value, found := fields[key]
	if !found {
		return 0, false, nil
	}
	if value.Kind != "number" || value.Number == nil {
		return 0, false, metadataKindError(key, value.Kind, "number")
	}
	return *value.Number, true, nil
}

func metadataPositiveInt(fields map[string]content.MetadataValue, key string) (*int, error) {
	value, found, err := metadataInteger(fields, key)
	if err != nil || !found || value <= 0 {
		return nil, err
	}
	converted := int(value)
	if int64(converted) != value {
		return nil, fmt.Errorf("%w: Docbank metadata field %q exceeds integer range", errs.ErrInvalidArgument, key)
	}
	return &converted, nil
}

func metadataDimension(fields map[string]content.MetadataValue, keys ...string) (*int, error) {
	for _, key := range keys {
		value, err := metadataPositiveInt(fields, key)
		if err != nil {
			return nil, err
		}
		if value != nil {
			return value, nil
		}
	}
	return nil, nil
}

func metadataCoordinate(fields map[string]content.MetadataValue, key string) (float64, bool, error) {
	value, found := fields[key]
	if !found {
		return 0, false, nil
	}
	if value.Kind != "string" {
		return 0, false, metadataKindError(key, value.Kind, "string")
	}
	parsed, err := strconv.ParseFloat(value.String, 64)
	if err != nil {
		return 0, false, fmt.Errorf("%w: parse Docbank metadata field %q: %v", errs.ErrInvalidArgument, key, err)
	}
	return parsed, true, nil
}

func metadataTimestamp(fields map[string]content.MetadataValue, key string) (*time.Time, error) {
	value, found := fields[key]
	if !found {
		return nil, nil
	}
	if value.Kind != "timestamp" || value.Timestamp == nil {
		return nil, metadataKindError(key, value.Kind, "timestamp")
	}
	stamp := value.Timestamp.Normalized
	if value.Timestamp.Timezone != "omitted" {
		parsed, err := time.Parse(time.RFC3339Nano, stamp)
		if err != nil {
			return nil, fmt.Errorf("%w: parse Docbank metadata field %q: %v", errs.ErrInvalidArgument, key, err)
		}
		return &parsed, nil
	}
	layouts := map[string]string{
		"date": "2006-01-02", "hour": "2006-01-02T15",
		"minute": "2006-01-02T15:04", "second": "2006-01-02T15:04:05",
		"fraction": "2006-01-02T15:04:05.999999999",
	}
	layout, ok := layouts[value.Timestamp.Precision]
	if !ok {
		return nil, fmt.Errorf("%w: unknown Docbank timestamp precision %q", errs.ErrInvalidArgument, value.Timestamp.Precision)
	}
	parsed, err := time.ParseInLocation(layout, stamp, time.UTC)
	if err != nil {
		return nil, fmt.Errorf("%w: parse Docbank metadata field %q: %v", errs.ErrInvalidArgument, key, err)
	}
	return &parsed, nil
}

func metadataKindError(key, got, want string) error {
	return fmt.Errorf("%w: Docbank metadata field %q has kind %q, want %q",
		errs.ErrInvalidArgument, key, got, want)
}
