package store

import (
	"fmt"
	"time"

	"minigateway/internal/model"
)

// LogQuery 是查询日志的过滤条件。
type LogQuery struct {
	// Limit 是返回条数上限，0 表示用 DefaultLogLimit。
	Limit int
	// ChannelID / TokenID / Model 非零时按对应字段过滤。
	ChannelID uint
	TokenID   uint
	Model     string
}

// DefaultLogLimit 是日志查询的默认条数。
//
// 为什么必须限量：logs 表会无限增长，一个「查全部」的接口迟早把内存打爆。
// 上限 1000 是按「一屏能看完」定的，真要导出全量应该走专门的导出接口。
const (
	DefaultLogLimit = 100
	MaxLogLimit     = 1000
)

// ListLogs 按条件查日志，新的在前。
func (s *Store) ListLogs(q LogQuery) ([]model.RequestLog, error) {
	limit := q.Limit
	if limit <= 0 {
		limit = DefaultLogLimit
	}
	if limit > MaxLogLimit {
		limit = MaxLogLimit
	}
	db := s.db.Model(&model.RequestLog{})
	if q.ChannelID != 0 {
		db = db.Where("channel_id = ?", q.ChannelID)
	}
	if q.TokenID != 0 {
		db = db.Where("token_id = ?", q.TokenID)
	}
	if q.Model != "" {
		db = db.Where("origin_model = ?", q.Model)
	}
	var logs []model.RequestLog
	// 显式 ORDER BY id DESC：不写排序的话 SQLite 的返回顺序是实现相关的，
	// 看起来「碰巧」按插入序，但删几条之后就会乱
	if err := db.Order("id DESC").Limit(limit).Find(&logs).Error; err != nil {
		return nil, fmt.Errorf("查询日志失败: %w", err)
	}
	return logs, nil
}

// WriteLog 落一条请求日志。
//
// 失败只记日志不返回错误：记账写不进去不该让客户端拿到的响应变成 500——
// 用户那边请求已经成功了，网关因为自己的账本坏了就报错是不讲道理的。
func (s *Store) WriteLog(entry *model.RequestLog) {
	if entry == nil {
		return
	}
	if entry.CreatedAt.IsZero() {
		entry.CreatedAt = time.Now()
	}
	if err := s.db.Create(entry).Error; err != nil {
		fmt.Printf("[日志写入失败] request_id=%s: %v\n", entry.RequestID, err)
	}
}

// CountLogs 返回日志总数，用于健康检查和容量观测。
func (s *Store) CountLogs() (int64, error) {
	var n int64
	if err := s.db.Model(&model.RequestLog{}).Count(&n).Error; err != nil {
		return 0, fmt.Errorf("统计日志数失败: %w", err)
	}
	return n, nil
}
