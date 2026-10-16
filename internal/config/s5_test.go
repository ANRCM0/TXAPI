package config

import "testing"

func TestS5BillingModeConfiguration(t *testing.T) {
	t.Setenv(EnvAdminKey, "isolated-test-admin-only")
	t.Setenv(EnvBillingMode, "")
	for _, mode := range []string{"legacy", "disabled", "strict", "typo"} {
		t.Run(mode, func(t *testing.T) {
			cfg, err := Load(writeCfg(t, "billing:\n  mode: "+mode+"\n"))
			if mode == "typo" {
				if err == nil {
					t.Fatal("unknown billing mode accepted")
				}
			} else if err != nil || cfg.Billing.Mode != mode {
				t.Fatalf("mode lost: cfg=%+v err=%v", cfg, err)
			}
		})
	}
	cfg, err := Load(writeCfg(t, ""))
	if err != nil || cfg.Billing.Mode != "legacy" {
		t.Fatalf("existing deployment behavior changed: cfg=%+v err=%v", cfg, err)
	}
	t.Setenv(EnvBillingMode, "strict")
	cfg, err = Load(writeCfg(t, "billing:\n  mode: disabled\n"))
	if err != nil || cfg.Billing.Mode != "strict" {
		t.Fatalf("environment override lost: cfg=%+v err=%v", cfg, err)
	}
	t.Setenv(EnvBillingMode, "typo")
	if _, err := Load(writeCfg(t, "")); err == nil {
		t.Fatal("invalid environment mode silently downgraded")
	}
}
