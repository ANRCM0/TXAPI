package config

import (
	"strings"
	"testing"
)

func TestOperatorDeclaredBillingFieldsAreExplicitStartupPolicy(t *testing.T) {
	t.Setenv(EnvAdminKey, "isolated-operator-policy-admin")
	t.Setenv(EnvBillingMode, "")
	cfg, err := Load(writeCfg(t, `billing:
  mode: strict
  max_request_cost: "1"
  max_provider_request_cost: "0.01"
  provider_contracts:
    - channel_id: 1
      base_url: https://relay.example
      protocol: openai
      actual_model: relay-text-model
      operation: responses
      input_bound_profile: operator_responses_v1
      output_limit_field: max_output_tokens
      input_token_ceiling: 8192
      max_output_tokens: 256
      input_price_per_1k: "0.001"
      output_price_per_1k: "0.002"
      tariff_profile: operator_declared_tokens_v1
      currency: USD
      policy_version: supplier-contract-v1
      policy_expires_at: 1893456000
      max_request_bytes: 4096
      additional_cost_per_attempt: "0.0001"
      evidence: Operator declaration, not a remote invoice certification
      credential_bindings:
        "7": "0000000000000000000000000000000000000000000000000000000000000000"
`))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.HasSeedData() || len(cfg.Billing.ProviderContracts) != 1 {
		t.Fatal("startup policy treated as seed data")
	}
	c := cfg.Billing.ProviderContracts[0]
	if c.PolicyVersion != "supplier-contract-v1" || c.PolicyExpiresAt != 1893456000 || c.MaxRequestBytes != 4096 || c.AdditionalCostPerAttempt != "0.0001" || c.TariffProfile != "operator_declared_tokens_v1" {
		t.Fatalf("operator declaration changed on load: %+v", c)
	}
}

func TestOperatorPolicyRejectsCoercedYAMLDeclarations(t *testing.T) {
	t.Setenv(EnvAdminKey, "isolated-operator-policy-admin")
	t.Setenv(EnvBillingMode, "")
	for _, value := range []string{"policy_expires_at: 1893456000.5", "max_request_bytes: 100.5", "policy_version: 17", "additional_cost_per_attempt: 0.001", "input_token_ceiling: 100.5", "input_price_per_1k: 0.001"} {
		_, err := Load(writeCfg(t, "billing:\n  mode: strict\n  provider_contracts:\n    - tariff_profile: operator_declared_tokens_v1\n      "+value+"\n"))
		if err == nil || !strings.Contains(err.Error(), "billing.provider_contracts.") {
			t.Fatalf("coerced declaration accepted: %s %v", value, err)
		}
	}
}
