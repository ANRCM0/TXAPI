package config

import (
	"fmt"
	"net/url"
	"strings"

	"gopkg.in/yaml.v3"
)

type ResponsesWSConfig struct {
	Enabled           bool     `yaml:"enabled"`
	PersistentHistory bool     `yaml:"persistent_history"`
	Models            []string `yaml:"models"`
	Origins           []string `yaml:"origins"`
}

func (c *ResponsesWSConfig) UnmarshalYAML(node *yaml.Node) error {
	var setting struct {
		Value yaml.Node `yaml:"persistent_history"`
	}
	if err := node.Decode(&setting); err != nil {
		return err
	}
	value := &setting.Value
	if value.Kind == yaml.AliasNode {
		value = value.Alias
	}
	if value.Kind != 0 && (value.Kind != yaml.ScalarNode || value.Tag != "!!bool") {
		return fmt.Errorf("responses_ws.persistent_history must be a boolean")
	}
	type plain ResponsesWSConfig
	return node.Decode((*plain)(c))
}

func (c ResponsesWSConfig) validate() error {
	if c.PersistentHistory && !c.Enabled {
		return fmt.Errorf("responses_ws.persistent_history requires responses_ws.enabled")
	}
	if !c.Enabled {
		return nil
	}
	if len(c.Models) == 0 || len(c.Models) > 64 || len(c.Origins) > 32 {
		return fmt.Errorf("responses_ws requires 1..64 explicit models and at most 32 origins")
	}
	seen := map[string]bool{}
	for _, m := range c.Models {
		if strings.TrimSpace(m) != m || m == "" || len(m) > 200 || strings.ContainsAny(m, "*\r\n") || seen[m] {
			return fmt.Errorf("responses_ws.models requires distinct explicit model names")
		}
		seen[m] = true
	}
	for _, origin := range c.Origins {
		u, err := url.Parse(origin)
		if err != nil || strings.ContainsAny(origin, "*?#\\\r\n\t ,") || u == nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" {
			return fmt.Errorf("responses_ws.origins requires exact http(s) scheme and host")
		}
	}
	return nil
}
