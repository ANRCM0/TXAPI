package config

import (
	"strings"
	"testing"
)

func TestGenerationRuntimeDefaultsAreFiniteAndOptIn(t *testing.T) {
	cfg, err := Load(writeCfg(t, minimalCfg))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Generation.Enabled || cfg.Generation.Workers != 2 || cfg.Generation.LeaseSeconds != 10 || cfg.Generation.RetentionSeconds != 86400 || cfg.Database.CredentialKeysFile != "" {
		t.Fatal("generation defaults changed or enabled plaintext runtime")
	}
	t.Setenv(EnvCredentialKeysFile, "mounted-credential-keys.json")
	t.Setenv(EnvGenerationKeysFile, "mounted-generation-keys.json")
	cfg, err = Load(writeCfg(t, minimalCfg))
	if err != nil || cfg.Database.CredentialKeysFile != "mounted-credential-keys.json" || cfg.Generation.KeysFile != "mounted-generation-keys.json" {
		t.Fatal("secret-file path override not applied", err)
	}
}

func TestGenerationRuntimeRejectsUnboundedOrUnsafeAssemblyConfig(t *testing.T) {
	billing := BillingConfig{Mode: "strict", MaxRequestCost: "1", MaxProviderRequestCost: "1", ProviderContracts: []ProviderContractConfig{{}}}
	valid := GenerationConfig{Enabled: true, KeysFile: "mounted.json"}
	valid.defaults(60)
	if err := valid.validate(billing, 60); err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*GenerationConfig){
		"workers":        func(c *GenerationConfig) { c.Workers = 33 },
		"poll":           func(c *GenerationConfig) { c.PollMilliseconds = 0 },
		"lease":          func(c *GenerationConfig) { c.LeaseSeconds = 61 },
		"deadline":       func(c *GenerationConfig) { c.DeadlineSeconds = 301 },
		"retention":      func(c *GenerationConfig) { c.RetentionSeconds = 60 },
		"output":         func(c *GenerationConfig) { c.MaxOutputBytes = 9 << 20 },
		"instance bytes": func(c *GenerationConfig) { c.InstanceBytes = 65 << 20 },
		"owner records":  func(c *GenerationConfig) { c.OwnerRecords = c.InstanceRecords },
		"owner bytes":    func(c *GenerationConfig) { c.OwnerBytes = 1 },
		"missing keys":   func(c *GenerationConfig) { c.KeysFile = " " },
	} {
		t.Run(name, func(t *testing.T) {
			invalid := valid
			change(&invalid)
			if err := invalid.validate(billing, 60); err == nil {
				t.Fatal("unsafe configuration accepted")
			}
		})
	}
	for _, mode := range []string{"disabled", "legacy"} {
		unsafeBilling := billing
		unsafeBilling.Mode = mode
		if err := valid.validate(unsafeBilling, 60); err == nil || !strings.Contains(err.Error(), "strict") {
			t.Fatal("durable execution downgraded billing", err)
		}
	}
}
