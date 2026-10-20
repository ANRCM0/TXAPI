package config

import (
	"fmt"

	"gopkg.in/yaml.v3"
)

// Operator declarations must not silently truncate YAML floats or coerce
// monetary scalars into strings before their immutable snapshot is compiled.
func (c *ProviderContractConfig) UnmarshalYAML(node *yaml.Node) error {
	type plain ProviderContractConfig
	if err := node.Decode((*plain)(c)); err != nil {
		return err
	}
	var fields map[string]yaml.Node
	if err := node.Decode(&fields); err != nil {
		return err
	}
	expect := map[string]string{"policy_version": "!!str", "policy_expires_at": "!!int", "max_request_bytes": "!!int", "additional_cost_per_attempt": "!!str"}
	if c.TariffProfile == "operator_declared_tokens_v1" {
		for _, key := range []string{"channel_id", "input_token_ceiling", "max_output_tokens"} {
			expect[key] = "!!int"
		}
		for _, key := range []string{"input_price_per_1k", "output_price_per_1k"} {
			expect[key] = "!!str"
		}
	}
	for key, tag := range expect {
		value, exists := fields[key]
		if !exists {
			continue
		}
		if value.Kind == yaml.AliasNode {
			value = *value.Alias
		}
		if value.Kind != yaml.ScalarNode || value.Tag != tag {
			return fmt.Errorf("billing.provider_contracts.%s requires %s", key, tag)
		}
	}
	return nil
}
