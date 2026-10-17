package config

import "testing"

func TestEmbeddingInputBoundProfileConfiguration(t *testing.T) {
	t.Setenv(EnvAdminKey, "isolated-test-admin-only")
	t.Setenv(EnvBillingMode, "")
	cfg, err := Load(writeCfg(t, `billing:
  mode: strict
  provider_contracts:
    - channel_id: 1
      base_url: "https://api.openai.com"
      protocol: openai
      actual_model: text-embedding-3-small
      operation: embeddings
      input_bound_profile: openai_embeddings_v1
      input_token_ceiling: 300000
      input_price_per_1k: "0.00002"
      output_price_per_1k: "0"
      evidence: "isolated fixture"
`))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.HasSeedData() || len(cfg.Billing.ProviderContracts) != 1 || cfg.Billing.ProviderContracts[0].InputBoundProfile != "openai_embeddings_v1" {
		t.Fatalf("input bound profile lost or treated as seed: %+v", cfg)
	}
}

func TestProviderCostConfigurationIsNotSeedData(t *testing.T) {
	t.Setenv(EnvAdminKey, "isolated-test-admin-only")
	t.Setenv(EnvBillingMode, "")
	cfg, err := Load(writeCfg(t, `billing:
  mode: strict
  max_provider_request_cost: "0.002128"
  provider_contracts:
    - channel_id: 1
      base_url: "https://fixture.invalid"
      protocol: openai
      actual_model: m
      operation: chat
      output_limit_field: max_tokens
      input_token_ceiling: 1000
      max_output_tokens: 32
      input_price_per_1k: "0.001"
      output_price_per_1k: "0.002"
      evidence: "isolated fixture"
`))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.HasSeedData() || cfg.Billing.MaxProviderRequestCost != "0.002128" || len(cfg.Billing.ProviderContracts) != 1 {
		t.Fatalf("provider policy lost or treated as seed data: %+v", cfg)
	}
	c := cfg.Billing.ProviderContracts[0]
	if c.InputPricePer1K != "0.001" || c.InputTokenCeiling != 1000 || c.OutputLimitField != "max_tokens" || c.Evidence != "isolated fixture" {
		t.Fatalf("cost contract changed on load: %+v", c)
	}
}

func TestS5BillingModeConfiguration(t *testing.T) {
	t.Setenv(EnvAdminKey, "isolated-test-admin-only")
	t.Setenv(EnvBillingMode, "")
	for _, mode := range []string{"legacy", "disabled", "strict", "typo"} {
		t.Run(mode, func(t *testing.T) {
			cfg, err := Load(writeCfg(t, "billing:\n  mode: "+mode+"\n"))
			if mode == "typo" {
				if err == nil {
					t.Fatal("unknown billing mode accepted")
				}
			} else if err != nil || cfg.Billing.Mode != mode {
				t.Fatalf("mode lost: cfg=%+v err=%v", cfg, err)
			}
		})
	}
	cfg, err := Load(writeCfg(t, ""))
	if err != nil || cfg.Billing.Mode != "legacy" {
		t.Fatalf("existing deployment behavior changed: cfg=%+v err=%v", cfg, err)
	}
	t.Setenv(EnvBillingMode, "strict")
	cfg, err = Load(writeCfg(t, "billing:\n  mode: disabled\n"))
	if err != nil || cfg.Billing.Mode != "strict" {
		t.Fatalf("environment override lost: cfg=%+v err=%v", cfg, err)
	}
	t.Setenv(EnvBillingMode, "typo")
	if _, err := Load(writeCfg(t, "")); err == nil {
		t.Fatal("invalid environment mode silently downgraded")
	}
}
