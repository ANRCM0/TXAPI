// Package store 是数据访问层：SQLite（默认）+ GORM + 内存缓存。
//
// 分层职责：store 之上所有代码都不直接碰 gorm.DB，全部走这里的 CRUD。
// 这样以后要换 PostgreSQL（PLAN 里的可选项）只需要改 Open()，业务代码零改动。
//
// 缓存策略（PLAN 明确「别一上来就上 Redis」）：
//   - 渠道表 + 令牌表读多写极少，全量放进内存，靠 sync.RWMutex 保证并发安全
//   - 转发路径上一次内存命中就能拿到渠道和 Key，不产生任何 SQLite 读
//   - 任何写操作都在同一把写锁内先落库、再刷新缓存，避免「库里改了内存没改」
package store

import (
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"minigateway/internal/access"
	"minigateway/internal/config"
	"minigateway/internal/model"
)

// middlewareTokenEntry 已废弃：令牌投影改用 model.TokenView，避免 store 包
// 为了对接中间件而复制一份结构体。

// Store 聚合数据库连接和内存缓存。
type Store struct {
	db *gorm.DB

	// logs 是请求日志的异步批量写入器。由 Open 创建、Close 先 flush。
	// 指针类型是为了让「不开异步」的构造路径（测试里直接造 Store）可用。
	logs *asyncLogWriter

	mu       sync.RWMutex
	cacheMu  sync.Mutex
	channels []model.Channel
	tokens   map[string]*model.Token
}

// Open 打开数据库并建好缓存。
//
// 为什么 SQLite 要显式开 foreign_keys：SQLite 默认**不**强制执行外键约束
// （历史兼容原因），不打开的话 channels 删了、credentials 还留着。
// busy_timeout 是为了并发写时等锁而不是立刻报 "database is locked"。
func Open(cfg config.DatabaseConfig) (*Store, error) {
	driver := cfg.Driver
	if driver == "" {
		driver = "sqlite"
	}
	path := cfg.Path
	if path == "" {
		path = "data/gateway.db"
	}
	if driver != "sqlite" {
		return nil, fmt.Errorf("P2 阶段只实现了 sqlite 驱动，收到 %q", driver)
	}

	dsn := fmt.Sprintf("file:%s?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)", path)
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		// 默认 Warn 级别：SQL 全打出来会淹没业务日志，但慢查询/错误要留痕。
		Logger: logger.Default.LogMode(logger.Warn),
	})
	if err != nil {
		return nil, fmt.Errorf("打开数据库 %s 失败: %w", path, err)
	}
	// SQLite 单写者，连接池开大了只是互相抢锁
	sqlDB, err := db.DB()
	if err != nil {
		return nil, fmt.Errorf("获取底层连接池失败: %w", err)
	}
	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetMaxIdleConns(1)
	sqlDB.SetConnMaxLifetime(time.Hour)

	s := &Store{db: db}
	if err := s.AutoMigrate(); err != nil {
		_ = sqlDB.Close()
		return nil, err
	}
	// 日志异步写入器：建好表之后再启动，保证第一批日志就能落库
	s.logs = newAsyncLogWriter(db)
	if err := s.reloadCache(); err != nil {
		return nil, err
	}
	return s, nil
}

// AutoMigrate builds structural columns, then applies the versioned access data migration.
//
// 用 AutoMigrate 而不是手写 SQL 迁移：自用场景下表结构的演进靠它足够，
// 且能保证 Go 结构体和表结构不漂移。生产上换 PG 后再引入版本化迁移工具。
func (s *Store) AutoMigrate() error {
	if err := checkMigrationVersion(s.db, latestMigrationVersion); err != nil {
		return err
	}
	if err := s.db.AutoMigrate(
		&model.User{},
		&model.Session{},
		&model.SchemaMigration{},
		&model.Channel{},
		&model.Credential{},
		&model.Token{},
		&model.Ability{},
		&model.RequestLog{},
		&model.ModelPrice{},
		&model.BillingPriceVersion{},
		&model.BillingBalance{},
		&model.BillingReservation{},
		&model.BillingAttempt{},
		&model.BillingResolution{},
		&model.GenerationRecord{},
		&model.GenerationSegment{},
		&model.GenerationIntent{},
		&model.Setting{},
	); err != nil {
		return fmt.Errorf("建表失败: %w", err)
	}
	return applyDataMigrations(s.db, dataMigrations)
}

// Close 先 flush 异步日志再关闭连接池。
//
// 正常关闭时 main 先 srv.Shutdown，再排空已接收日志后关库。
// 队列满/关闭时的新记录与最终写库失败均可能丢失，分别计数。
func (s *Store) Close() error {
	if s.logs != nil {
		s.logs.Close()
	}
	sqlDB, err := s.db.DB()
	if err != nil {
		return err
	}
	return sqlDB.Close()
}

// DB 暴露底层 gorm 句柄，只给「确实需要拼复杂查询」的场景用（比如日志统计）。
// 常规 CRUD 一律走本包的方法，不要绕过缓存。
func (s *Store) DB() *gorm.DB { return s.db }

// ErrNotFound 供调用方用 errors.Is 判断「记录不存在」。
var ErrNotFound = errors.New("记录不存在")

// reloadCache 从数据库全量重读渠道和令牌，重建内存缓存。
//
// 为什么是「全量重读」而不是增量更新：渠道/令牌总量在自用场景下是个位到百位，
// 全量重读一次是微秒级；而增量更新要维护复杂的失效逻辑，容易漏。
// 写多读多的场景才值得上增量。
func (s *Store) reloadCache() error {
	s.cacheMu.Lock()
	defer s.cacheMu.Unlock()
	var channels []model.Channel
	// Preload 凭据池：一次 JOIN 拿全，避免 N+1 查询
	if err := s.db.Preload("Credentials", func(db *gorm.DB) *gorm.DB {
		return db.Order("weight DESC, id ASC")
	}).Order("priority DESC, id ASC").Find(&channels).Error; err != nil {
		return fmt.Errorf("读取渠道缓存失败: %w", err)
	}

	var tokens []model.Token
	if err := s.db.Order("id ASC").Find(&tokens).Error; err != nil {
		return fmt.Errorf("读取令牌缓存失败: %w", err)
	}

	tokenMap := make(map[string]*model.Token, len(tokens))
	for i := range tokens {
		tokenMap[tokens[i].KeyHash] = &tokens[i]
	}

	s.mu.Lock()
	s.channels = channels
	s.tokens = tokenMap
	s.mu.Unlock()
	return nil
}

// Channels 返回全部渠道（含凭据池）的只读快照。
//
// 返回的是切片头副本，元素本身共享底层数组——调用方**只读**。
// 这是有意的：转发路径每请求都调它，深拷贝的分配会成为热点。
func (s *Store) Channels() []model.Channel {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.channels
}

// LookupToken 按 key 查令牌，实现 middleware.TokenLookup。
//
// 只返回「当前可用」的令牌（启用且未过期）：过期令牌在这里就过滤掉，
// 上层不用每个调用点都记得判过期。
func (s *Store) LookupToken(key string) (model.TokenView, bool) {
	view, err := resolveTokenView(s.db.Where("key_hash = ?", access.Hash(key)))
	return view, err == nil
}
