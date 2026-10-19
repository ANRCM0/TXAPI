package config

import (
	"gopkg.in/yaml.v3"
	"testing"
)

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

func TestResponsesWSStoredHistoryRequiresExplicitDependencies(t *testing.T) {
	if (ResponsesWSConfig{PersistentHistory: true}).validate() == nil {
		t.Fatal("persistent history accepted a disabled websocket runtime")
	}
	for _, raw := range []string{"null", "\"true\"", "yes", "1", "[]", "{}"} {
		var c ResponsesWSConfig
		if err := yaml.Unmarshal([]byte("persistent_history: "+raw), &c); err == nil {
			t.Fatalf("non-boolean persistent history enabled: %s", raw)
		}
	}
	for _, document := range []string{"persistent_history: true", "flag: &enabled true\npersistent_history: *enabled", "base: &base {persistent_history: true}\n<<: *base"} {
		var c ResponsesWSConfig
		if err := yaml.Unmarshal([]byte(document), &c); err != nil || !c.PersistentHistory {
			t.Fatal("explicit boolean or alias rejected", document, err)
		}
	}
}

func TestResponsesWSStoredHistoryGlobalDependenciesAndDefault(t *testing.T) {
	const base = "admin_key: fixture-key\nbilling:\n  mode: disabled\ndatabase:\n  credential_keys_file: fixture-credentials.json\nresponses_ws:\n  enabled: true\n  models: [m]\n"
	cfg, err := Load(writeCfg(t, base))
	if err != nil || cfg.ResponsesWS.PersistentHistory {
		t.Fatal("native WS default changed", err)
	}
	if _, err = Load(writeCfg(t, base+"  persistent_history: true\n")); err == nil {
		t.Fatal("stored WS started without HTTP history owner")
	}
	cfg, err = Load(writeCfg(t, base+"  persistent_history: true\nresponses:\n  enabled: true\n  keys_file: fixture-content.json\n"))
	if err != nil || !cfg.ResponsesWS.PersistentHistory {
		t.Fatal("valid explicit dependencies rejected", err)
	}
}
