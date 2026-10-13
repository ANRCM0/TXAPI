package config

import (
	"os"
	"path/filepath"
	"testing"
)

// writeCfg 写一份最小合法配置到临时目录并返回路径。
//
// admin_key 是必填项（批次 1 起），所以每个用例都要带上；
// 用环境变量而不是文件里的值，避免测环境变量覆盖时被文件值干扰。
func writeCfg(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("写配置失败: %v", err)
	}
	return path
}

// minimalCfg 是能通过 validate 的最小配置（必填 admin_key）。
const minimalCfg = "admin_key: test-key\n"

// TestLogLevelDefaults 验证不配 log_level 时默认 info。
func TestLogLevelDefaults(t *testing.T) {
	cfg, err := Load(writeCfg(t, minimalCfg))
	if err != nil {
		t.Fatalf("加载失败: %v", err)
	}
	if cfg.LogLevel != "info" {
		t.Errorf("默认 log_level = %q，期望 info", cfg.LogLevel)
	}
}

// TestLogLevelFromYAMLAndNormalized 验证 YAML 值被读取且大小写归一。
//
// 归一很重要：配置里写 DEBUG 是常见习惯，如果 validate 白名单只认
// 小写，用户会遇到「明明配了却拒绝启动」且看不出原因。
func TestLogLevelFromYAMLAndNormalized(t *testing.T) {
	cfg, err := Load(writeCfg(t, "admin_key: k\nlog_level: DEBUG\n"))
	if err != nil {
		t.Fatalf("加载失败: %v", err)
	}
	if cfg.LogLevel != "debug" {
		t.Errorf("log_level = %q，期望 debug（归一化后）", cfg.LogLevel)
	}
}

// TestLogLevelEnvOverridesYAML 验证环境变量优先于 YAML。
//
// 这是 Docker 部署的用法：镜像里的配置写死 info，运行时用
// -e GATEWAY_LOG_LEVEL=debug 开调试，不用重打镜像。
func TestLogLevelEnvOverridesYAML(t *testing.T) {
	t.Setenv(EnvLogLevel, "warn")
	cfg, err := Load(writeCfg(t, "admin_key: k\nlog_level: debug\n"))
	if err != nil {
		t.Fatalf("加载失败: %v", err)
	}
	if cfg.LogLevel != "warn" {
		t.Errorf("log_level = %q，期望 warn（环境变量应优先）", cfg.LogLevel)
	}
}

// TestLogLevelInvalidRejected 验证非法级别在启动时就被拒。
//
// 关键点是「拒绝启动」而不是「静默退化到 info」：后者会让运维以为
// 开了 debug 却看不到调试信息，排查时怀疑代码而不是配置。
func TestLogLevelInvalidRejected(t *testing.T) {
	_, err := Load(writeCfg(t, "admin_key: k\nlog_level: verbose\n"))
	if err == nil {
		t.Fatal("非法日志级别应该被拒绝启动")
	}
}

// TestLogRetentionDefault 验证保留天数默认 30。
func TestLogRetentionDefault(t *testing.T) {
	cfg, err := Load(writeCfg(t, minimalCfg))
	if err != nil {
		t.Fatalf("加载失败: %v", err)
	}
	if cfg.LogRetentionDays != 30 {
		t.Errorf("默认 log_retention_days = %d，期望 30", cfg.LogRetentionDays)
	}
}

// TestLogRetentionFromYAML 验证 YAML 里的值被读取。
func TestLogRetentionFromYAML(t *testing.T) {
	cfg, err := Load(writeCfg(t, "admin_key: k\nlog_retention_days: 7\n"))
	if err != nil {
		t.Fatalf("加载失败: %v", err)
	}
	if cfg.LogRetentionDays != 7 {
		t.Errorf("log_retention_days = %d，期望 7", cfg.LogRetentionDays)
	}
}

// TestLogRetentionEnvOverridesYAML 验证环境变量优先，且非整数直接报错。
//
// 解析失败必须报错而不是静默取 0：0 的语义是「关闭清理」，静默变 0
// 会让运维以为在清理而 logs 表无限增长。
func TestLogRetentionEnvOverridesYAML(t *testing.T) {
	t.Setenv(EnvLogRetentionDays, "14")
	cfg, err := Load(writeCfg(t, "admin_key: k\nlog_retention_days: 7\n"))
	if err != nil {
		t.Fatalf("加载失败: %v", err)
	}
	if cfg.LogRetentionDays != 14 {
		t.Errorf("log_retention_days = %d，期望 14（环境变量应优先）", cfg.LogRetentionDays)
	}

	t.Setenv(EnvLogRetentionDays, "abc")
	if _, err := Load(writeCfg(t, minimalCfg)); err == nil {
		t.Fatal("非整数的保留天数应该报错而不是静默取 0")
	}
}

// TestLogRetentionNegativeRejected 验证负数被拒。
func TestLogRetentionNegativeRejected(t *testing.T) {
	_, err := Load(writeCfg(t, "admin_key: k\nlog_retention_days: -5\n"))
	if err == nil {
		t.Fatal("负的保留天数应该被拒绝启动")
	}
}

// TestChannelKeyFromEnv 验证上游 Key 走环境变量注入（B4-4 的 Docker
// 部署依赖它）。
//
// 为什么要这条路径：配置文件会进 git、进镜像层，把上游 Key 写进去
// 等于把凭据提交进仓库。环境变量注入让「镜像里没有任何密钥」成为
// 事实而不是靠纪律。
func TestChannelKeyFromEnv(t *testing.T) {
	t.Setenv(EnvChannelKeyPrefix+"1", "sk-from-env")
	cfg, err := Load(writeCfg(t, `admin_key: k
channels:
  - id: 1
    name: ch1
    type: openai
    base_url: http://127.0.0.1:9911
    key: sk-in-config
    models: ["gpt-4o-mini"]
`))
	if err != nil {
		t.Fatalf("加载失败: %v", err)
	}
	if len(cfg.Channels) != 1 {
		t.Fatalf("渠道数 = %d，期望 1", len(cfg.Channels))
	}
	if cfg.Channels[0].Key != "sk-from-env" {
		t.Errorf("渠道 key = %q，期望 sk-from-env（环境变量应优先）", cfg.Channels[0].Key)
	}
}

// TestChannelKeyEnvEmptyIgnored 验证空的环境变量不清空已有 key。
//
// 空串 = 「没设」：把环境变量设成空（比如 compose 里写了个未赋值的
// ${UPSTREAM_KEY}）不该把渠道变成没有 Key 的空壳。
func TestChannelKeyEnvEmptyIgnored(t *testing.T) {
	t.Setenv(EnvChannelKeyPrefix+"1", "   ")
	cfg, err := Load(writeCfg(t, `admin_key: k
channels:
  - id: 1
    name: ch1
    type: openai
    base_url: http://127.0.0.1:9911
    key: sk-in-config
    models: ["gpt-4o-mini"]
`))
	if err != nil {
		t.Fatalf("加载失败: %v", err)
	}
	if cfg.Channels[0].Key != "sk-in-config" {
		t.Errorf("渠道 key = %q，期望保持 sk-in-config", cfg.Channels[0].Key)
	}
}

// TestChannelKeyEnvOnlyForMatchingID 验证只影响对应 ID 的渠道。
func TestChannelKeyEnvOnlyForMatchingID(t *testing.T) {
	t.Setenv(EnvChannelKeyPrefix+"1", "sk-one")
	cfg, err := Load(writeCfg(t, `admin_key: k
channels:
  - id: 1
    name: ch1
    type: openai
    base_url: http://127.0.0.1:9911
    key: sk-a
    models: ["m"]
  - id: 2
    name: ch2
    type: openai
    base_url: http://127.0.0.1:9911
    key: sk-b
    models: ["m"]
`))
	if err != nil {
		t.Fatalf("加载失败: %v", err)
	}
	if cfg.Channels[0].Key != "sk-one" || cfg.Channels[1].Key != "sk-b" {
		t.Errorf("key 覆盖串到了别的渠道: ch1=%q ch2=%q", cfg.Channels[0].Key, cfg.Channels[1].Key)
	}
}
