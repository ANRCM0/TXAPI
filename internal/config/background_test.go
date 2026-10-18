package config

import "testing"

func TestBackgroundConfigurationIsExplicitAndDisabledBillingOnly(t *testing.T) {
	prefix := minimalCfg + "\ndatabase:\n  credential_keys_file: credential.json\nresponses:\n  enabled: true\n  keys_file: content.json\n  background:\n    enabled: true\n    channel_ids: [1]\n"
	for _, mode := range []string{"legacy", "strict"} {
		if _, e := Load(writeCfg(t, prefix+"billing:\n  mode: "+mode+"\n")); e == nil {
			t.Fatal("charged mode enabled background", mode)
		}
	}
	cfg, e := Load(writeCfg(t, prefix+"billing:\n  mode: disabled\n"))
	if e != nil {
		t.Fatal(e)
	}
	b := cfg.Responses.Background
	if !b.Enabled || len(b.ChannelIDs) != 1 || b.RetentionSeconds != 600 || b.MetadataSeconds != 86400 || b.InstanceActive != 16 || b.OwnerActive != 2 || b.InstanceBytes != 64<<20 || b.OwnerBytes != 16<<20 {
		t.Fatal("unexpected background defaults", b)
	}
	for _, change := range []func(*ResponsesConfig){
		func(c *ResponsesConfig) { c.Enabled = false },
		func(c *ResponsesConfig) { c.Background.ChannelIDs = nil },
		func(c *ResponsesConfig) { c.Background.ChannelIDs = []uint{1, 1} },
		func(c *ResponsesConfig) { c.Background.ChannelIDs = []uint{0} },
		func(c *ResponsesConfig) { c.Background.RetentionSeconds = 299 },
		func(c *ResponsesConfig) { c.Background.RetentionSeconds = 3601 },
		func(c *ResponsesConfig) { c.Background.MetadataSeconds = 599 },
	} {
		copy := cfg.Responses
		change(&copy)
		if copy.validate() == nil {
			t.Fatal("unsafe background configuration accepted")
		}
	}
}
