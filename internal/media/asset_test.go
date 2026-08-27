package media_test

import (
	"testing"

	"github.com/stretchr/testify/require"
	"go.kenn.io/fotobank/internal/errs"
	"go.kenn.io/fotobank/internal/media"
)

func TestAssetEnumValues(t *testing.T) {
	values := []struct {
		name string
		got  string
		want string
	}{
		{name: "state pending", got: string(media.AssetPending), want: "pending"},
		{name: "state ready", got: string(media.AssetReady), want: "ready"},
		{name: "state conflict", got: string(media.AssetConflict), want: "conflict"},
		{name: "role primary", got: string(media.RolePrimary), want: "primary"},
		{name: "role original", got: string(media.RoleOriginal), want: "original"},
		{name: "role sidecar", got: string(media.RoleSidecar), want: "sidecar"},
		{name: "role alternate", got: string(media.RoleAlternate), want: "alternate"},
		{name: "sidecar relationship", got: string(media.SidecarOf), want: "sidecar_of"},
		{name: "derived relationship", got: string(media.DerivedFrom), want: "derived_from"},
		{name: "paired relationship", got: string(media.PairedWith), want: "paired_with"},
	}
	for _, value := range values {
		t.Run(value.name, func(t *testing.T) {
			require.Equal(t, value.want, value.got)
		})
	}
}

func TestAssetEnumValidation(t *testing.T) {
	r := require.New(t)
	for _, state := range []media.AssetState{
		media.AssetPending, media.AssetReady, media.AssetConflict,
	} {
		r.NoError(media.ValidateAssetState(state))
	}
	r.ErrorIs(media.ValidateAssetState(""), errs.ErrInvalidArgument)

	for _, role := range []media.FileRole{
		media.RolePrimary, media.RoleOriginal, media.RoleSidecar, media.RoleAlternate,
	} {
		r.NoError(media.ValidateFileRole(role))
	}
	r.ErrorIs(media.ValidateFileRole(""), errs.ErrInvalidArgument)

	for _, kind := range []media.RelationshipKind{
		media.SidecarOf, media.DerivedFrom, media.PairedWith,
	} {
		r.NoError(media.ValidateRelationshipKind(kind))
	}
	r.ErrorIs(media.ValidateRelationshipKind(""), errs.ErrInvalidArgument)
}
