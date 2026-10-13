package model

import (
	"time"
)

// RequestLog 是每次转发的记账流水。
//
// 这张表是计费、排障、用量统计的唯一事实来源。设计要点：
//   - 同时记录 channel_id 和 credential_id，才能回答「哪个 Key 出的问题」
//   - 记录 OriginModel 和 ActualModel，才能发现「模型映射配错了」
//   - latency_ms 分开记录首字延迟和总延迟的余地（P5 再拆）
type RequestLog struct {
	ID           uint   `gorm:"primaryKey" json:"id"`
	RequestID    string `gorm:"size:64;index" json:"request_id"`
	UserID       uint   `gorm:"index" json:"user_id"`
	TokenID      uint   `gorm:"index" json:"token_id"`
	ChannelID    uint   `gorm:"index" json:"channel_id"`
	CredentialID uint   `gorm:"index" json:"credential_id"`
	// OriginModel 是客户端请求的模型名，ActualModel 是映射后发给上游的名字。
	OriginModel string `gorm:"size:128;index" json:"origin_model"`
	ActualModel string `gorm:"size:128" json:"actual_model"`
	IsStream    bool   `gorm:"not null;default:false" json:"is_stream"`

	PromptTokens     int `gorm:"not null;default:0" json:"prompt_tokens"`
	CompletionTokens int `gorm:"not null;default:0" json:"completion_tokens"`
	TotalTokens      int `gorm:"not null;default:0" json:"total_tokens"`
	// Cost 是本次请求的费用（元），由 P5 的价格表算出。
	Cost float64 `gorm:"not null;default:0" json:"cost"`
	// LatencyMs 是整条链路耗时（含重试），不是单次上游耗时。
	LatencyMs int64 `gorm:"not null;default:0" json:"latency_ms"`

	// Status 是 HTTP 状态码；0 表示「还没拿到响应就失败了」（网络错误等）。
	Status       int    `gorm:"not null;default:0" json:"status"`
	ErrorMessage string `gorm:"type:text" json:"error_message"`

	// IsEstimated 标记 usage 是否为估算值——上游没返回 usage 时按
	// 字符数降级估算（SPEC 2.3）。true 时 token 数和 cost 都要打折看，
	// 统计接口会把这类条数单独汇总出来。
	IsEstimated bool `gorm:"not null;default:false" json:"is_estimated"`
	// PriceMissing 标记价格表里没有这个模型的单价（SPEC 4.5）：
	// 按 0 计费但正常服务。它不是 0 就说明成本数字漏了一部分，
	// 该去补价格表了。
	PriceMissing bool `gorm:"not null;default:false" json:"price_missing"`

	CreatedAt time.Time `gorm:"index" json:"created_at"`
}

// TableName 固定表名为 logs。GORM 的默认复数化会把 Log 变成 logs，
// 这里显式声明，和 PLAN 的表结构逐字对齐。
func (RequestLog) TableName() string { return "logs" }

// ModelPrice 是模型价格表，计费的输入。
type ModelPrice struct {
	// Model 是上游真实模型名（不是客户端请求名）。
	Model string `gorm:"primaryKey;size:128" json:"model"`
	// 单价单位：元 / 1K tokens。
	InputPricePer1K  float64 `gorm:"not null;default:0" json:"input_price_per_1k"`
	OutputPricePer1K float64 `gorm:"not null;default:0" json:"output_price_per_1k"`

	UpdatedAt time.Time `json:"updated_at"`
}

// TableName 固定表名。
func (ModelPrice) TableName() string { return "model_prices" }

// Setting 是键值对形式的系统设置。
//
// 为什么用 KV 而不是加字段：这里放的是「很少读、偶尔改」的开关
// （比如默认分组、是否开启计费），为它们改表结构代价太大。
type Setting struct {
	Key   string `gorm:"primaryKey;size:128" json:"key"`
	Value string `gorm:"type:text;not null;default:''" json:"value"`
}

// TableName 固定表名。
func (Setting) TableName() string { return "settings" }
