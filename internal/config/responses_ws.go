package config

import (
	"fmt"
	"net/url"
	"strings"
)

type ResponsesWSConfig struct {
	Enabled bool     `yaml:"enabled"`
	Models  []string `yaml:"models"`
	Origins []string `yaml:"origins"`
}

func (c ResponsesWSConfig) validate() error {
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
