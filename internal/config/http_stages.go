package config

import (
	"fmt"

	"gopkg.in/yaml.v3"
)

// yaml.v3 normally truncates floating-point scalars when decoding into int.
// Stage budgets require whole seconds so invalid limits fail at startup.
func (s *HTTPStageTimeoutsConfig) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind != yaml.MappingNode {
		return fmt.Errorf("server.http_stage_timeouts must be a mapping")
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		key, value := node.Content[i].Value, node.Content[i+1]
		switch key {
		case "headers_seconds", "first_effective_output_seconds", "stream_idle_seconds", "downstream_write_seconds":
			if value.Kind == yaml.AliasNode {
				value = value.Alias
			}
			if value.Kind != yaml.ScalarNode || value.Tag != "!!int" {
				return fmt.Errorf("server.http_stage_timeouts.%s must be whole seconds", key)
			}
		default:
			return fmt.Errorf("unknown server.http_stage_timeouts field %q", key)
		}
	}
	type plain HTTPStageTimeoutsConfig
	return node.Decode((*plain)(s))
}
