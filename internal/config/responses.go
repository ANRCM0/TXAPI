package config

import (
	"fmt"
	"strings"
)

const EnvResponsesKeysFile = "GATEWAY_RESPONSES_KEYS_FILE"

// ResponsesConfig bounds foreground idempotency, not sessions or background jobs.
type ResponsesConfig struct {
	Enabled          bool   `yaml:"enabled"`
	KeysFile         string `yaml:"keys_file"`
	RetentionSeconds int    `yaml:"retention_seconds"`
	ReplaySeconds    int    `yaml:"replay_seconds"`
	MaxOutputBytes   int64  `yaml:"max_output_bytes"`
	InstanceRecords  int64  `yaml:"instance_records"`
	OwnerRecords     int64  `yaml:"owner_records"`
	InstanceBytes    int64  `yaml:"instance_bytes"`
	OwnerBytes       int64  `yaml:"owner_bytes"`
	ActiveRequests   int    `yaml:"active_requests"`
	OwnerRequests    int    `yaml:"owner_requests"`
}

func (c *ResponsesConfig) defaults() {
	if c.RetentionSeconds == 0 {
		c.RetentionSeconds = 86400
	}
	if c.ReplaySeconds == 0 {
		c.ReplaySeconds = 3600
	}
	if c.MaxOutputBytes == 0 {
		c.MaxOutputBytes = 256 << 10
	}
	if c.InstanceRecords == 0 {
		c.InstanceRecords = 1024
	}
	if c.OwnerRecords == 0 {
		c.OwnerRecords = 128
	}
	if c.InstanceBytes == 0 {
		c.InstanceBytes = 16 << 20
	}
	if c.OwnerBytes == 0 {
		c.OwnerBytes = 4 << 20
	}
	if c.ActiveRequests == 0 {
		c.ActiveRequests = 32
	}
	if c.OwnerRequests == 0 {
		c.OwnerRequests = 4
	}
}

func (c ResponsesConfig) validate() error {
	if !c.Enabled {
		return nil
	}
	if strings.TrimSpace(c.KeysFile) == "" {
		return fmt.Errorf("responses.enabled requires responses.keys_file or %s", EnvResponsesKeysFile)
	}
	if c.RetentionSeconds < 60 || c.RetentionSeconds > 7*86400 || c.ReplaySeconds < 60 || c.ReplaySeconds > c.RetentionSeconds ||
		c.MaxOutputBytes < 16384 || c.MaxOutputBytes > 8<<20 || c.InstanceRecords < 2 || c.InstanceRecords > 65536 || c.OwnerRecords < 1 || c.OwnerRecords >= c.InstanceRecords ||
		c.OwnerBytes < c.MaxOutputBytes || c.OwnerBytes < 68<<10 || c.InstanceBytes < c.OwnerBytes || c.InstanceBytes > 64<<20 ||
		c.ActiveRequests < 2 || c.ActiveRequests > 128 || c.OwnerRequests < 1 || c.OwnerRequests >= c.ActiveRequests {
		return fmt.Errorf("responses limits or retention are outside supported bounds")
	}
	return nil
}
