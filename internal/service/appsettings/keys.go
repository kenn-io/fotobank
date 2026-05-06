// Package appsettings contains the typed service layer for server-global
// runtime settings.
package appsettings

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
)

// ErrKeyNotEditable is returned when an admin settings request targets
// a key outside the v1 runtime allowlist.
var ErrKeyNotEditable = errors.New("app settings: key not editable")

const (
	KeyAIEnabled       = "ai.enabled"
	KeyVisionEndpoint  = "ai.vision.endpoint"
	KeyVisionAPIKeyEnv = "ai.vision.api_key_env"
	KeyTagEnabled      = "ai.tag.enabled"
	KeyTagModel        = "ai.tag.model"
	KeyCaptionEnabled  = "ai.caption.enabled"
	KeyCaptionModel    = "ai.caption.model"
	KeyEmbedEnabled    = "ai.embed.enabled"
	KeyEmbedEndpoint   = "ai.embed.endpoint"
	KeyEmbedAPIKeyEnv  = "ai.embed.api_key_env"
	KeyEmbedModel      = "ai.embed.model"
	KeyEmbedDimension  = "ai.embed.dimension"
	KeyEmbedInputEdge  = "ai.embed.input_edge"
)

// Section is the admin UI/apply grouping for editable keys.
type Section string

const (
	SectionMaster  Section = "master"
	SectionVision  Section = "vision"
	SectionTag     Section = "tag"
	SectionCaption Section = "caption"
	SectionEmbed   Section = "embed"
)

var sectionKeys = map[Section][]string{
	SectionMaster:  {KeyAIEnabled},
	SectionVision:  {KeyVisionEndpoint, KeyVisionAPIKeyEnv},
	SectionTag:     {KeyTagEnabled, KeyTagModel},
	SectionCaption: {KeyCaptionEnabled, KeyCaptionModel},
	SectionEmbed: {
		KeyEmbedEnabled, KeyEmbedEndpoint, KeyEmbedAPIKeyEnv,
		KeyEmbedModel, KeyEmbedDimension, KeyEmbedInputEdge,
	},
}

var editable = func() map[string]Section {
	out := map[string]Section{}
	for section, keys := range sectionKeys {
		for _, key := range keys {
			out[key] = section
		}
	}
	return out
}()

// EditableKeys returns the v1 allowlisted keys in stable UI order.
func EditableKeys() []string {
	out := make([]string, 0, len(editable))
	for _, section := range []Section{SectionMaster, SectionVision, SectionTag, SectionCaption, SectionEmbed} {
		out = append(out, sectionKeys[section]...)
	}
	return out
}

// KeysForSection returns a copy of section's editable keys.
func KeysForSection(section Section) []string {
	keys := sectionKeys[section]
	out := make([]string, len(keys))
	copy(out, keys)
	return out
}

// SectionForKey returns the section that owns key.
func SectionForKey(key string) (Section, bool) {
	section, ok := editable[key]
	return section, ok
}

// Editable reports whether key is in the v1 allowlist.
func Editable(key string) bool {
	_, ok := editable[key]
	return ok
}

// APIKeyEnvStatus is the UI-safe status for an api_key_env setting.
type APIKeyEnvStatusValue struct {
	Name     string `json:"name"`
	IsSet    bool   `json:"is_set"`
	Required bool   `json:"required"`
}

// APIKeyEnvStatus returns whether env var name is set. Empty name means
// no auth is required.
func APIKeyEnvStatus(name string) APIKeyEnvStatusValue {
	if name == "" {
		return APIKeyEnvStatusValue{Name: "", IsSet: false, Required: false}
	}
	return APIKeyEnvStatusValue{Name: name, IsSet: os.Getenv(name) != "", Required: true}
}

// ValidateValue verifies raw is valid JSON of the expected type for key.
func ValidateValue(key string, raw json.RawMessage) error {
	if !Editable(key) {
		return fmt.Errorf("%w: %s", ErrKeyNotEditable, key)
	}
	switch key {
	case KeyAIEnabled, KeyTagEnabled, KeyCaptionEnabled, KeyEmbedEnabled:
		var v bool
		if err := json.Unmarshal(raw, &v); err != nil {
			return fmt.Errorf("field %s must be boolean", key)
		}
	case KeyVisionEndpoint, KeyVisionAPIKeyEnv, KeyTagModel,
		KeyCaptionModel, KeyEmbedEndpoint, KeyEmbedAPIKeyEnv, KeyEmbedModel:
		var v string
		if err := json.Unmarshal(raw, &v); err != nil {
			return fmt.Errorf("field %s must be string", key)
		}
	case KeyEmbedDimension, KeyEmbedInputEdge:
		var v int
		if err := json.Unmarshal(raw, &v); err != nil {
			return fmt.Errorf("field %s must be integer", key)
		}
		if v <= 0 {
			return fmt.Errorf("field %s must be > 0", key)
		}
	default:
		return fmt.Errorf("%w: %s", ErrKeyNotEditable, key)
	}
	return nil
}
