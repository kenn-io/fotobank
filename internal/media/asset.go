package media

import (
	"fmt"
	"time"

	"go.kenn.io/fotobank/internal/errs"
	"go.kenn.io/fotobank/internal/owners"
)

type AssetState string

const (
	AssetPending  AssetState = "pending"
	AssetReady    AssetState = "ready"
	AssetConflict AssetState = "conflict"
)

type FileRole string

const (
	RolePrimary   FileRole = "primary"
	RoleOriginal  FileRole = "original"
	RoleSidecar   FileRole = "sidecar"
	RoleAlternate FileRole = "alternate"
)

type RelationshipKind string

const (
	SidecarOf   RelationshipKind = "sidecar_of"
	DerivedFrom RelationshipKind = "derived_from"
	PairedWith  RelationshipKind = "paired_with"
)

type Asset struct {
	ID         string
	Owner      owners.Principal
	State      AssetState
	Type       Type
	ImportedAt time.Time
	Timestamp  *time.Time

	Make           string
	Model          string
	LensModel      string
	FocalLength    string
	Shutter        string
	Width          *int
	Height         *int
	ISO            *int
	Aperture       *float64
	DurationMs     *int64
	Latitude       *float64
	Longitude      *float64
	GPSAt          *time.Time
	LocationLabel  string
	ThumbStatus    string
	ThumbVersion   int
	ThumbUpdatedAt *time.Time
	HiddenAt       *time.Time
}

type File struct {
	ID                 string
	AssetID            string
	Owner              owners.Principal
	Role               FileRole
	MimeType           string
	OriginalFilename   string
	ImportSourcePath   string
	Size               int64
	DocbankNodeID      *int64
	DocbankVirtualPath string
	CurrentVersionID   string
	SHA256             string
}

type FileRelationship struct {
	SourceFileID string
	TargetFileID string
	Kind         RelationshipKind
}

func ValidateAssetState(state AssetState) error {
	switch state {
	case AssetPending, AssetReady, AssetConflict:
		return nil
	default:
		return fmt.Errorf("%w: invalid asset state %q", errs.ErrInvalidArgument, state)
	}
}

func ValidateFileRole(role FileRole) error {
	switch role {
	case RolePrimary, RoleOriginal, RoleSidecar, RoleAlternate:
		return nil
	default:
		return fmt.Errorf("%w: invalid file role %q", errs.ErrInvalidArgument, role)
	}
}

func ValidateRelationshipKind(kind RelationshipKind) error {
	switch kind {
	case SidecarOf, DerivedFrom, PairedWith:
		return nil
	default:
		return fmt.Errorf("%w: invalid relationship kind %q", errs.ErrInvalidArgument, kind)
	}
}
