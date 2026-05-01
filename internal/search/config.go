// Package search owns the [search] config block. It is intentionally
// configuration-only at this point; the search engine, transport, and
// service wiring land in later tasks.
package search

import "fmt"

// Config is the [search] TOML block.
type Config struct {
	// KPerSignal caps the number of candidates pulled from each signal
	// (FTS, vector, etc.) before fusion.
	KPerSignal int `toml:"k_per_signal"`
	// RRFK is the reciprocal-rank-fusion smoothing constant.
	RRFK int `toml:"rrf_k"`
	// RetainRetiredDays controls how long retired embedding generations
	// are kept on disk before compaction sweeps them.
	RetainRetiredDays int `toml:"retain_retired_days"`
	// ActivationThresholdPct is the percent of eligible media that must
	// be embedded in the active generation before the activator promotes
	// it from staging to active.
	ActivationThresholdPct int `toml:"activation_threshold"`
}

// ApplyDefaults fills sensible defaults so a missing or partial [search]
// block still produces a usable configuration.
func (c *Config) ApplyDefaults() {
	if c.KPerSignal <= 0 {
		c.KPerSignal = 200
	}
	if c.RRFK <= 0 {
		c.RRFK = 60
	}
	if c.RetainRetiredDays <= 0 {
		c.RetainRetiredDays = 30
	}
	if c.ActivationThresholdPct <= 0 {
		c.ActivationThresholdPct = 95
	}
}

// Validate checks that all values are in sensible ranges. Called by
// the top-level config validator after ApplyDefaults.
func (c *Config) Validate() error {
	if c.KPerSignal <= 0 || c.KPerSignal > 10000 {
		return fmt.Errorf("search.k_per_signal: must be in 1..10000 (got %d)", c.KPerSignal)
	}
	if c.RRFK <= 0 {
		return fmt.Errorf("search.rrf_k: must be > 0 (got %d)", c.RRFK)
	}
	if c.RetainRetiredDays < 0 {
		return fmt.Errorf("search.retain_retired_days: must be >= 0 (got %d)", c.RetainRetiredDays)
	}
	if c.ActivationThresholdPct < 1 || c.ActivationThresholdPct > 100 {
		return fmt.Errorf("search.activation_threshold: must be in 1..100 (got %d)", c.ActivationThresholdPct)
	}
	return nil
}
