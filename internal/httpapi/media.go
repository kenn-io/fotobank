package httpapi

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/wesm/fotobank/internal/auth/hidden"
	"github.com/wesm/fotobank/internal/errs"
	"github.com/wesm/fotobank/internal/media"
	"github.com/wesm/fotobank/internal/service"
)

// mediaDTO is the JSON shape of one media row. ThumbStatus and
// ThumbVersion are included so clients can decide whether to issue a
// /thumb request and cache-bust via the ?v= param when a regenerate
// bumps the version.
type mediaDTO struct {
	ID               string     `json:"id"`
	Type             string     `json:"type"`
	MimeType         string     `json:"mime_type"`
	Path             string     `json:"path"`
	OriginalFilename string     `json:"original_filename,omitempty"`
	ImportedAt       time.Time  `json:"imported_at"`
	Timestamp        *time.Time `json:"timestamp,omitempty"`
	Size             int64      `json:"size"`
	Checksum         string     `json:"checksum"`
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

	// F2.2 RAW + JPEG pairing. PairedWithID is set on every response
	// where the row is a sidecar. PairedWith is populated only by the
	// detail handler when the row is a sidecar; Sidecars is populated
	// only by the detail handler when the row is a primary. None of
	// the three are populated by the list endpoint or by toMediaDTO
	// directly — see the get-media handler.
	PairedWithID *string         `json:"paired_with_id,omitempty"`
	PairedWith   *pairSummaryDTO `json:"paired_with,omitempty"`
	Sidecars     []mediaDTO      `json:"sidecars,omitempty"`

	// F2.4 Hidden privacy. Omitted (omitempty) when nil so the field is
	// absent from visible-media responses — minimises client-side noise.
	HiddenAt *time.Time `json:"hidden_at,omitempty"`
}

// pairSummaryDTO is the slim primary-side projection embedded under a
// sidecar's `paired_with` field. Carries just enough for the frontend
// to render a "View JPEG" link without round-tripping again.
type pairSummaryDTO struct {
	ID               string `json:"id"`
	OriginalFilename string `json:"original_filename"`
}

func toMediaDTO(m media.Media) mediaDTO {
	dto := mediaDTO{
		ID:               m.ID,
		Type:             string(m.Type),
		MimeType:         m.MimeType,
		Path:             m.Path,
		OriginalFilename: m.OriginalFilename,
		ImportedAt:       m.ImportedAt,
		Timestamp:        m.Timestamp,
		Size:             m.Size,
		Checksum:         m.Checksum,
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
	dto.PairedWithID = m.PairedWithID
	dto.HiddenAt = m.HiddenAt
	return dto
}

type listMediaInput struct {
	MediaType string `query:"media_type" doc:"photo or video; anything else returns zero rows"`
	Limit     int    `query:"limit" doc:"max rows to return (default 100, cap 1000)"`
	Offset    int    `query:"offset" doc:"pagination offset"`
	SortDesc  bool   `query:"sort_desc" doc:"sort by timestamp DESC when true"`
}

type listMediaOutput struct {
	Body struct {
		Items      []mediaDTO `json:"items"`
		NextOffset *int       `json:"next_offset,omitempty"`
		Total      *int       `json:"total,omitempty"`
	}
}

type getMediaInput struct {
	ID string `path:"id"`
}

type getMediaOutput struct {
	Body mediaDTO
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
	}, func(ctx context.Context, in *listMediaInput) (*listMediaOutput, error) {
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
			Owner:    id.Principal.OwnersPrincipal(),
			Limit:    limit + 1,
			Offset:   in.Offset,
			SortDesc: in.SortDesc,
		}
		if in.MediaType != "" {
			t := media.Type(in.MediaType)
			filter.Type = &t
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
		out.Body.Items = make([]mediaDTO, 0, len(rows))
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
		if dto.PairedWithID == nil {
			// Primary path: embed any sidecars on the response so the
			// frontend can render a "Files" row without an extra trip.
			sidecars, err := svc.GetSidecars(ctx, m.ID, caller, includeHidden)
			if err != nil {
				return nil, err
			}
			// Leave Sidecars nil when the primary has none so omitempty
			// drops the field from the wire instead of emitting "[]".
			if len(sidecars) > 0 {
				dto.Sidecars = make([]mediaDTO, 0, len(sidecars))
				for _, s := range sidecars {
					child := toMediaDTO(s)
					child.PairedWith = &pairSummaryDTO{
						ID:               m.ID,
						OriginalFilename: m.OriginalFilename,
					}
					dto.Sidecars = append(dto.Sidecars, child)
				}
			}
		} else {
			// Sidecar path: surface a primary summary so the frontend
			// can offer a "View JPEG" affordance. The schema's
			// media_paired_with_owner_consistency_* triggers guarantee
			// the primary stays visible to the caller; the only
			// legitimate miss is an ON DELETE SET NULL race where the
			// primary was deleted between the sidecar fetch and this
			// follow-up. Treat that as PairedWith nil; surface every
			// other error.
			primary, err := svc.Get(ctx, *m.PairedWithID, caller, includeHidden)
			switch {
			case err == nil:
				dto.PairedWith = &pairSummaryDTO{
					ID:               primary.ID,
					OriginalFilename: primary.OriginalFilename,
				}
			case errors.Is(err, errs.ErrNotFound):
				// Stale FK after ON DELETE SET NULL race — leave
				// PairedWith nil.
			default:
				return nil, err
			}
		}
		return &getMediaOutput{Body: dto}, nil
	})
}
