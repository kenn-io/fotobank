package appsettings

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/wesm/fotobank/internal/ai"
	"github.com/wesm/fotobank/internal/ai/embedding"
	airuntime "github.com/wesm/fotobank/internal/ai/runtime"
	store "github.com/wesm/fotobank/internal/appsettings"
	"github.com/wesm/fotobank/internal/owners"
)

var (
	// ErrValidationFailed is returned when a proposed value or merged AI
	// config is structurally invalid.
	ErrValidationFailed = errors.New("app settings: validation failed")
	// ErrReloadFailed is returned after a successful commit when the
	// provider cannot publish the new file+DB merge. The persisted
	// overrides remain in place for a later Reload.
	ErrReloadFailed = errors.New("app settings: reload failed")
)

// Provider is the runtime snapshot surface required by Service.
type Provider interface {
	Effective() airuntime.Snapshot
	Reload(context.Context) error
}

// Service owns admin-facing runtime settings semantics.
type Service struct {
	rw       *sql.DB
	provider Provider
	gens     *embedding.Generations
}

// NewService constructs a Service over the single-writer DB pool, the
// effective-config provider, and the embedding generation registry.
func NewService(rw *sql.DB, provider Provider, gens *embedding.Generations) *Service {
	return &Service{rw: rw, provider: provider, gens: gens}
}

type PrincipalRef struct {
	Hub    string `json:"hub"`
	UserID string `json:"user_id"`
}

type OverrideMetadata struct {
	Value     any           `json:"value"`
	UpdatedAt time.Time     `json:"updated_at"`
	UpdatedBy *PrincipalRef `json:"updated_by"`
}

type EmbedGeneration struct {
	ID           int64  `json:"id"`
	State        string `json:"state"`
	Model        string `json:"model"`
	Dimension    int    `json:"dimension"`
	InputProfile string `json:"input_profile"`
}

type EffectiveResponse struct {
	Effective              map[string]any                  `json:"effective"`
	FileDefault            map[string]any                  `json:"file_default"`
	Overrides              map[string]OverrideMetadata     `json:"overrides"`
	APIKeyEnvStatus        map[string]APIKeyEnvStatusValue `json:"api_key_env_status"`
	CurrentEmbedGeneration *EmbedGeneration                `json:"current_embed_generation"`
}

type ApplyResponse struct {
	Effective    map[string]any `json:"effective"`
	GenerationID *int64         `json:"generation_id,omitempty"`
}

// ValidationError carries a UI-addressable field for validation
// failures while still wrapping ErrValidationFailed for transport
// mapping.
type ValidationError struct {
	Field  string
	Detail string
}

func (e ValidationError) Error() string {
	if e.Field == "" {
		return e.Detail
	}
	return e.Field + ": " + e.Detail
}

func (e ValidationError) Unwrap() error { return ErrValidationFailed }

// Effective returns all v1 editable keys with their effective values,
// file defaults, override metadata, API key env status, and current
// embedding generation status.
func (s *Service) Effective(ctx context.Context) (EffectiveResponse, error) {
	snap := s.provider.Effective()
	resp := EffectiveResponse{
		Effective:       valuesForKeys(snap.Config, EditableKeys()),
		FileDefault:     valuesForKeys(snap.FileDefault, EditableKeys()),
		Overrides:       overridesForRows(snap.Overrides),
		APIKeyEnvStatus: apiKeyStatuses(snap.Config),
	}
	if s.gens != nil {
		gen, err := s.currentEmbedGeneration(ctx)
		if err != nil {
			return EffectiveResponse{}, err
		}
		resp.CurrentEmbedGeneration = gen
	}
	return resp, nil
}

// ApplySection validates and persists section values in one transaction.
// Embed Apply also creates/fetches the building generation inside that
// same transaction. Provider Reload runs only after commit.
func (s *Service) ApplySection(ctx context.Context, caller owners.Principal, section Section, values map[string]any) (ApplyResponse, error) {
	if _, ok := sectionKeys[section]; !ok {
		return ApplyResponse{}, ValidationError{Field: "section", Detail: "unknown section"}
	}
	snap := s.provider.Effective()
	proposed := snap.Config
	encoded := map[string]string{}
	for key, value := range values {
		if err := validateSectionKey(section, key); err != nil {
			return ApplyResponse{}, err
		}
		raw, decoded, err := encodeSettingValue(key, value)
		if err != nil {
			return ApplyResponse{}, err
		}
		if err := setAIValue(&proposed, key, decoded); err != nil {
			return ApplyResponse{}, err
		}
		encoded[key] = string(raw)
	}
	if err := validateConfig(proposed); err != nil {
		return ApplyResponse{}, err
	}

	var generationID *int64
	err := s.runTx(ctx, func(tx *sql.Tx) error {
		for key, raw := range encoded {
			if err := upsertTx(ctx, tx, key, raw, caller); err != nil {
				return err
			}
		}
		if section == SectionEmbed && s.gens != nil && proposed.Embed.Enabled {
			row, err := s.gens.FindOrCreateBuildingTx(ctx, tx, embedding.Fingerprint(proposed.Embed), proposed.Embed.Dimension)
			if err != nil {
				return err
			}
			generationID = &row.ID
		}
		return nil
	})
	if err != nil {
		return ApplyResponse{}, err
	}
	if err := s.provider.Reload(ctx); err != nil {
		return ApplyResponse{}, fmt.Errorf("%w: %v", ErrReloadFailed, err)
	}
	snap = s.provider.Effective()
	return ApplyResponse{Effective: valuesForKeys(snap.Config, KeysForSection(section)), GenerationID: generationID}, nil
}

// ResetKey deletes one override and applies the effective-without-
// override value through the same validation/generation path as Apply.
func (s *Service) ResetKey(ctx context.Context, caller owners.Principal, key string) (ApplyResponse, error) {
	section, ok := SectionForKey(key)
	if !ok {
		return ApplyResponse{}, fmt.Errorf("%w: %s", ErrKeyNotEditable, key)
	}
	snap := s.provider.Effective()
	proposed := snap.Config
	if err := setAIValue(&proposed, key, valueForKey(snap.FileDefault, key)); err != nil {
		return ApplyResponse{}, err
	}
	if err := validateConfig(proposed); err != nil {
		return ApplyResponse{}, err
	}
	return s.deleteAndReload(ctx, section, proposed, []string{key})
}

// ResetSection deletes every override for section and applies the
// section's file defaults through the same validation/generation path.
func (s *Service) ResetSection(ctx context.Context, caller owners.Principal, section Section) (ApplyResponse, error) {
	keys := KeysForSection(section)
	if len(keys) == 0 {
		return ApplyResponse{}, ValidationError{Field: "section", Detail: "unknown section"}
	}
	snap := s.provider.Effective()
	proposed := snap.Config
	for _, key := range keys {
		if err := setAIValue(&proposed, key, valueForKey(snap.FileDefault, key)); err != nil {
			return ApplyResponse{}, err
		}
	}
	if err := validateConfig(proposed); err != nil {
		return ApplyResponse{}, err
	}
	return s.deleteAndReload(ctx, section, proposed, keys)
}

func (s *Service) deleteAndReload(ctx context.Context, section Section, proposed ai.Config, keys []string) (ApplyResponse, error) {
	var generationID *int64
	err := s.runTx(ctx, func(tx *sql.Tx) error {
		if err := deleteManyTx(ctx, tx, keys); err != nil {
			return err
		}
		if section == SectionEmbed && s.gens != nil && proposed.Embed.Enabled {
			row, err := s.gens.FindOrCreateBuildingTx(ctx, tx, embedding.Fingerprint(proposed.Embed), proposed.Embed.Dimension)
			if err != nil {
				return err
			}
			generationID = &row.ID
		}
		return nil
	})
	if err != nil {
		return ApplyResponse{}, err
	}
	if err := s.provider.Reload(ctx); err != nil {
		return ApplyResponse{}, fmt.Errorf("%w: %v", ErrReloadFailed, err)
	}
	snap := s.provider.Effective()
	return ApplyResponse{Effective: valuesForKeys(snap.Config, KeysForSection(section)), GenerationID: generationID}, nil
}

func (s *Service) runTx(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := s.rw.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return nil
}

func validateSectionKey(section Section, key string) error {
	actual, ok := SectionForKey(key)
	if !ok {
		return fmt.Errorf("%w: %s", ErrKeyNotEditable, key)
	}
	if actual != section {
		return ValidationError{Field: key, Detail: "does not belong to section " + string(section)}
	}
	return nil
}

func encodeSettingValue(key string, value any) ([]byte, any, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, nil, ValidationError{Field: key, Detail: "must be JSON-encodable"}
	}
	if err := ValidateValue(key, raw); err != nil {
		if errors.Is(err, ErrKeyNotEditable) {
			return nil, nil, err
		}
		return nil, nil, ValidationError{Field: key, Detail: strings.TrimPrefix(err.Error(), "field "+key+" ")}
	}
	decoded, err := decodeSettingValue(key, raw)
	if err != nil {
		return nil, nil, err
	}
	return raw, decoded, nil
}

func decodeSettingValue(key string, raw []byte) (any, error) {
	switch key {
	case KeyAIEnabled, KeyTagEnabled, KeyCaptionEnabled, KeyEmbedEnabled:
		var v bool
		if err := json.Unmarshal(raw, &v); err != nil {
			return nil, ValidationError{Field: key, Detail: "must be boolean"}
		}
		return v, nil
	case KeyVisionEndpoint, KeyVisionAPIKeyEnv, KeyTagModel,
		KeyCaptionModel, KeyEmbedEndpoint, KeyEmbedAPIKeyEnv, KeyEmbedModel:
		var v string
		if err := json.Unmarshal(raw, &v); err != nil {
			return nil, ValidationError{Field: key, Detail: "must be string"}
		}
		return v, nil
	case KeyEmbedDimension, KeyEmbedInputEdge:
		var v int
		if err := json.Unmarshal(raw, &v); err != nil {
			return nil, ValidationError{Field: key, Detail: "must be integer"}
		}
		return v, nil
	default:
		return nil, fmt.Errorf("%w: %s", ErrKeyNotEditable, key)
	}
}

func validateConfig(cfg ai.Config) error {
	if err := cfg.Validate(); err != nil {
		field := ""
		detail := err.Error()
		if before, after, ok := strings.Cut(detail, ": "); ok {
			field = before
			detail = after
		}
		return ValidationError{Field: field, Detail: detail}
	}
	return nil
}

func upsertTx(ctx context.Context, tx *sql.Tx, key, value string, by owners.Principal) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO app_settings (key, value, updated_at, updated_by_hub, updated_by_user_id)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (key) DO UPDATE SET
			value = excluded.value,
			updated_at = excluded.updated_at,
			updated_by_hub = excluded.updated_by_hub,
			updated_by_user_id = excluded.updated_by_user_id
	`, key, value, time.Now().UTC(), by.Hub, by.UserID)
	if err != nil {
		return fmt.Errorf("upsert app setting %s: %w", key, err)
	}
	return nil
}

func deleteManyTx(ctx context.Context, tx *sql.Tx, keys []string) error {
	if len(keys) == 0 {
		return nil
	}
	placeholders := make([]string, len(keys))
	args := make([]any, len(keys))
	for i, key := range keys {
		placeholders[i] = "?"
		args[i] = key
	}
	_, err := tx.ExecContext(ctx, `DELETE FROM app_settings WHERE key IN (`+strings.Join(placeholders, ",")+`)`, args...)
	if err != nil {
		return fmt.Errorf("delete app settings: %w", err)
	}
	return nil
}

func overridesForRows(rows map[string]store.Row) map[string]OverrideMetadata {
	out := make(map[string]OverrideMetadata, len(rows))
	for key, row := range rows {
		var value any
		if err := json.Unmarshal([]byte(row.Value), &value); err != nil {
			value = row.Value
		}
		var by *PrincipalRef
		if row.UpdatedByHub != nil && row.UpdatedByUserID != nil {
			by = &PrincipalRef{Hub: *row.UpdatedByHub, UserID: *row.UpdatedByUserID}
		}
		out[key] = OverrideMetadata{Value: normalizeJSONNumber(value), UpdatedAt: row.UpdatedAt, UpdatedBy: by}
	}
	return out
}

func apiKeyStatuses(cfg ai.Config) map[string]APIKeyEnvStatusValue {
	return map[string]APIKeyEnvStatusValue{
		KeyVisionAPIKeyEnv: APIKeyEnvStatus(cfg.Vision.APIKeyEnv),
		KeyEmbedAPIKeyEnv:  APIKeyEnvStatus(cfg.Embed.APIKeyEnv),
	}
}

func (s *Service) currentEmbedGeneration(ctx context.Context) (*EmbedGeneration, error) {
	if building, err := s.gens.FindBuilding(ctx); err != nil {
		return nil, err
	} else if building != nil {
		return embedGenerationFromRow(*building), nil
	}
	active, err := s.gens.FindActive(ctx)
	if err != nil || active == nil {
		return nil, err
	}
	return embedGenerationFromRow(*active), nil
}

func embedGenerationFromRow(row embedding.Row) *EmbedGeneration {
	return &EmbedGeneration{
		ID:           row.ID,
		State:        row.State,
		Model:        row.ModelID,
		Dimension:    row.Dimension,
		InputProfile: row.InputProfile,
	}
}

func valuesForKeys(cfg ai.Config, keys []string) map[string]any {
	out := make(map[string]any, len(keys))
	for _, key := range keys {
		out[key] = valueForKey(cfg, key)
	}
	return out
}

func valueForKey(cfg ai.Config, key string) any {
	switch key {
	case KeyAIEnabled:
		return cfg.Enabled
	case KeyVisionEndpoint:
		return cfg.Vision.Endpoint
	case KeyVisionAPIKeyEnv:
		return cfg.Vision.APIKeyEnv
	case KeyTagEnabled:
		return cfg.Tag.Enabled
	case KeyTagModel:
		return cfg.Tag.Model
	case KeyCaptionEnabled:
		return cfg.Caption.Enabled
	case KeyCaptionModel:
		return cfg.Caption.Model
	case KeyEmbedEnabled:
		return cfg.Embed.Enabled
	case KeyEmbedEndpoint:
		return cfg.Embed.Endpoint
	case KeyEmbedAPIKeyEnv:
		return cfg.Embed.APIKeyEnv
	case KeyEmbedModel:
		return cfg.Embed.Model
	case KeyEmbedDimension:
		return cfg.Embed.Dimension
	case KeyEmbedInputEdge:
		return cfg.Embed.InputEdge
	default:
		return nil
	}
}

func setAIValue(cfg *ai.Config, key string, value any) error {
	switch key {
	case KeyAIEnabled:
		v, ok := value.(bool)
		if !ok {
			return ValidationError{Field: key, Detail: "must be boolean"}
		}
		cfg.Enabled = v
	case KeyVisionEndpoint:
		v, ok := value.(string)
		if !ok {
			return ValidationError{Field: key, Detail: "must be string"}
		}
		cfg.Vision.Endpoint = v
	case KeyVisionAPIKeyEnv:
		v, ok := value.(string)
		if !ok {
			return ValidationError{Field: key, Detail: "must be string"}
		}
		cfg.Vision.APIKeyEnv = v
	case KeyTagEnabled:
		v, ok := value.(bool)
		if !ok {
			return ValidationError{Field: key, Detail: "must be boolean"}
		}
		cfg.Tag.Enabled = v
	case KeyTagModel:
		v, ok := value.(string)
		if !ok {
			return ValidationError{Field: key, Detail: "must be string"}
		}
		cfg.Tag.Model = v
	case KeyCaptionEnabled:
		v, ok := value.(bool)
		if !ok {
			return ValidationError{Field: key, Detail: "must be boolean"}
		}
		cfg.Caption.Enabled = v
	case KeyCaptionModel:
		v, ok := value.(string)
		if !ok {
			return ValidationError{Field: key, Detail: "must be string"}
		}
		cfg.Caption.Model = v
	case KeyEmbedEnabled:
		v, ok := value.(bool)
		if !ok {
			return ValidationError{Field: key, Detail: "must be boolean"}
		}
		cfg.Embed.Enabled = v
	case KeyEmbedEndpoint:
		v, ok := value.(string)
		if !ok {
			return ValidationError{Field: key, Detail: "must be string"}
		}
		cfg.Embed.Endpoint = v
	case KeyEmbedAPIKeyEnv:
		v, ok := value.(string)
		if !ok {
			return ValidationError{Field: key, Detail: "must be string"}
		}
		cfg.Embed.APIKeyEnv = v
	case KeyEmbedModel:
		v, ok := value.(string)
		if !ok {
			return ValidationError{Field: key, Detail: "must be string"}
		}
		cfg.Embed.Model = v
	case KeyEmbedDimension:
		v, ok := intValue(value)
		if !ok {
			return ValidationError{Field: key, Detail: "must be integer"}
		}
		cfg.Embed.Dimension = v
	case KeyEmbedInputEdge:
		v, ok := intValue(value)
		if !ok {
			return ValidationError{Field: key, Detail: "must be integer"}
		}
		cfg.Embed.InputEdge = v
	default:
		return fmt.Errorf("%w: %s", ErrKeyNotEditable, key)
	}
	return nil
}

func intValue(value any) (int, bool) {
	switch v := value.(type) {
	case int:
		return v, true
	case int64:
		return int(v), true
	case float64:
		if v != float64(int(v)) {
			return 0, false
		}
		return int(v), true
	case json.Number:
		n, err := v.Int64()
		return int(n), err == nil
	default:
		return 0, false
	}
}

func normalizeJSONNumber(value any) any {
	if n, ok := value.(float64); ok && n == float64(int(n)) {
		return int(n)
	}
	return value
}
