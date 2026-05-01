package testutil_test

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/wesm/fotobank/internal/testutil"
)

// TestOpenTestDB_VecRegistered proves that vec_version() resolves
// inside any test that has called OpenTestDB. The search-side tests
// added in plan 2 will rely on this — failing it now means the
// search-plan tests would fail later for a confusing reason.
func TestOpenTestDB_VecRegistered(t *testing.T) {
	d := testutil.OpenTestDB(t)
	var v string
	require.NoError(t, d.ReadDB().QueryRow(`SELECT vec_version()`).Scan(&v))
	require.NotEmpty(t, v)
}
