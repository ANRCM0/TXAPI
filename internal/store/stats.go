package store

import (
	"fmt"
	control "minigateway/internal/foundation/contracts/control/v1"
	"time"

	"gorm.io/gorm"

	"minigateway/internal/model"
)

// 统计 API 支持的时间范围。写成常量而不是自由解析字符串：
// 统计查询走的是 GROUP BY 聚合，范围由调用方从固定集合里选，
// 自由输入除了多一层解析负担没有收益。
const (
	RangeAll = "all"
)

// LogSummary 是全局用量汇总。
type LogSummary struct {
	TotalRequests    int64 `json:"total_requests"`
	SuccessRequests  int64 `json:"success_requests"`
	ErrorRequests    int64 `json:"error_requests"`
	PromptTokens     int64 `json:"prompt_tokens"`
	CompletionTokens int64 `json:"completion_tokens"`
	TotalTokens      int64 `json:"total_tokens"`
	// TotalCost 是总费用（元）。
	TotalCost float64 `json:"total_cost"`
	// EstimatedCount 是「usage 为估算值」的条数。它不是 0 就说明
	// 有上游没返回 usage，成本数字要打折看。
	EstimatedCount int64 `json:"estimated_count"`
	// AvgLatencyMs 是平均整链耗时（含重试）。
	AvgLatencyMs float64 `json:"avg_latency_ms"`
}

// ModelStat 是按模型的用量统计。
type ModelStat struct {
	Model            string  `json:"model"`
	Requests         int64   `json:"requests"`
	SuccessRequests  int64   `json:"success_requests"`
	PromptTokens     int64   `json:"prompt_tokens"`
	CompletionTokens int64   `json:"completion_tokens"`
	TotalTokens      int64   `json:"total_tokens"`
	TotalCost        float64 `json:"total_cost"`
	EstimatedCount   int64   `json:"estimated_count"`
	AvgLatencyMs     float64 `json:"avg_latency_ms"`
}

// ChannelStat 是按渠道的用量统计。
type ChannelStat struct {
	ChannelID        uint    `json:"channel_id"`
	ChannelName      string  `json:"channel_name"`
	Requests         int64   `json:"requests"`
	SuccessRequests  int64   `json:"success_requests"`
	PromptTokens     int64   `json:"prompt_tokens"`
	CompletionTokens int64   `json:"completion_tokens"`
	TotalTokens      int64   `json:"total_tokens"`
	TotalCost        float64 `json:"total_cost"`
	AvgLatencyMs     float64 `json:"avg_latency_ms"`
}

// logScope 给统计查询加上公共的时间过滤。
// since 为零值时不加条件（range=all 的语义）。
func (s *Store) logScope(since time.Time) *gorm.DB {
	db := s.db.Model(&model.RequestLog{})
	if !since.IsZero() {
		db = db.Where("created_at >= ?", since)
	}
	return db
}

// LogSummary 汇总全局用量。
//
// status=200 算成功，其余（含 0——还没拿到响应就失败的）算失败。
// COALESCE 包住所有 SUM/AVG：空范围时它们返回 NULL，
// Go 侧扫进 float64/int64 会得到 0 值但 GORM 可能报 scan 错误，
// 显式转 0 更稳。
func (s *Store) LogSummary(since time.Time) (*LogSummary, error) {
	var out LogSummary
	err := s.logScope(since).
		Select(`COUNT(*) AS total_requests,
			COALESCE(SUM(CASE WHEN status = 200 THEN 1 ELSE 0 END), 0) AS success_requests,
			COALESCE(SUM(CASE WHEN status <> 200 THEN 1 ELSE 0 END), 0) AS error_requests,
			COALESCE(SUM(prompt_tokens), 0) AS prompt_tokens,
			COALESCE(SUM(completion_tokens), 0) AS completion_tokens,
			COALESCE(SUM(total_tokens), 0) AS total_tokens,
			COALESCE(SUM(cost), 0) AS total_cost,
			COALESCE(SUM(CASE WHEN is_estimated THEN 1 ELSE 0 END), 0) AS estimated_count,
			COALESCE(AVG(latency_ms), 0) AS avg_latency_ms`).
		Scan(&out).Error
	if err != nil {
		return nil, fmt.Errorf("统计用量汇总失败: %w", err)
	}
	return &out, nil
}

// StatsByModel 按模型分组统计，费用高的在前。
//
// 按 origin_model 分组（客户端请求的模型名）而不是 actual_model：
// 用量分析关心的是「用户点了哪个模型」，映射后的上游名是内部细节。
func (s *Store) StatsByModel(since time.Time) ([]ModelStat, error) {
	var out []ModelStat
	err := s.logScope(since).
		Select(`origin_model AS model,
			COUNT(*) AS requests,
			COALESCE(SUM(CASE WHEN status = 200 THEN 1 ELSE 0 END), 0) AS success_requests,
			COALESCE(SUM(prompt_tokens), 0) AS prompt_tokens,
			COALESCE(SUM(completion_tokens), 0) AS completion_tokens,
			COALESCE(SUM(total_tokens), 0) AS total_tokens,
			COALESCE(SUM(cost), 0) AS total_cost,
			COALESCE(SUM(CASE WHEN is_estimated THEN 1 ELSE 0 END), 0) AS estimated_count,
			COALESCE(AVG(latency_ms), 0) AS avg_latency_ms`).
		Group("origin_model").
		Order("total_cost DESC").
		Scan(&out).Error
	if err != nil {
		return nil, fmt.Errorf("按模型统计失败: %w", err)
	}
	return out, nil
}

// StatsByChannel 按渠道分组统计，费用高的在前。
//
// 渠道名从内存缓存补而不是 JOIN channels 表：channels 有软删除字段，
// JOIN 会把已删渠道的行也匹配上（或者因歧义漏匹配），而日志表只存
// channel_id。缓存是「写后即刷」的，名字映射足够新。
func (s *Store) StatsByChannel(since time.Time) ([]ChannelStat, error) {
	var out []ChannelStat
	err := s.logScope(since).
		Select(`channel_id AS channel_id,
			COUNT(*) AS requests,
			COALESCE(SUM(CASE WHEN status = 200 THEN 1 ELSE 0 END), 0) AS success_requests,
			COALESCE(SUM(prompt_tokens), 0) AS prompt_tokens,
			COALESCE(SUM(completion_tokens), 0) AS completion_tokens,
			COALESCE(SUM(total_tokens), 0) AS total_tokens,
			COALESCE(SUM(cost), 0) AS total_cost,
			COALESCE(AVG(latency_ms), 0) AS avg_latency_ms`).
		Group("channel_id").
		Order("total_cost DESC").
		Scan(&out).Error
	if err != nil {
		return nil, fmt.Errorf("按渠道统计失败: %w", err)
	}
	s.mu.RLock()
	names := make(map[uint]string, len(s.channels))
	for _, ch := range s.channels {
		names[ch.ID] = ch.Name
	}
	s.mu.RUnlock()
	for i := range out {
		out[i].ChannelName = names[out[i].ChannelID]
	}
	return out, nil
}

// ParseSince 把 range 参数（"24h"/"7d"/"30d"/"all"）解析成起始时间。
//
// 空值默认 7 天：大多数「最近用得怎么样」的问题关心的就是一周。
// "all" 返回零值时间，调用方据此跳过时间过滤。
func ParseSince(raw string, now time.Time) (time.Time, error) { return control.ParseSince(raw, now) }
