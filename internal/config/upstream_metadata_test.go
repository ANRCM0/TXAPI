package config

import "testing"

func TestUpstreamMetadataConfigurationRequiresExplicitBoolean(t *testing.T) {
	for _, body := range []string{minimalCfg, minimalCfg + "server:\n  forward_upstream_metadata: false\n"} {
		cfg, err := Load(writeCfg(t, body))
		if err != nil || cfg.Server.ForwardUpstreamMetadata {
			t.Fatalf("metadata must default off: %+v %v", cfg, err)
		}
	}
	for _, value := range []string{"true", "&enabled true", "*enabled"} {
		prefix := ""
		if value == "*enabled" {
			prefix = "metadata_enabled: &enabled true\n"
		}
		cfg, err := Load(writeCfg(t, minimalCfg+prefix+"server:\n  forward_upstream_metadata: "+value+"\n"))
		if err != nil || !cfg.Server.ForwardUpstreamMetadata {
			t.Fatalf("explicit boolean %q rejected: %+v %v", value, cfg, err)
		}
	}
	for _, value := range []string{"'true'", "1", "null", "[]", "{}", "yes", "1.0"} {
		if _, err := Load(writeCfg(t, minimalCfg+"server:\n  forward_upstream_metadata: "+value+"\n")); err == nil {
			t.Fatalf("nonboolean opt-in accepted: %s", value)
		}
	}
	for _, value := range []string{"true", "'true'"} {
		cfg, err := Load(writeCfg(t, minimalCfg+"defaults: &defaults\n  forward_upstream_metadata: "+value+"\nserver:\n  <<: *defaults\n"))
		if value == "true" && (err != nil || !cfg.Server.ForwardUpstreamMetadata) || value != "true" && err == nil {
			t.Fatalf("merge bypassed boolean policy: value=%s cfg=%+v err=%v", value, cfg, err)
		}
	}
}
