package search_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wesm/fotobank/internal/search"
)

func TestConfig_DefaultsApply(t *testing.T) {
	r := require.New(t)
	c := &search.Config{}
	c.ApplyDefaults()
	r.Equal(200, c.KPerSignal)
	r.Equal(60, c.RRFK)
	r.Equal(30, c.RetainRetiredDays)
	r.Equal(95, c.ActivationThresholdPct)
}

// TestConfig_ValidateAcceptsDefaults confirms a default-applied config
// passes validation; this is a guard against bumping a default outside
// the validator's accepted range without updating both together.
func TestConfig_ValidateAcceptsDefaults(t *testing.T) {
	r := require.New(t)
	c := &search.Config{}
	c.ApplyDefaults()
	r.NoError(c.Validate())
}

// TestConfig_ValidateRejectsBadValues covers the boundary conditions for
// each [search] tunable. Each subtest sets a single field to an invalid
// value and asserts the resulting error mentions the field name so the
// operator can fix the offending key.
func TestConfig_ValidateRejectsBadValues(t *testing.T) {
	cases := []struct {
		name string
		set  func(*search.Config)
		want string
	}{
		{"k_per_signal=0", func(c *search.Config) { c.KPerSignal = 0 }, "search.k_per_signal"},
		{"k_per_signal=10001", func(c *search.Config) { c.KPerSignal = 10001 }, "search.k_per_signal"},
		{"rrf_k=0", func(c *search.Config) { c.RRFK = 0 }, "search.rrf_k"},
		{"retain_retired_days=-1", func(c *search.Config) { c.RetainRetiredDays = -1 }, "search.retain_retired_days"},
		{"activation_threshold=0", func(c *search.Config) { c.ActivationThresholdPct = 0 }, "search.activation_threshold"},
		{"activation_threshold=101", func(c *search.Config) { c.ActivationThresholdPct = 101 }, "search.activation_threshold"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := require.New(t)
			c := &search.Config{}
			c.ApplyDefaults()
			tc.set(c)
			err := c.Validate()
			r.Error(err)
			r.Contains(err.Error(), tc.want)
		})
	}
}
