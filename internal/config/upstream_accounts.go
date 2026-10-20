package config

import (
	"fmt"
)

type UpstreamAccountsConfig struct {
	Inference ManagedInferenceConfig `yaml:"inference"`
	Refresh   UpstreamRefreshConfig  `yaml:"refresh"`
}

type ManagedInferenceConfig struct {
	Enabled bool `yaml:"enabled"`
}

// Refresh is startup-only and uses the same account claim/refresh owner as CLI.
type UpstreamRefreshConfig struct {
	Enabled             bool `yaml:"enabled"`
	IntervalSeconds     int  `yaml:"interval_seconds"`
	BeforeExpirySeconds int  `yaml:"before_expiry_seconds"`
	MaxAttemptsPerCycle int  `yaml:"max_attempts_per_cycle"`
}

func (c *UpstreamAccountsConfig) defaults() {
	if c.Refresh.IntervalSeconds == 0 {
		c.Refresh.IntervalSeconds = 30
	}
	if c.Refresh.BeforeExpirySeconds == 0 {
		c.Refresh.BeforeExpirySeconds = 600
	}
	if c.Refresh.MaxAttemptsPerCycle == 0 {
		c.Refresh.MaxAttemptsPerCycle = 4
	}
}

func (c UpstreamAccountsConfig) validate(_ string) error {
	if c.Inference.Enabled {
		return fmt.Errorf("upstream_accounts.inference.enabled is unavailable in the API-key relay; configure a static API channel")
	}
	if c.Refresh.Enabled {
		return fmt.Errorf("upstream_accounts.refresh.enabled is unavailable in the API-key relay; OAuth refresh is not supported")
	}
	r := c.Refresh
	if r.IntervalSeconds < 5 || r.IntervalSeconds > 3600 || r.BeforeExpirySeconds < 30 || r.BeforeExpirySeconds > 86400 || r.BeforeExpirySeconds < r.IntervalSeconds || r.MaxAttemptsPerCycle < 1 || r.MaxAttemptsPerCycle > 16 {
		return fmt.Errorf("upstream_accounts.refresh requires interval_seconds 5..3600, before_expiry_seconds 30..86400 and at least interval_seconds, max_attempts_per_cycle 1..16")
	}
	return nil
}
