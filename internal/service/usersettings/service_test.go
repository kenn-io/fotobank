package usersettings_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wesm/fotobank/internal/owners"
	"github.com/wesm/fotobank/internal/service/usersettings"
	"github.com/wesm/fotobank/internal/testutil"
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
