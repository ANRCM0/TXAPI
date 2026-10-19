package config

import "fmt"

type UsageHistoryConfig struct {
	Enabled          bool `yaml:"enabled"`
	InstanceRecords  int  `yaml:"instance_records"`
	OwnerRecords     int  `yaml:"owner_records"`
	RetentionSeconds int  `yaml:"retention_seconds"`
}

func (c *UsageHistoryConfig) defaults() {
	if c.InstanceRecords == 0 {
		c.InstanceRecords = 4096
	}
	if c.OwnerRecords == 0 {
		c.OwnerRecords = 256
	}
	if c.RetentionSeconds == 0 {
		c.RetentionSeconds = 604800
	}
}

func (c UsageHistoryConfig) validate(mode string) error {
	if c.InstanceRecords < 1 || c.InstanceRecords > 65536 || c.OwnerRecords < 1 || c.OwnerRecords > c.InstanceRecords || c.RetentionSeconds < 1 || c.RetentionSeconds > 2592000 {
		return fmt.Errorf("usage_history requires 1..65536 instance records, owner records within that limit and 1..2592000 retention seconds")
	}
	if c.Enabled && mode != "disabled" {
		return fmt.Errorf("usage_history.enabled requires billing.mode disabled")
	}
	return nil
}
