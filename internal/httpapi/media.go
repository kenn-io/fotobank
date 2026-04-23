package httpapi

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/wesm/fotobank/internal/errs"
	"github.com/wesm/fotobank/internal/media"
	"github.com/wesm/fotobank/internal/service"
)

// mediaDTO is the JSON shape of one media row. Thumb fields are
// deliberately omitted so they stay internal until Plan C exposes
// thumbnails.
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
	Make             string     `json:"make,omitempty"`
	Model            string     `json:"model,omitempty"`
	FocalLength      string     `json:"focal_length,omitempty"`
	Shutter          string     `json:"shutter,omitempty"`
	Width            *int       `json:"width,omitempty"`
	Height           *int       `json:"height,omitempty"`
	ISO              *int       `json:"iso,omitempty"`
	Aperture         *float64   `json:"aperture,omitempty"`
	DurationMs       *int64     `json:"duration_ms,omitempty"`
}

func toMediaDTO(m media.Media) mediaDTO {
	return mediaDTO{
		ID:               m.ID,
		Type:             string(m.Type),
		MimeType:         m.MimeType,
		Path:             m.Path,
		OriginalFilename: m.OriginalFilename,
		ImportedAt:       m.ImportedAt,
		Timestamp:        m.Timestamp,
		Size:             m.Size,
		Checksum:         m.Checksum,
		Make:             m.Make,
		Model:            m.Model,
		FocalLength:      m.FocalLength,
		Shutter:          m.Shutter,
		Width:            m.Width,
		Height:           m.Height,
		ISO:              m.ISO,
		Aperture:         m.Aperture,
		DurationMs:       m.DurationMs,
	}
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

// registerMedia wires the /api/v1/media list and detail routes onto api
// when svc is non-nil. Callers that don't need media HTTP access (for
// example the OpenAPI spec dumper or tests that only exercise /me) pass
// a Deps without a MediaService; this function then returns without
// registering anything.
func registerMedia(api huma.API, svc *service.MediaService) {
	if svc == nil {
		return
	}
	huma.Register(api, huma.Operation{
		OperationID: "list-media",
		Method:      http.MethodGet,
		Path:        "/api/v1/media",
		Summary:     "List media visible to the caller",
	}, func(ctx context.Context, in *listMediaInput) (*listMediaOutput, error) {
		id, ok := IdentityFromContext(ctx)
		if !ok {
			return nil, huma.Error401Unauthorized(errs.ErrIdentityMissing.Error())
		}
		filter := media.ListFilter{
			Owner:    id.Principal.OwnersPrincipal(),
			Limit:    in.Limit,
			Offset:   in.Offset,
			SortDesc: in.SortDesc,
		}
		if in.MediaType != "" {
			t := media.Type(in.MediaType)
			filter.Type = &t
		}
		if filter.Limit <= 0 {
			filter.Limit = listMediaDefaultLimit
		}
		if filter.Limit > listMediaMaxLimit {
			filter.Limit = listMediaMaxLimit
		}
		rows, err := svc.List(ctx, filter, id.Principal.OwnersPrincipal())
		if err != nil {
			return nil, err
		}
		out := &listMediaOutput{}
		out.Body.Items = make([]mediaDTO, 0, len(rows))
		for _, m := range rows {
			out.Body.Items = append(out.Body.Items, toMediaDTO(m))
		}
		if len(rows) == filter.Limit {
			next := filter.Offset + filter.Limit
			out.Body.NextOffset = &next
		}
		return out, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "get-media",
		Method:      http.MethodGet,
		Path:        "/api/v1/media/{id}",
		Summary:     "Return the detail for a single media item",
	}, func(ctx context.Context, in *getMediaInput) (*getMediaOutput, error) {
		id, ok := IdentityFromContext(ctx)
		if !ok {
			return nil, huma.Error401Unauthorized(errs.ErrIdentityMissing.Error())
		}
		m, err := svc.Get(ctx, in.ID, id.Principal.OwnersPrincipal())
		if err != nil {
			if errors.Is(err, errs.ErrNotFound) {
				return nil, huma.Error404NotFound("media not found")
			}
			return nil, err
		}
		return &getMediaOutput{Body: toMediaDTO(m)}, nil
	})
}
