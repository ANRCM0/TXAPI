package config

import "testing"

func TestResponsesDefaultsOptInAndSecretOverrides(t *testing.T) {
	cfg, err := Load(writeCfg(t, minimalCfg))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Responses.Enabled || cfg.Responses.RetentionSeconds != 86400 || cfg.Responses.ReplaySeconds != 3600 || cfg.Responses.ActiveRequests != 32 {
		t.Fatal("unsafe Responses defaults")
	}
	t.Setenv(EnvResponsesKeysFile, "mounted-responses.json")
	cfg, err = Load(writeCfg(t, minimalCfg))
	if err != nil || cfg.Responses.KeysFile != "mounted-responses.json" {
		t.Fatal("Responses override", err)
	}
}

func TestResponsesRejectsUnsafeLimitsButDoesNotRequireStrict(t *testing.T) {
	valid := ResponsesConfig{Enabled: true, KeysFile: "mounted.json"}
	valid.defaults()
	if err := valid.validate(); err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*ResponsesConfig){
		"keys":         func(c *ResponsesConfig) { c.KeysFile = " " },
		"retention":    func(c *ResponsesConfig) { c.RetentionSeconds = 59 },
		"replay":       func(c *ResponsesConfig) { c.ReplaySeconds = c.RetentionSeconds + 1 },
		"body":         func(c *ResponsesConfig) { c.MaxOutputBytes = 9 << 20 },
		"records":      func(c *ResponsesConfig) { c.OwnerRecords = c.InstanceRecords },
		"bytes":        func(c *ResponsesConfig) { c.OwnerBytes = 1 },
		"active":       func(c *ResponsesConfig) { c.ActiveRequests = 129 },
		"owner active": func(c *ResponsesConfig) { c.OwnerRequests = c.ActiveRequests },
	} {
		t.Run(name, func(t *testing.T) {
			cfg := valid
			change(&cfg)
			if cfg.validate() == nil {
				t.Fatal("unsafe configuration accepted")
			}
		})
	}
	for _, mode := range []string{"disabled", "legacy"} {
		cfg, err := Load(writeCfg(t, minimalCfg+"\nresponses:\n  enabled: true\n  keys_file: mounted.json\nbilling:\n  mode: "+mode+"\ndatabase:\n  credential_keys_file: credentials.json\n"))
		if err != nil || !cfg.Responses.Enabled {
			t.Fatal("foreground Responses unnecessarily depends on strict runtime", err)
		}
	}
	if _, err := Load(writeCfg(t, minimalCfg+"\nserver:\n  total_timeout_seconds: 120\nresponses:\n  enabled: true\n  keys_file: mounted.json\n  replay_seconds: 60\ndatabase:\n  credential_keys_file: credentials.json\n")); err == nil {
		t.Fatal("replay retention expires during a valid generation")
	}
}
