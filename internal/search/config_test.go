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
