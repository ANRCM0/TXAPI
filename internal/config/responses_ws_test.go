package config

import "testing"

func TestResponsesWSConfigRequiresExplicitSafeSettings(t *testing.T) {
	valid := ResponsesWSConfig{Enabled: true, Models: []string{"gpt-6-astra"}, Origins: []string{"https://client.example"}}
	if err := valid.validate(); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*ResponsesWSConfig){
		"empty models":    func(c *ResponsesWSConfig) { c.Models = nil },
		"wildcard":        func(c *ResponsesWSConfig) { c.Models = []string{"*"} },
		"duplicate":       func(c *ResponsesWSConfig) { c.Models = []string{"m", "m"} },
		"wildcard origin": func(c *ResponsesWSConfig) { c.Origins = []string{"https://*.example"} },
		"path origin":     func(c *ResponsesWSConfig) { c.Origins = []string{"https://client.example/path"} },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := valid
			mutate(&candidate)
			if err := candidate.validate(); err == nil {
				t.Fatal("unsafe config accepted")
			}
		})
	}
}

func TestResponsesWSDisabledAllowsNoProviderSettings(t *testing.T) {
	if err := (ResponsesWSConfig{}).validate(); err != nil {
		t.Fatal(err)
	}
}
