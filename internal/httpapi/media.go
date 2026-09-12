package httpapi

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"go.kenn.io/fotobank/internal/auth/hidden"
	"go.kenn.io/fotobank/internal/errs"
	"go.kenn.io/fotobank/internal/media"
	"go.kenn.io/fotobank/internal/service"
)

// MediaDTO is the JSON shape of one media row. ThumbStatus and
// ThumbVersion are included so clients can decide whether to issue a
// /thumb request and cache-bust via the ?v= param when a regenerate
// bumps the version.
type MediaDTO struct {
	ID               string     `json:"id"`
	Type             string     `json:"type"`
	MimeType         string     `json:"mime_type"`
	OriginalFilename string     `json:"original_filename,omitempty"`
	ImportedAt       time.Time  `json:"imported_at"`
	Timestamp        *time.Time `json:"timestamp,omitempty"`
	Size             int64      `json:"size"`
	SHA256           string     `json:"sha256"`
	ThumbStatus      string     `json:"thumb_status"`
	ThumbVersion     int        `json:"thumb_version"`
	Make             string     `json:"make,omitempty"`
	Model            string     `json:"model,omitempty"`
	FocalLength      string     `json:"focal_length,omitempty"`
	Shutter          string     `json:"shutter,omitempty"`
	Width            *int       `json:"width,omitempty"`
	Height           *int       `json:"height,omitempty"`
	ISO              *int       `json:"iso,omitempty"`
	Aperture         *float64   `json:"aperture,omitempty"`
	DurationMs       *int64     `json:"duration_ms,omitempty"`
	Latitude         *float64   `json:"latitude,omitempty"`
	Longitude        *float64   `json:"longitude,omitempty"`
	GPSAt            *time.Time `json:"gps_at,omitempty"`
	LocationLabel    string     `json:"location_label,omitempty"`

	Files *[]FileDTO `json:"files,omitzero" nullable:"false"`

	// F2.4 Hidden privacy. Omitted (omitempty) when nil so the field is
	// absent from visible-media responses — minimises client-side noise.
	HiddenAt *time.Time `json:"hidden_at,omitempty"`
}

type FileDTO struct {
	ID               string `json:"id"`
	Role             string `json:"role"`
	MimeType         string `json:"mime_type"`
	OriginalFilename string `json:"original_filename"`
	Size             int64  `json:"size"`
	SHA256           string `json:"sha256"`
}

func toMediaDTO(m media.Media) MediaDTO {
	dto := MediaDTO{
		ID:               m.ID,
		Type:             string(m.Type),
		MimeType:         m.MimeType,
		OriginalFilename: m.OriginalFilename,
		ImportedAt:       m.ImportedAt,
		Timestamp:        m.Timestamp,
		Size:             m.Size,
		SHA256:           m.SHA256,
		ThumbStatus:      m.ThumbStatus,
		ThumbVersion:     m.ThumbVersion,
		Make:             m.Make,
		Model:            m.Model,
		FocalLength:      m.FocalLength,
		Shutter:          m.Shutter,
		Width:            m.Width,
		Height:           m.Height,
		ISO:              m.ISO,
		Aperture:         m.Aperture,
		DurationMs:       m.DurationMs,
		Latitude:         m.Latitude,
		Longitude:        m.Longitude,
		GPSAt:            m.GPSAt,
		LocationLabel:    m.LocationLabel,
	}
	dto.HiddenAt = m.HiddenAt
	return dto
}

type ListMediaInput struct {
	MediaType string   `query:"media_type" doc:"photo or video; anything else returns zero rows"`
	Limit     int      `query:"limit" doc:"max rows to return (default 100, cap 1000)"`
	Offset    int      `query:"offset" doc:"pagination offset"`
	SortDesc  bool     `query:"sort_desc" doc:"sort by timestamp DESC when true"`
	Camera    []string `query:"camera,explode" doc:"narrow to rows whose '<make> <model>' matches any value (OR-composed; repeatable)"`
	Lens      []string `query:"lens,explode" doc:"narrow to rows whose lens_model matches any value (OR-composed; repeatable)"`
	FacetTag  []string `query:"facet_tag,explode" doc:"narrow to rows that carry at least one tag matching any key (repeatable)"`
	// HasGPS is a string with enum {"true","false"} so the tri-state
	// (true / false / unset) round-trips cleanly. huma v2 does not support
	// pointer-typed query params (panics at registration), so we mirror
	// the /facets workaround and decode the literal string in the handler.
	HasGPS string `query:"has_gps" enum:"true,false" doc:"true: only geotagged rows; false: only non-geotagged; omit for no filter"`
}

type listMediaOutput struct {
	Body MediaListResult
}

// MediaListResult is one page of visible photos and videos.
type MediaListResult struct {
	Items      []MediaDTO `json:"items"`
	NextOffset *int       `json:"next_offset,omitempty"`
	Total      *int       `json:"total,omitempty"`
}

type getMediaInput struct {
	ID string `path:"id"`
}

type getMediaOutput struct {
	Body MediaDTO
}

const (
	listMediaDefaultLimit = 100
	listMediaMaxLimit     = 1000
)

// registerMedia wires the /api/v1/media list and detail routes onto api.
// Operations are registered unconditionally so the generated OpenAPI
// spec documents them even for callers (OpenAPISpec dumper, tests) that
// pass a nil MediaService. When svc is nil the handlers answer 503
// Service Unavailable, which keeps the schema honest without requiring
// a real service.
func registerMedia(api huma.API, svc *service.MediaService) {
	huma.Register(api, huma.Operation{
		OperationID: "list-media",
		Method:      http.MethodGet,
		Path:        "/api/v1/media",
		Summary:     "List media visible to the caller",
	}, func(ctx context.Context, in *ListMediaInput) (*listMediaOutput, error) {
		if svc == nil {
			return nil, huma.Error503ServiceUnavailable("media service not configured")
		}
		id, ok := IdentityFromContext(ctx)
		if !ok {
			return nil, huma.Error401Unauthorized(errs.ErrIdentityMissing.Error())
		}
		limit := in.Limit
		if limit <= 0 {
			limit = listMediaDefaultLimit
		}
		if limit > listMediaMaxLimit {
			limit = listMediaMaxLimit
		}
		// Fetch one extra row so we can emit next_offset only when a
		// real continuation row exists, not merely because the page was
		// full by coincidence.
		filter := media.ListFilter{
			Owner:      id.Principal.OwnersPrincipal(),
			Limit:      limit + 1,
			Offset:     in.Offset,
			SortDesc:   in.SortDesc,
			Cameras:    in.Camera,
			Lenses:     in.Lens,
			AnyTagKeys: in.FacetTag,
		}
		if in.MediaType != "" {
			t := media.Type(in.MediaType)
			filter.Type = &t
		}
		// HasGPS arrives as the literal "true"/"false" string (or "" for
		// "not supplied") because huma v2 panics on *bool query params.
		// Decode here into the pointer-shaped media.ListFilter field.
		if in.HasGPS != "" {
			v := in.HasGPS == "true"
			filter.HasGPS = &v
		}
		rows, err := svc.List(ctx, filter, id.Principal.OwnersPrincipal())
		if err != nil {
			return nil, err
		}
		out := &listMediaOutput{}
		hasMore := len(rows) > limit
		if hasMore {
			rows = rows[:limit]
			next := in.Offset + limit
			out.Body.NextOffset = &next
		}
		out.Body.Items = make([]MediaDTO, 0, len(rows))
		for _, m := range rows {
			out.Body.Items = append(out.Body.Items, toMediaDTO(m))
		}
		return out, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "get-media",
		Method:      http.MethodGet,
		Path:        "/api/v1/media/{id}",
		Summary:     "Return the detail for a single media item",
	}, func(ctx context.Context, in *getMediaInput) (*getMediaOutput, error) {
		if svc == nil {
			return nil, huma.Error503ServiceUnavailable("media service not configured")
		}
		id, ok := IdentityFromContext(ctx)
		if !ok {
			return nil, huma.Error401Unauthorized(errs.ErrIdentityMissing.Error())
		}
		caller := id.Principal.OwnersPrincipal()
		// Honor the unlock claim for direct-by-id reads. A hidden row with a
		// valid unlock cookie from the same principal returns 200; without a
		// cookie it returns 404 (anti-enumeration).
		includeHidden := false
		if claim, hasClaim := hidden.UnlockClaimFromContext(ctx); hasClaim && claim.Principal == caller {
			includeHidden = true
		}
		m, err := svc.Get(ctx, in.ID, caller, includeHidden)
		if err != nil {
			if errors.Is(err, errs.ErrNotFound) {
				return nil, huma.Error404NotFound("media not found")
			}
			return nil, err
		}
		dto := toMediaDTO(m)
		filesDTO := make([]FileDTO, 0)
		files, err := svc.ListFiles(ctx, m.ID, caller, includeHidden)
		if err != nil {
			return nil, err
		}
		for _, file := range files {
			filesDTO = append(filesDTO, FileDTO{
				ID: file.ID, Role: string(file.Role), MimeType: file.MimeType,
				OriginalFilename: file.OriginalFilename, Size: file.Size, SHA256: file.SHA256,
			})
		}
		dto.Files = &filesDTO
		return &getMediaOutput{Body: dto}, nil
	})
}
