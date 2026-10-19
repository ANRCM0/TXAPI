package config

import (
	"strings"
	"testing"
)

func TestUsageHistoryDefaultsAndExplicitDisabledBillingGate(t *testing.T) {
	c, err := Load(writeCfg(t, minimalCfg))
	if err != nil || c.UsageHistory.Enabled || c.UsageHistory.InstanceRecords != 4096 || c.UsageHistory.OwnerRecords != 256 || c.UsageHistory.RetentionSeconds != 604800 {
		t.Fatal("unbounded or enabled default", c, err)
	}
	for _, mode := range []string{"legacy", "strict", "disabled"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv(EnvBillingMode, mode)
			_, err := Load(writeCfg(t, minimalCfg+"usage_history:\n  enabled: true\n"))
			if (err == nil) != (mode == "disabled") || err != nil && !strings.Contains(err.Error(), "usage_history.enabled") {
				t.Fatal("recording gate accepted a charged mode", mode, err)
			}
		})
	}
}

func TestUsageHistoryConfigurationHasFiniteRowAndRetentionBounds(t *testing.T) {
	for _, fields := range []string{
		"instance_records: -1", "instance_records: 65537", "owner_records: -1",
		"instance_records: 2\n  owner_records: 3", "retention_seconds: -1", "retention_seconds: 2592001",
	} {
		if _, err := Load(writeCfg(t, minimalCfg+"usage_history:\n  "+fields+"\n")); err == nil {
			t.Fatal("invalid bounded metadata configuration accepted", fields)
		}
	}
}
