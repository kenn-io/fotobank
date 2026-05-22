package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"go.kenn.io/fotobank/internal/ai/probe"
	"go.kenn.io/fotobank/internal/owners"
	appsettingssvc "go.kenn.io/fotobank/internal/service/appsettings"
)

type adminSettingsGETOutput struct {
	Body appsettingssvc.EffectiveResponse
}

type adminSettingsApplyInput struct {
	Section string `path:"section"`
	Body    struct {
		Values map[string]any `json:"values"`
	}
}

type adminSettingsApplyOutput struct {
	Body appsettingssvc.ApplyResponse
}

type adminSettingsResetKeyInput struct {
	Key string `path:"key"`
}

type adminSettingsResetSectionInput struct {
	Section string `path:"section"`
}

type adminSettingsProbeOutput struct {
	Body probe.Result
}

func registerAdminSettings(api huma.API, svc *appsettingssvc.Service, admins []owners.Principal, logger *slog.Logger) {
	if logger == nil {
		logger = slog.Default()
	}
	huma.Register(api, huma.Operation{
		OperationID: "admin-settings-get",
		Method:      http.MethodGet,
		Path:        "/api/v1/admin/settings",
		Summary:     "Return effective admin settings",
	}, func(ctx context.Context, _ *struct{}) (*adminSettingsGETOutput, error) {
		if _, err := requireAdmin(ctx, admins); err != nil {
			return nil, err
		}
		if svc == nil {
			return nil, huma.Error503ServiceUnavailable("admin settings unavailable")
		}
		resp, err := svc.Effective(ctx)
		if err != nil {
			return nil, translateAdminSettingsError(err)
		}
		return &adminSettingsGETOutput{Body: resp}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "admin-settings-apply-section",
		Method:      http.MethodPut,
		Path:        "/api/v1/admin/settings/sections/{section}",
		Summary:     "Apply one admin settings section",
	}, func(ctx context.Context, in *adminSettingsApplyInput) (*adminSettingsApplyOutput, error) {
		caller, err := requireAdmin(ctx, admins)
		if err != nil {
			return nil, err
		}
		if svc == nil {
			return nil, huma.Error503ServiceUnavailable("admin settings unavailable")
		}
		resp, err := svc.ApplySection(ctx, caller, appsettingssvc.Section(in.Section), in.Body.Values)
		if err != nil {
			return nil, translateAdminSettingsError(err)
		}
		return &adminSettingsApplyOutput{Body: resp}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "admin-settings-reset-key",
		Method:      http.MethodDelete,
		Path:        "/api/v1/admin/settings/keys/{key}",
		Summary:     "Reset one admin settings key",
	}, func(ctx context.Context, in *adminSettingsResetKeyInput) (*adminSettingsApplyOutput, error) {
		caller, err := requireAdmin(ctx, admins)
		if err != nil {
			return nil, err
		}
		if svc == nil {
			return nil, huma.Error503ServiceUnavailable("admin settings unavailable")
		}
		resp, err := svc.ResetKey(ctx, caller, in.Key)
		if err != nil {
			return nil, translateAdminSettingsError(err)
		}
		return &adminSettingsApplyOutput{Body: resp}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "admin-settings-reset-section",
		Method:      http.MethodDelete,
		Path:        "/api/v1/admin/settings/sections/{section}",
		Summary:     "Reset one admin settings section",
	}, func(ctx context.Context, in *adminSettingsResetSectionInput) (*adminSettingsApplyOutput, error) {
		caller, err := requireAdmin(ctx, admins)
		if err != nil {
			return nil, err
		}
		if svc == nil {
			return nil, huma.Error503ServiceUnavailable("admin settings unavailable")
		}
		resp, err := svc.ResetSection(ctx, caller, appsettingssvc.Section(in.Section))
		if err != nil {
			return nil, translateAdminSettingsError(err)
		}
		return &adminSettingsApplyOutput{Body: resp}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "admin-settings-test-vision",
		Method:      http.MethodPost,
		Path:        "/api/v1/admin/settings/test/vision",
		Summary:     "Probe pending vision endpoint settings",
	}, func(ctx context.Context, in *struct{ Body probe.VisionConfig }) (*adminSettingsProbeOutput, error) {
		caller, err := requireAdmin(ctx, admins)
		if err != nil {
			return nil, err
		}
		result := probe.Vision(ctx, in.Body)
		logProbe(logger, caller, "vision", in.Body.Endpoint, result)
		return &adminSettingsProbeOutput{Body: result}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "admin-settings-test-embed",
		Method:      http.MethodPost,
		Path:        "/api/v1/admin/settings/test/embed",
		Summary:     "Probe pending embedding endpoint settings",
	}, func(ctx context.Context, in *struct{ Body probe.EmbedConfig }) (*adminSettingsProbeOutput, error) {
		caller, err := requireAdmin(ctx, admins)
		if err != nil {
			return nil, err
		}
		result := probe.Embed(ctx, in.Body)
		logProbe(logger, caller, "embed", in.Body.Endpoint, result)
		return &adminSettingsProbeOutput{Body: result}, nil
	})
}

func logProbe(logger *slog.Logger, caller owners.Principal, section, endpoint string, result probe.Result) {
	logger.Info("admin settings probe",
		"principal_hub", caller.Hub,
		"principal_user_id", caller.UserID,
		"section", section,
		"endpoint", endpoint,
		"classification", string(result.Classification),
		"latency_ms", result.LatencyMS,
	)
}

func translateAdminSettingsError(err error) error {
	var validation appsettingssvc.ValidationError
	switch {
	case errors.As(err, &validation):
		return &huma.ErrorModel{
			Title:  http.StatusText(http.StatusBadRequest),
			Status: http.StatusBadRequest,
			Detail: "validation_failed",
			Errors: []*huma.ErrorDetail{{
				Message:  validation.Detail,
				Location: validation.Field,
			}},
		}
	case errors.Is(err, appsettingssvc.ErrKeyNotEditable):
		return huma.Error409Conflict("key_not_editable")
	case errors.Is(err, appsettingssvc.ErrReloadFailed):
		return huma.Error500InternalServerError("reload_failed")
	default:
		return Translate(err)
	}
}
