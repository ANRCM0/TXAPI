// Package config 负责把 YAML 配置文件读进内存，并做默认值填充和校验。
//
// 设计原则：配置在启动时一次性加载完，运行期不再读盘。
// 这样转发路径上没有任何文件 IO，也不会因为配置文件被改动导致行为漂移。
//
// P2 起职责收窄：YAML 只剩两件事——
//  1. server 段：网关自身运行参数（监听地址/重试/超时）
//  2. database 段：数据库位置
//  3. channels / tokens 段降级为「首次启动的种子数据」，只在数据库为空时导入一次
//
// 理由：渠道和令牌要能通过 REST API 热更新，就不能再把它们当成静态配置。
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// ServerConfig 是网关自身的运行参数。
type ServerConfig struct {
	Addr string `yaml:"addr"`
	// RetryTimes 是「首次失败后最多再试几次」。
	// 设成 2 意味着最坏情况下一个请求会打 3 个渠道。
	RetryTimes int `yaml:"retry_times"`
	// TimeoutSeconds 是单次上游请求的超时时间。
	// 流式响应会长时间挂着连接，所以这个值不能太小。
	TimeoutSeconds int `yaml:"timeout_seconds"`
	// TotalTimeoutSeconds bounds the entire request, including all retries.
	TotalTimeoutSeconds int `yaml:"total_timeout_seconds"`
	// HTTPStageTimeouts applies only to foreground HTTP attempts; zero disables each stage.
	HTTPStageTimeouts HTTPStageTimeoutsConfig `yaml:"http_stage_timeouts"`
	// ForwardUpstreamMetadata exposes only validated final-attempt diagnostic headers.
	ForwardUpstreamMetadata bool `yaml:"forward_upstream_metadata"`
	// SessionAffinity is an opt-in optimization. Keep it disabled until an
	// operator has measured a real upstream cache/latency benefit.
	SessionAffinity bool `yaml:"session_affinity"`
	// RouteDiagnostics keeps bounded recent HTTP routing metadata in memory.
	RouteDiagnostics bool `yaml:"route_diagnostics"`
	// ShadowRouteScoring compares recommendations only when diagnostics is enabled.
	ShadowRouteScoring bool `yaml:"shadow_route_scoring"`
	// IPRPMLimit 是 IP 级限流的每分钟请求数上限，0 表示不限。
	//
	// 为什么和令牌级限流分开：令牌级限流挂在鉴权之后，管得住
	// 「有令牌的滥用」，管不住「拿一堆随机 key 试」的探测请求
	// ——那种请求根本过不了鉴权，也就走不到令牌级限流那一步，
	// 但每一次都实打实打了网关（查库、比对）。IP 级限流挂在
	// 鉴权之前，是这条路上唯一的闸门。
	//
	// 默认 0（不限）：自用场景下访问来源就是本机或内网，没有
	// 探测流量，开了反而可能挡掉正常的并发压测。暴露到公网时
	// 一定要配上（比如 120，即平均每秒 2 次）。
	IPRPMLimit  int               `yaml:"ip_rpm_limit"`
	Concurrency ConcurrencyConfig `yaml:"concurrency"`
}

// HTTPStageTimeoutsConfig keeps stage budgets separate from the total request budget.
type HTTPStageTimeoutsConfig struct {
	HeadersSeconds              int `yaml:"headers_seconds"`
	FirstEffectiveOutputSeconds int `yaml:"first_effective_output_seconds"`
	StreamIdleSeconds           int `yaml:"stream_idle_seconds"`
	DownstreamWriteSeconds      int `yaml:"downstream_write_seconds"`
}

// ConcurrencyConfig sets per-process ceilings; zero selects finite defaults.
// Individual user/Key settings can further restrict these ceilings.
type ConcurrencyConfig struct {
	Instance int `yaml:"instance"`
	User     int `yaml:"user"`
	Key      int `yaml:"key"`
	Channel  int `yaml:"channel"`
}

// DatabaseConfig 是存储层配置。
type DatabaseConfig struct {
	CredentialKeysFile string `yaml:"credential_keys_file"`
	// Driver 目前只支持 "sqlite"（留字段给 PostgreSQL，见 PLAN 技术栈）。
	Driver string `yaml:"driver"`
	// Path 是 SQLite 文件路径，相对路径按进程工作目录解析。
	// 默认 data/gateway.db。
	Path string `yaml:"path"`
}

type BillingConfig struct {
	// Legacy preserves availability-first quota behavior. Disabled does not
	// charge; strict requires a fee ceiling and durable settlement components.
	Mode                   string                   `yaml:"mode"`
	MaxRequestCost         string                   `yaml:"max_request_cost"`
	MaxProviderRequestCost string                   `yaml:"max_provider_request_cost"`
	ProviderContracts      []ProviderContractConfig `yaml:"provider_contracts"`
}

// Startup-only, independently priced provider envelopes; never seed data.
type ProviderContractConfig struct {
	ChannelID                uint              `yaml:"channel_id"`
	BaseURL                  string            `yaml:"base_url"`
	Protocol                 string            `yaml:"protocol"`
	ActualModel              string            `yaml:"actual_model"`
	Operation                string            `yaml:"operation"`
	OutputLimitField         string            `yaml:"output_limit_field"`
	InputTokenCeiling        int64             `yaml:"input_token_ceiling"`
	InputBoundProfile        string            `yaml:"input_bound_profile"`
	MaxOutputTokens          int64             `yaml:"max_output_tokens"`
	InputPricePer1K          string            `yaml:"input_price_per_1k"`
	OutputPricePer1K         string            `yaml:"output_price_per_1k"`
	Evidence                 string            `yaml:"evidence"`
	TariffProfile            string            `yaml:"tariff_profile"`
	Currency                 string            `yaml:"currency"`
	CredentialBindings       map[string]string `yaml:"credential_bindings"`
	PolicyVersion            string            `yaml:"policy_version"`
	PolicyExpiresAt          int64             `yaml:"policy_expires_at"`
	MaxRequestBytes          int               `yaml:"max_request_bytes"`
	AdditionalCostPerAttempt string            `yaml:"additional_cost_per_attempt"`
}

// ChannelConfig 描述一个上游渠道，仅用于「从 YAML 导入空数据库」。
//
// 注意：这个结构体是**种子数据格式**，不是运行期渠道定义。
// 运行期渠道定义在 internal/model 的 Channel 上，两者靠 seed 逻辑转换。
type ChannelConfig struct {
	ID      int    `yaml:"id"`
	Name    string `yaml:"name"`
	Type    string `yaml:"type"`
	BaseURL string `yaml:"base_url"`
	// Key 是给上游用的渠道 key。导入时会变成该渠道下的第一个凭据（credential），
	// 因为从 P2 开始「渠道」和「Key」就是两个概念了。
	Key     string            `yaml:"key"`
	Models  []string          `yaml:"models"`
	Mapping map[string]string `yaml:"model_mapping"`
	Weight  int               `yaml:"weight"`
	// Enabled 用指针是为了区分「没写」和「写了 false」。
	Enabled *bool `yaml:"enabled"`
}

// IsEnabled 在没配置 enabled 时默认视为启用。
func (c *ChannelConfig) IsEnabled() bool { return c.Enabled == nil || *c.Enabled }

// TokenConfig 是客户端访问网关的凭证，同样是种子数据格式。
type TokenConfig struct {
	Key  string `yaml:"key"`
	Name string `yaml:"name"`
	// Models 限制这个令牌能访问哪些模型，空表示不限制。
	Models []string `yaml:"models"`
	// Quota 预留给 P5 计费，0 表示不限额度。
	Quota float64 `yaml:"quota"`
	// RPMLimit/TPMLimit 是限流阈值，0 表示不限。
	// 只在种子导入时生效（和 quota 一样），之后通过管理 API 改。
	RPMLimit int `yaml:"rpm_limit"`
	TPMLimit int `yaml:"tpm_limit"`
}

// Config 是整份配置的根结构。
type Config struct {
	Server ServerConfig `yaml:"server"`
	// AdminKey 是管理 API（/api/*）的访问密钥。
	// 环境变量 GATEWAY_ADMIN_KEY 优先于这里的值——密钥不该写进
	// 会进 git 的配置文件，环境变量是生产部署的标准做法。
	// 两者都为空时 Load 直接报错拒绝启动（见 validate）。
	AdminKey string `yaml:"admin_key"`
	// LogLevel 是进程日志级别（debug/info/warn/error）。
	//
	// P6 批次 4 引入结构化日志（log/slog）后才有意义：同一份代码
	// 在自用调试时开 debug 看选凭据细节，在长期运行时保持 info
	// 减少噪音。环境变量 GATEWAY_LOG_LEVEL 优先，方便 Docker
	// 部署改级别而不用重打镜像。
	LogLevel string `yaml:"log_level"`
	// LogRetentionDays 是请求日志（logs 表）的保留天数，默认 30。
	//
	// 为什么需要：logs 表每请求一行、无限增长，SQLite 是单文件，
	// 表越大每次写入和统计越慢。后台任务每天分批删除过期行
	// （见 store.PurgeExpiredLogs）。
	// 0 表示关闭自动清理（比如想自己导出归档后再删），负数非法。
	// 环境变量 GATEWAY_LOG_RETENTION_DAYS 优先。
	LogRetentionDays int                    `yaml:"log_retention_days"`
	Database         DatabaseConfig         `yaml:"database"`
	Billing          BillingConfig          `yaml:"billing"`
	Quotation        QuotationConfig        `yaml:"quotation"`
	UsageHistory     UsageHistoryConfig     `yaml:"usage_history"`
	Generation       GenerationConfig       `yaml:"generation"`
	Responses        ResponsesConfig        `yaml:"responses"`
	ResponsesWS      ResponsesWSConfig      `yaml:"responses_ws"`
	UpstreamAccounts UpstreamAccountsConfig `yaml:"upstream_accounts"`
	Channels         []ChannelConfig        `yaml:"channels"`
	Tokens           []TokenConfig          `yaml:"tokens"`
}

// EnvAdminKey 是管理密钥的环境变量名。
//
// 用常量而不是散落的字符串：拼错的环境变量名会让「以为配了其实没配」，
// 而这里的设计恰恰是「没配就拒绝启动」，拼错会被立刻发现。
const EnvAdminKey = "GATEWAY_ADMIN_KEY"

// EnvLogLevel / EnvLogRetentionDays 是日志相关配置的环境变量名。
//
// 与 EnvAdminKey 同一套理由：名字集中在一处，拼错会被 validate 立刻
// 抓到（级别写错直接拒绝启动，不会静默退化到 info）。
//
// EnvServerAddr 覆盖监听地址。
//
// 为什么单独为它开环境变量：Docker 部署时容器内必须监听 0.0.0.0，
// 而配置文件的默认值是 127.0.0.1:8080（自用直连时更安全——不对外）。
// 没有这个覆盖的话，用户在 Docker 里要么改 config.yaml（那就得为
// 容器单独维护一份配置），要么把默认值改成 0.0.0.0（那就把「默认
// 只听本机」这个安全默认给破坏了）。环境变量让两种场景各取所需。
const (
	EnvLogLevel         = "GATEWAY_LOG_LEVEL"
	EnvLogRetentionDays = "GATEWAY_LOG_RETENTION_DAYS"
	EnvChannelKeyPrefix = "GATEWAY_CHANNEL_KEY_"
	EnvServerAddr       = "GATEWAY_ADDR"
	EnvDatabasePath     = "GATEWAY_DATABASE_PATH"
	EnvBillingMode      = "GATEWAY_BILLING_MODE"
)

// Load 读取并校验配置文件。
func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取配置文件 %s 失败: %w", path, err)
	}
	cfg := &Config{}
	if err := yaml.Unmarshal(raw, cfg); err != nil {
		return nil, fmt.Errorf("解析 YAML 失败: %w", err)
	}
	var explicit struct {
		LogRetentionDays *int `yaml:"log_retention_days"`
		Server           struct {
			RetryTimes *int `yaml:"retry_times"`
		} `yaml:"server"`
	}
	if err := yaml.Unmarshal(raw, &explicit); err != nil {
		return nil, fmt.Errorf("解析 YAML 失败: %w", err)
	}
	cfg.applyDefaults()
	cfg.Generation.defaults(cfg.Server.TotalTimeoutSeconds)
	cfg.Responses.defaults()
	if value := strings.TrimSpace(os.Getenv(EnvResponsesKeysFile)); value != "" {
		cfg.Responses.KeysFile = value
	}
	if value := strings.TrimSpace(os.Getenv(EnvGenerationKeysFile)); value != "" {
		cfg.Generation.KeysFile = value
	}
	if explicit.Server.RetryTimes != nil {
		cfg.Server.RetryTimes = *explicit.Server.RetryTimes
	}
	if explicit.LogRetentionDays != nil {
		cfg.LogRetentionDays = *explicit.LogRetentionDays
	}
	if v := strings.TrimSpace(os.Getenv(EnvDatabasePath)); v != "" {
		cfg.Database.Path = v
	}
	if v := strings.TrimSpace(os.Getenv(EnvCredentialKeysFile)); v != "" {
		cfg.Database.CredentialKeysFile = v
	}
	// 环境变量覆盖配置文件：部署时改配置要重新打镜像/改文件，
	// 改环境变量不用。密钥尤其应该走环境变量（不进 git）。
	if v := strings.TrimSpace(os.Getenv(EnvAdminKey)); v != "" {
		cfg.AdminKey = v
	}
	if v := strings.TrimSpace(os.Getenv(EnvLogLevel)); v != "" {
		cfg.LogLevel = v
	}
	if v := strings.TrimSpace(os.Getenv(EnvServerAddr)); v != "" {
		cfg.Server.Addr = v
	}
	if v := strings.TrimSpace(os.Getenv(EnvBillingMode)); v != "" {
		cfg.Billing.Mode = v
	}
	// 保留天数走 strconv 而不是直接字符串：YAML 里已经是 int，
	// 环境变量一律是字符串，解析失败必须报错而不是静默取 0
	// （0 的含义是「关闭清理」，静默变 0 会让运维以为在清理）。
	if v := strings.TrimSpace(os.Getenv(EnvLogRetentionDays)); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return nil, fmt.Errorf("环境变量 %s=%q 不是整数: %w", EnvLogRetentionDays, v, err)
		}
		cfg.LogRetentionDays = n
	}
	// 渠道上游 Key 按渠道 ID 从环境变量注入（B4-4 的 Docker 部署要用）：
	// GATEWAY_CHANNEL_KEY_1 覆盖 id=1 的渠道 key。
	//
	// 为什么不直接在 compose 里改 config.yaml：配置文件的渠道 key 会
	// 进 git、进镜像层，而 Key 是真正的凭据。环境变量注入让「镜像里
	// 没有任何密钥」成为事实，而不是靠纪律。
	cfg.applyChannelKeyEnv()
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// applyChannelKeyEnv 用环境变量覆盖各渠道的上游 key。
//
// 键名规则 GATEWAY_CHANNEL_KEY_<渠道ID>。只在值非空时覆盖：
// 环境变量设成空串等于「没设」，不该把一个有 key 的渠道清空。
func (c *Config) applyChannelKeyEnv() {
	for i := range c.Channels {
		key := fmt.Sprintf("%s%d", EnvChannelKeyPrefix, c.Channels[i].ID)
		if v := strings.TrimSpace(os.Getenv(key)); v != "" {
			c.Channels[i].Key = v
		}
	}
}

func (c *Config) applyDefaults() {
	c.UpstreamAccounts.defaults()
	c.UsageHistory.defaults()
	if c.Billing.Mode == "" {
		c.Billing.Mode = "legacy"
	}
	if c.Server.Concurrency.Instance == 0 {
		c.Server.Concurrency.Instance = 256
	}
	if c.Server.Concurrency.User == 0 {
		c.Server.Concurrency.User = 32
	}
	if c.Server.Concurrency.Key == 0 {
		c.Server.Concurrency.Key = 16
	}
	if c.Server.Concurrency.Channel == 0 {
		c.Server.Concurrency.Channel = 64
	}
	if c.Server.Addr == "" {
		c.Server.Addr = "127.0.0.1:8080"
	}
	if c.Server.RetryTimes == 0 {
		c.Server.RetryTimes = 2
	}
	if c.Server.TimeoutSeconds == 0 {
		c.Server.TimeoutSeconds = 120
	}
	if c.Server.TotalTimeoutSeconds == 0 {
		c.Server.TotalTimeoutSeconds = c.Server.TimeoutSeconds
	}
	if c.Database.Driver == "" {
		c.Database.Driver = "sqlite"
	}
	if c.Database.Path == "" {
		c.Database.Path = "data/gateway.db"
	}
	if c.LogLevel == "" {
		c.LogLevel = "info"
	} else {
		// 统一小写：配置里写 INFO / Info 与 info 同义，避免因为大小写
		// 在 validate 里被判成非法级别（那种「明明配了却起不来」的
		// 报错最容易被当成 bug）。
		c.LogLevel = strings.ToLower(strings.TrimSpace(c.LogLevel))
	}
	// 保留天数默认 30：一个月的流水足够做用量回溯和排障，再久的
	// 明细价值低而成本（库体积、统计扫描）线性增长。
	// 注意 0 是合法值（关闭清理），所以这里只补「没配」的情况——
	// 用 applyDefaults 的顺序保证 LogRetentionDays == 0 且 YAML 里
	// 真的写了 0 时也不会被改成 30（两者在这条路径上不可区分，
	// 取舍是「没写」远比「显式写 0」常见）。
	if c.LogRetentionDays == 0 {
		c.LogRetentionDays = 30
	}
	for i := range c.Channels {
		ch := &c.Channels[i]
		if ch.ID == 0 {
			ch.ID = i + 1
		}
		if ch.Weight == 0 {
			ch.Weight = 1
		}
		if ch.Type == "" {
			ch.Type = "openai"
		}
	}
}

// validate 在启动时就拦住配错的配置。
//
// P2 的变化：channels / tokens 从「必须有」降级为「可选」。
// 数据库里已经有渠道时，YAML 完全可以只有 server + database 两段；
// 反之数据库为空但 YAML 有种子数据时，导入逻辑会接管。
// 只有「两边都没有渠道」才是真正的启动失败。
//
// P6 批次 1 的变化：admin_key 变成**必须有**。管理 API 能改渠道、
// 看上游 Key，裸奔的代价是「谁连上 localhost 就能拿走全部凭据」。
// 所以缺密钥时拒绝启动——宁可起不来，也不要一个「以为有鉴权其实
// 没有」的网关。报错信息里直接给出两种配法，运维不用翻文档。
func (c *Config) validate() error {
	if err := c.UpstreamAccounts.validate(c.Database.CredentialKeysFile); err != nil {
		return err
	}
	if err := c.UsageHistory.validate(c.Billing.Mode); err != nil {
		return err
	}
	if err := c.Quotation.validate(); err != nil {
		return err
	}
	if err := c.ResponsesWS.validate(); err != nil {
		return err
	}
	if c.ResponsesWS.Enabled && (c.Billing.Mode != "disabled" || strings.TrimSpace(c.Database.CredentialKeysFile) == "") {
		return fmt.Errorf("responses_ws.enabled requires billing.mode disabled and database.credential_keys_file")
	}
	if c.ResponsesWS.PersistentHistory && !c.Responses.Enabled {
		return fmt.Errorf("responses_ws.persistent_history requires responses.enabled")
	}
	if c.Responses.Background.Enabled && c.Billing.Mode != "disabled" {
		return fmt.Errorf("responses.background.enabled requires billing.mode disabled")
	}
	if err := c.Responses.validate(); err != nil {
		return err
	}
	if c.Responses.Enabled && c.Responses.ReplaySeconds < c.Server.TotalTimeoutSeconds+5 {
		return fmt.Errorf("responses.replay_seconds must cover server.total_timeout_seconds plus finalization time")
	}
	if c.Responses.Enabled && c.Responses.HistorySeconds < c.Server.TotalTimeoutSeconds+5 {
		return fmt.Errorf("responses.history_seconds must cover total request timeout and finalization")
	}
	if c.Responses.Enabled && strings.TrimSpace(c.Database.CredentialKeysFile) == "" {
		return fmt.Errorf("responses.enabled requires database.credential_keys_file or %s", EnvCredentialKeysFile)
	}
	if c.Responses.Enabled && c.Generation.Enabled && c.Responses.KeysFile != c.Generation.KeysFile {
		return fmt.Errorf("responses and generation must share one content keys_file when both are enabled")
	}
	if err := c.Generation.validate(c.Billing, c.Server.TotalTimeoutSeconds); err != nil {
		return err
	}
	if c.Generation.Enabled && strings.TrimSpace(c.Database.CredentialKeysFile) == "" {
		return fmt.Errorf("generation.enabled requires database.credential_keys_file or %s", EnvCredentialKeysFile)
	}
	switch c.Billing.Mode {
	case "legacy", "disabled", "strict":
	default:
		return fmt.Errorf("billing.mode must be legacy, disabled or strict")
	}
	limits := c.Server.Concurrency
	for _, n := range []int{limits.Instance, limits.User, limits.Key, limits.Channel} {
		if n < 1 || n > 1_000_000 {
			return fmt.Errorf("concurrency ceilings must be between 1 and 1000000")
		}
	}
	if limits.User >= limits.Instance || limits.Key > limits.User {
		return fmt.Errorf("concurrency ceilings require key <= user < instance")
	}
	if strings.TrimSpace(c.AdminKey) == "" {
		return fmt.Errorf("缺少管理密钥：请设置环境变量 %s，或在配置文件的 admin_key 字段里配置", EnvAdminKey)
	}
	// 日志级别白名单：写错级别时拒绝启动而不是静默退化。退化成 info
	// 的后果是「开了 debug 却看不到调试信息」，排查时会怀疑代码而不是
	// 配置——启动即失败能立刻把矛头指向正确的地方。
	switch c.LogLevel {
	case "debug", "info", "warn", "error":
	default:
		return fmt.Errorf("非法日志级别 %q：只支持 debug/info/warn/error（或环境变量 %s）",
			c.LogLevel, EnvLogLevel)
	}
	if c.LogRetentionDays < 0 {
		return fmt.Errorf("log_retention_days 不能为负数（收到 %d；0 表示关闭自动清理）", c.LogRetentionDays)
	}
	if c.Server.RetryTimes < 0 || c.Server.RetryTimes > 10 {
		return fmt.Errorf("server.retry_times 必须在 0 到 10 之间（收到 %d）", c.Server.RetryTimes)
	}
	if c.Server.TimeoutSeconds < 1 || c.Server.TimeoutSeconds > 600 {
		return fmt.Errorf("server.timeout_seconds 必须在 1 到 600 之间（收到 %d）", c.Server.TimeoutSeconds)
	}
	if c.Server.TotalTimeoutSeconds < 1 || c.Server.TotalTimeoutSeconds > 600 {
		return fmt.Errorf("server.total_timeout_seconds must be between 1 and 600")
	}
	stages := c.Server.HTTPStageTimeouts
	for name, seconds := range map[string]int{
		"headers_seconds":                stages.HeadersSeconds,
		"first_effective_output_seconds": stages.FirstEffectiveOutputSeconds,
		"stream_idle_seconds":            stages.StreamIdleSeconds,
		"downstream_write_seconds":       stages.DownstreamWriteSeconds,
	} {
		if seconds < 0 || seconds > 3600 {
			return fmt.Errorf("server.http_stage_timeouts.%s must be between 0 and 3600", name)
		}
	}
	if c.Server.IPRPMLimit < 0 {
		return fmt.Errorf("server.ip_rpm_limit 不能为负数（收到 %d）", c.Server.IPRPMLimit)
	}
	seenChannel := map[int]bool{}
	for _, ch := range c.Channels {
		if seenChannel[ch.ID] {
			return fmt.Errorf("渠道 ID 重复: %d", ch.ID)
		}
		seenChannel[ch.ID] = true
		if ch.Weight < 0 {
			return fmt.Errorf("渠道 %d(%s) weight 不能为负数", ch.ID, ch.Name)
		}
		if ch.BaseURL == "" {
			return fmt.Errorf("渠道 %d(%s) 缺少 base_url", ch.ID, ch.Name)
		}
		// P2 起渠道的 key 挂在凭据上，但种子数据允许直接写 key 字段
		if ch.Key == "" {
			return fmt.Errorf("渠道 %d(%s) 缺少 key（导入时会成为该渠道的第一个凭据）", ch.ID, ch.Name)
		}
		if len(ch.Models) == 0 {
			return fmt.Errorf("渠道 %d(%s) 没有配置任何模型", ch.ID, ch.Name)
		}
	}
	seenToken := map[string]bool{}
	for _, tk := range c.Tokens {
		if tk.Key == "" {
			return fmt.Errorf("存在空的令牌 key")
		}
		if seenToken[tk.Key] {
			return fmt.Errorf("令牌 key 重复: %s", tk.Name)
		}
		seenToken[tk.Key] = true
		if tk.Quota < 0 || tk.RPMLimit < 0 || tk.TPMLimit < 0 {
			return fmt.Errorf("令牌 %q 的 quota/rpm_limit/tpm_limit 不能为负数", tk.Name)
		}
	}
	return nil
}

// HasSeedData 判断 YAML 里是否带了可导入的种子数据。
func (c *Config) HasSeedData() bool {
	return len(c.Channels) > 0 || len(c.Tokens) > 0
}
