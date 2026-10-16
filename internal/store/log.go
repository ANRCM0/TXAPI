package store

import (
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"gorm.io/gorm"

	"minigateway/internal/model"
)

// 异步日志写入器的参数。
//
//	bufferSize    channel 容量。按「峰值并发 × 批次间隔」估，自用网关 4096 足够；
//	              满时丢弃并计数，不把普通日志背压传给转发请求
//	batchSize     攒够多少条落一批。批量插入把 N 次事务合并成 1 次，
//	              SQLite 的写放大主要来自事务提交，这是异步化的主要收益
//	flushInterval 最多攒多久。防止低流量时日志卡在 channel 里迟迟不落库
const (
	logBufferSize    = 4096
	logBatchSize     = 200
	logFlushInterval = 200 * time.Millisecond
)

// LogQuery 是查询日志的过滤条件。
type LogQuery struct {
	UserID    uint
	OwnerOnly bool
	// Limit 是返回条数上限，0 表示用 DefaultLogLimit。
	Limit int
	// Offset 是分页偏移，配合 Limit 做翻页。
	Offset int
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
	if q.OwnerOnly {
		if q.UserID == 0 {
			return nil, errors.New("owner required")
		}
		db = db.Where("user_id = ?", q.UserID)
	}
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
	// 看起来「碰巧」按插入序，但删几条之后就会乱。
	// Offset 放在 Limit 之前：SQL 的 LIMIT/OFFSET 顺序固定，GORM 不关心
	// 调用顺序，但写反了可读性差。
	if err := db.Order("id DESC").Offset(q.Offset).Limit(limit).Find(&logs).Error; err != nil {
		return nil, fmt.Errorf("查询日志失败: %w", err)
	}
	return logs, nil
}

// WriteLog 落一条请求日志。
//
// 默认走异步队列（见 asyncLogWriter）；异步器未初始化时（如测试直接
// 构造 Store）退回同步写。
//
// 失败只记日志不返回错误：记账写不进去不该让客户端拿到的响应变成 500——
// 用户那边请求已经成功了，网关因为自己的账本坏了就报错是不讲道理的。
func (s *Store) WriteLog(entry *model.RequestLog) {
	if entry == nil {
		return
	}
	if s.logs != nil {
		s.logs.Write(entry)
		return
	}
	writeLogSync(s.db, entry)
}

// CountLogs 返回日志总数，用于健康检查和容量观测。
func (s *Store) CountLogs() (int64, error) {
	var n int64
	if err := s.db.Model(&model.RequestLog{}).Count(&n).Error; err != nil {
		return 0, fmt.Errorf("统计日志数失败: %w", err)
	}
	return n, nil
}

// writeLogSync is used only by isolated stores without a background writer.
func writeLogSync(db *gorm.DB, entry *model.RequestLog) bool {
	if entry.CreatedAt.IsZero() {
		entry.CreatedAt = time.Now()
	}
	if err := db.Create(entry).Error; err != nil {
		// 打到标准错误日志而不是静默：丢日志的代价是账目永久对不上，
		// 必须留痕（SPEC 2.4：异步写失败不能静默丢失）
		slog.Error("[日志写入失败] 请求日志落库失败", "request_id", entry.RequestID, "err", err)
		return false
	}
	return true
}

// writeLogBatch 批量落库。
//
// 失败时逐条重试：一条坏数据（比如某个字段超长）不该拖垮整批。
// 逐条也失败的同样打到标准错误日志——宁可吵，不能静默丢。
func writeLogBatch(db *gorm.DB, batch []*model.RequestLog) uint64 {
	if len(batch) == 0 {
		return 0
	}
	for _, e := range batch {
		if e.CreatedAt.IsZero() {
			e.CreatedAt = time.Now()
		}
	}
	if err := db.CreateInBatches(batch, logBatchSize).Error; err != nil {
		slog.Warn("[日志批量写入失败，降级逐条重试]", "count", len(batch), "err", err)
		var failures uint64
		for _, e := range batch {
			if err := db.Create(e).Error; err != nil {
				slog.Error("[日志写入失败] 请求日志落库失败", "request_id", e.RequestID, "err", err)
				failures++
			}
		}
		return failures
	}
	return 0
}

// asyncLogWriter 是日志的异步批量写入器。
//
// 为什么异步：日志写在响应返回路径上，同步写 SQLite 会让每个请求
// 多付一次事务开销（WAL 下也是 fsync 级）。并发请求时，写日志的
// 时间会叠加进客户端看到的延迟里——账本慢不该让业务慢。
//
// Ordinary access logs are best-effort, not a billing ledger. A full/closed
// queue drops and counts new records. Close drains accepted entries before the
// database closes. Failed database writes are logged and counted separately.
type asyncLogWriter struct {
	db   *gorm.DB
	ch   chan *model.RequestLog
	quit chan struct{}
	wg   sync.WaitGroup

	mu      sync.Mutex
	closed  bool
	dropped atomic.Uint64
	failed  atomic.Uint64
}

type LogQueueStats struct {
	Depth    int    `json:"depth"`
	Capacity int    `json:"capacity"`
	Dropped  uint64 `json:"dropped"`
	Failed   uint64 `json:"failed"`
}

func (s *Store) LogQueueStats() LogQueueStats {
	if s.logs == nil {
		return LogQueueStats{}
	}
	return LogQueueStats{Depth: len(s.logs.ch), Capacity: cap(s.logs.ch), Dropped: s.logs.dropped.Load(), Failed: s.logs.failed.Load()}
}

// newAsyncLogWriter 创建并启动后台写入 goroutine。
func newAsyncLogWriter(db *gorm.DB) *asyncLogWriter {
	w := &asyncLogWriter{
		db:   db,
		ch:   make(chan *model.RequestLog, logBufferSize),
		quit: make(chan struct{}),
	}
	w.wg.Add(1)
	go w.loop()
	return w
}

// Write never waits for database IO. The mutex orders enqueue against Close.
func (w *asyncLogWriter) Write(entry *model.RequestLog) {
	if entry == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		w.dropped.Add(1)
		return
	}
	copy := *entry
	select {
	case w.ch <- &copy:
	default:
		w.dropped.Add(1)
	}
}

// loop 是后台批量写入循环。
func (w *asyncLogWriter) loop() {
	defer w.wg.Done()
	batch := make([]*model.RequestLog, 0, logBatchSize)
	ticker := time.NewTicker(logFlushInterval)
	defer ticker.Stop()

	flush := func() {
		if len(batch) == 0 {
			return
		}
		w.failed.Add(writeLogBatch(w.db, batch))
		batch = batch[:0]
	}

	for {
		select {
		case entry := <-w.ch:
			batch = append(batch, entry)
			if len(batch) >= logBatchSize {
				flush()
			}
		case <-ticker.C:
			flush()
		case <-w.quit:
			// 关闭信号：先把 channel 里剩下的全部排空，再落最后一批。
			// 不能直接 return——那会让 quit 之前排队的日志永久丢失
			// （这正是「进程退出丢日志」风险的实现处）。
			for {
				select {
				case entry := <-w.ch:
					batch = append(batch, entry)
					continue
				default:
				}
				break
			}
			flush()
			return
		}
	}
}

// Close 停止后台写入并 flush 全部积压日志。
//
// 由 Store.Close 在关数据库之前调用（顺序很重要：先 flush 后关库）。
// 幂等：重复调用直接返回。
func (w *asyncLogWriter) Close() {
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return
	}
	w.closed = true
	w.mu.Unlock()
	close(w.quit)
	w.wg.Wait()
}
