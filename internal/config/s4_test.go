package config

import "testing"

func TestS4RetentionDisableAndContainerDatabaseOverride(t *testing.T) {
	t.Setenv(EnvAdminKey, "isolated-config-test-only")
	t.Setenv(EnvLogRetentionDays, "")
	t.Setenv(EnvDatabasePath, "/app/data/gateway.db")
	cfg, err := Load(writeCfg(t, "log_retention_days: 0\ndatabase:\n  path: .local/data/gateway.db\n"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.LogRetentionDays != 0 || cfg.Database.Path != "/app/data/gateway.db" {
		t.Fatalf("explicit retention disable or mounted database override lost: %+v", cfg)
	}
}
