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
