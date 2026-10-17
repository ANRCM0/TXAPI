package config

import (
	"fmt"
	"strings"
)

const EnvGenerationKeysFile = "GATEWAY_GENERATION_KEYS_FILE"

// GenerationConfig enables the durable runtime, not a public background API.
// Only the secret-file path belongs in app configuration; key bytes never do.
type GenerationConfig struct {
	Enabled          bool   `yaml:"enabled"`
	KeysFile         string `yaml:"keys_file"`
	Workers          int    `yaml:"workers"`
	PollMilliseconds int    `yaml:"poll_milliseconds"`
	LeaseSeconds     int    `yaml:"lease_seconds"`
	DeadlineSeconds  int    `yaml:"deadline_seconds"`
	RetentionSeconds int    `yaml:"retention_seconds"`
	MaxOutputBytes   int64  `yaml:"max_output_bytes"`
	InstanceRecords  int64  `yaml:"instance_records"`
	OwnerRecords     int64  `yaml:"owner_records"`
	InstanceBytes    int64  `yaml:"instance_bytes"`
	OwnerBytes       int64  `yaml:"owner_bytes"`
}

func (c *GenerationConfig) defaults(timeout int) {
	if c.Workers == 0 {
		c.Workers = 2
	}
	if c.PollMilliseconds == 0 {
		c.PollMilliseconds = 250
	}
	if c.LeaseSeconds == 0 {
		c.LeaseSeconds = 10
	}
	if c.DeadlineSeconds == 0 {
		c.DeadlineSeconds = min(timeout, 300)
	}
	if c.RetentionSeconds == 0 {
		c.RetentionSeconds = 24 * 60 * 60
	}
	if c.MaxOutputBytes == 0 {
		c.MaxOutputBytes = 256 << 10
	}
	if c.InstanceRecords == 0 {
		c.InstanceRecords = 256
	}
	if c.OwnerRecords == 0 {
		c.OwnerRecords = 32
	}
	if c.InstanceBytes == 0 {
		c.InstanceBytes = 16 << 20
	}
	if c.OwnerBytes == 0 {
		c.OwnerBytes = 4 << 20
	}
}

func (c GenerationConfig) validate(billing BillingConfig, timeout int) error {
	if !c.Enabled {
		return nil
	}
	if billing.Mode != "strict" || billing.MaxRequestCost == "" || billing.MaxProviderRequestCost == "" || len(billing.ProviderContracts) == 0 {
		return fmt.Errorf("generation.enabled requires strict billing, fee/provider ceilings and provider contracts")
	}
	if strings.TrimSpace(c.KeysFile) == "" {
		return fmt.Errorf("generation.enabled requires generation.keys_file or %s", EnvGenerationKeysFile)
	}
	if c.Workers < 1 || c.Workers > 32 || c.PollMilliseconds < 20 || c.PollMilliseconds > 60000 || c.LeaseSeconds < 1 || c.LeaseSeconds > 60 ||
		c.DeadlineSeconds < 1 || c.DeadlineSeconds > 300 || c.DeadlineSeconds > timeout+5 || c.RetentionSeconds < 60 || c.RetentionSeconds > 7*24*60*60 || c.RetentionSeconds < c.DeadlineSeconds+5 ||
		c.MaxOutputBytes < 16384 || c.MaxOutputBytes > 8<<20 || c.InstanceRecords < 2 || c.InstanceRecords > 4096 || c.OwnerRecords < 1 || c.OwnerRecords >= c.InstanceRecords ||
		c.OwnerBytes < 68<<10 || c.OwnerBytes < c.MaxOutputBytes || c.InstanceBytes < c.OwnerBytes || c.InstanceBytes > 64<<20 {
		return fmt.Errorf("generation limits, deadlines or retention are outside supported bounds")
	}
	return nil
}
