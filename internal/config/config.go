// Package config 负责把 YAML 配置文件读进内存，并做默认值填充和校验。
//
// 设计原则：配置在启动时一次性加载完，运行期不再读盘。
// 这样转发路径上没有任何文件 IO，也不会因为配置文件被改动导致行为漂移。
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

// ChannelConfig 描述一个上游渠道（一个 API Key + 一个地址）。
type ChannelConfig struct {
	ID      int    `yaml:"id"`
	Name    string `yaml:"name"`
	Type    string `yaml:"type"`
	BaseURL string `yaml:"base_url"`
	Key     string `yaml:"key"`
	// Models 是这个渠道支持的模型列表，支持通配符 "*"。
	// 路由时就靠它判断「这个模型该走哪些渠道」。
	Models []string `yaml:"models"`
	// ModelMapping 把客户端请求的模型名映射成上游真实模型名。
	// 例如 {"gpt-4o": "gpt-4o-2024-08-06"}，不改客户端就能锁定版本。
	ModelMapping map[string]string `yaml:"model_mapping"`
	// Weight 是加权随机的权重，越大被选中概率越高。
	Weight int `yaml:"weight"`
	// Enabled 用指针是为了区分「没写」和「写了 false」。
	Enabled *bool `yaml:"enabled"`
}

// IsEnabled 在没配置 enabled 时默认视为启用。
func (c *ChannelConfig) IsEnabled() bool { return c.Enabled == nil || *c.Enabled }

// TokenConfig 是调用方（客户端）用来访问网关的凭证。
// 注意：渠道的 Key 是给上游的，令牌的 Key 是给客户端的，两者完全不同。
type TokenConfig struct {
	Key  string `yaml:"key"`
	Name string `yaml:"name"`
	// Models 限制这个令牌能访问哪些模型，空表示不限制。
	Models []string `yaml:"models"`
	// Quota 预留给后续计费，0 表示不限额度。
	Quota float64 `yaml:"quota"`
}

// Config 是整份配置的根结构。
type Config struct {
	Server   ServerConfig    `yaml:"server"`
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
// 宁可启动失败，也不要等到线上第一个请求打进来才发现 key 是空的。
func (c *Config) validate() error {
	if len(c.Channels) == 0 {
		return fmt.Errorf("配置里至少要有一个渠道")
	}
	if len(c.Tokens) == 0 {
		return fmt.Errorf("配置里至少要有一个令牌")
	}
	seenChannel := map[int]bool{}
	for _, ch := range c.Channels {
		if seenChannel[ch.ID] {
			return fmt.Errorf("渠道 ID 重复: %d", ch.ID)
		}
		seenChannel[ch.ID] = true
		if ch.BaseURL == "" {
			return fmt.Errorf("渠道 %d(%s) 缺少 base_url", ch.ID, ch.Name)
		}
		if ch.Key == "" {
			return fmt.Errorf("渠道 %d(%s) 缺少 key", ch.ID, ch.Name)
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
