package config

import (
	"strings"
	"testing"
)

func TestManagedInferenceRequiresExplicitSafeDependencies(t *testing.T) {
	base, err := Load(writeCfg(t, minimalCfg))
	if err != nil || base.UpstreamAccounts.Inference.Enabled {
		t.Fatalf("default capability: %+v %v", base, err)
	}
	for name, body := range map[string]string{
		"legacy":        "billing:\n  mode: legacy\ndatabase:\n  credential_keys_file: fixture-keys.json\nresponses:\n  enabled: true\n  keys_file: fixture-content.json\n",
		"strict":        "billing:\n  mode: strict\ndatabase:\n  credential_keys_file: fixture-keys.json\nresponses:\n  enabled: true\n  keys_file: fixture-content.json\n",
		"plaintext":     "billing:\n  mode: disabled\nresponses:\n  enabled: true\n  keys_file: fixture-content.json\n",
		"responses off": "billing:\n  mode: disabled\ndatabase:\n  credential_keys_file: fixture-keys.json\n",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Load(writeCfg(t, minimalCfg+body+"upstream_accounts:\n  inference:\n    enabled: true\n"))
			if err == nil || !strings.Contains(err.Error(), "upstream_accounts.inference.enabled") {
				t.Fatalf("unsafe enable: %v", err)
			}
		})
	}
	_, err = Load(writeCfg(t, minimalCfg+"billing:\n  mode: disabled\ndatabase:\n  credential_keys_file: fixture-keys.json\nresponses:\n  enabled: true\n  keys_file: fixture-content.json\nupstream_accounts:\n  inference:\n    enabled: true\n"))
	if err != nil {
		t.Fatal(err)
	}
}

func TestUpstreamRefreshDefaultsBoundsAndEncryption(t *testing.T) {
	cfg, err := Load(writeCfg(t, minimalCfg))
	if err != nil || cfg.UpstreamAccounts.Refresh.Enabled || cfg.UpstreamAccounts.Refresh.IntervalSeconds != 30 || cfg.UpstreamAccounts.Refresh.BeforeExpirySeconds != 600 || cfg.UpstreamAccounts.Refresh.MaxAttemptsPerCycle != 4 {
		t.Fatalf("refresh default configuration: %v", err)
	}
	for _, fields := range []string{
		"enabled: true", "interval_seconds: -1", "interval_seconds: 3601", "before_expiry_seconds: 29", "before_expiry_seconds: 86401", "max_attempts_per_cycle: -1", "max_attempts_per_cycle: 17", "interval_seconds: 3600\n    before_expiry_seconds: 600",
	} {
		_, err := Load(writeCfg(t, minimalCfg+"upstream_accounts:\n  refresh:\n    "+fields+"\n"))
		if err == nil || !strings.Contains(err.Error(), "upstream_accounts.refresh") {
			t.Fatalf("accepted invalid refresh configuration %q: %v", fields, err)
		}
	}
	_, err = Load(writeCfg(t, minimalCfg+"database:\n  credential_keys_file: fixture-keys.json\nupstream_accounts:\n  refresh:\n    enabled: true\n"))
	if err != nil {
		t.Fatal("refresh lifecycle should not require enabling inference", err)
	}
}
