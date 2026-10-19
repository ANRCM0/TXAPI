package config

import (
	"fmt"

	"gopkg.in/yaml.v3"
)

// Keep this opt-in an actual YAML boolean, including through aliases/merges.
// yaml.v3 also accepts some string scalars as bool; enabling a new response
// disclosure policy must be explicit. Other server fields keep their decoder.
func (s *ServerConfig) UnmarshalYAML(node *yaml.Node) error {
	var setting struct {
		Value yaml.Node `yaml:"forward_upstream_metadata"`
	}
	if err := node.Decode(&setting); err != nil {
		return err
	}
	value := &setting.Value
	if value.Kind == yaml.AliasNode {
		value = value.Alias
	}
	if value.Kind != 0 && (value.Kind != yaml.ScalarNode || value.Tag != "!!bool") {
		return fmt.Errorf("server.forward_upstream_metadata must be a boolean")
	}
	type plain ServerConfig
	return node.Decode((*plain)(s))
}
