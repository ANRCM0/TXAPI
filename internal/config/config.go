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
}

// DatabaseConfig 是存储层配置。
type DatabaseConfig struct {
	// Driver 目前只支持 "sqlite"（留字段给 PostgreSQL，见 PLAN 技术栈）。
	Driver string `yaml:"driver"`
	// Path 是 SQLite 文件路径，相对路径按进程工作目录解析。
	// 默认 data/gateway.db。
	Path string `yaml:"path"`
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
	Key  string   `yaml:"key"`
	Name string   `yaml:"name"`
	// Models 限制这个令牌能访问哪些模型，空表示不限制。
	Models []string `yaml:"models"`
	// Quota 预留给 P5 计费，0 表示不限额度。
	Quota float64 `yaml:"quota"`
}

// Config 是整份配置的根结构。
type Config struct {
	Server   ServerConfig    `yaml:"server"`
	Database DatabaseConfig  `yaml:"database"`
	Channels []ChannelConfig `yaml:"channels"`
	Tokens   []TokenConfig   `yaml:"tokens"`
}

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
	cfg.applyDefaults()
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func (c *Config) applyDefaults() {
	if c.Server.Addr == "" {
		c.Server.Addr = "127.0.0.1:8080"
	}
	if c.Server.RetryTimes == 0 {
		c.Server.RetryTimes = 2
	}
	if c.Server.TimeoutSeconds == 0 {
		c.Server.TimeoutSeconds = 120
	}
	if c.Database.Driver == "" {
		c.Database.Driver = "sqlite"
	}
	if c.Database.Path == "" {
		c.Database.Path = "data/gateway.db"
	}
	for i := range c.Channels {
		ch := &c.Channels[i]
		if ch.ID == 0 {
			ch.ID = i + 1
		}
		if ch.Weight <= 0 {
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
func (c *Config) validate() error {
	seenChannel := map[int]bool{}
	for _, ch := range c.Channels {
		if seenChannel[ch.ID] {
			return fmt.Errorf("渠道 ID 重复: %d", ch.ID)
		}
		seenChannel[ch.ID] = true
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
	}
	return nil
}

// HasSeedData 判断 YAML 里是否带了可导入的种子数据。
func (c *Config) HasSeedData() bool {
	return len(c.Channels) > 0 || len(c.Tokens) > 0
}
