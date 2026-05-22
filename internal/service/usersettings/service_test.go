package usersettings_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"go.kenn.io/fotobank/internal/owners"
	"go.kenn.io/fotobank/internal/service/usersettings"
	"go.kenn.io/fotobank/internal/testutil"
)

func TestServiceScopesByCaller(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	svc := usersettings.NewService(usersettings.NewRepo(d.WriteDB(), d.ReadDB()))
	alice := owners.Principal{Hub: "local", UserID: "alice"}
	bob := owners.Principal{Hub: "local", UserID: "bob"}

	r.NoError(svc.Set(context.Background(), alice, "theme", `"dark"`))
	r.NoError(svc.Set(context.Background(), bob, "theme", `"light"`))

	a, _, _ := svc.Get(context.Background(), alice, "theme")
	b, _, _ := svc.Get(context.Background(), bob, "theme")
	r.JSONEq(`"dark"`, a)
	r.JSONEq(`"light"`, b)
}

// TestAIInspectionEnabled verifies the typed boolean accessor on the
// ai.inspection key. Default-false on a missing row, true only on a
// canonical "true" payload, false for any other JSON shape — defense
// in depth against a malformed PUT.
func TestAIInspectionEnabled(t *testing.T) {
	r := require.New(t)
	d := testutil.OpenTestDB(t)
	svc := usersettings.NewService(usersettings.NewRepo(d.WriteDB(), d.ReadDB()))
	p := owners.Principal{Hub: "local", UserID: "alice"}
	ctx := context.Background()

	// Missing row → false (and no error).
	on, err := svc.AIInspectionEnabled(ctx, p)
	r.NoError(err)
	r.False(on)

	// "true" → true.
	r.NoError(svc.Set(ctx, p, usersettings.AIInspectionKey, `true`))
	on, err = svc.AIInspectionEnabled(ctx, p)
	r.NoError(err)
	r.True(on)

	// "false" → false.
	r.NoError(svc.Set(ctx, p, usersettings.AIInspectionKey, `false`))
	on, err = svc.AIInspectionEnabled(ctx, p)
	r.NoError(err)
	r.False(on)

	// Anything else → false (fail-closed).
	r.NoError(svc.Set(ctx, p, usersettings.AIInspectionKey, `"yes"`))
	on, err = svc.AIInspectionEnabled(ctx, p)
	r.NoError(err)
	r.False(on)
}
