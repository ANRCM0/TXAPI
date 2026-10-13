package scheduler

import (
	"log/slog"
	"sync"
	"time"
)

// LogCleanupScan 跑一轮日志清理，返回本轮删掉的行数。
//
// 由 main 注入 store 的实现（见 store.Store.PurgeExpiredLogs 的包装）。
// 注入而不是直接依赖 store：与 StartRecovery 同一套理由——scheduler
// 是叶子包，不该知道存储层的存在。
type LogCleanupScan func() (int, error)

// CleanupInterval 是日志清理任务的执行间隔。
//
// 为什么是 24h 而不是更短：清理的收益是控制库体积，这是「天」级别
// 的缓慢变化，每小时跑一次不会让库更小，只会白白给 SQLite 加写
// 压力（清理本身也是写事务）。24h 意味着过期日志最多多留一天，
// 对排障没有任何影响。
//
// 同时它足够短到「改小保留天数当天就能看到库开始缩」——运维调完
// log_retention_days 不用等一个结算周期。
const CleanupInterval = 24 * time.Hour

// StartLogCleanup 启动后台日志清理任务，返回停止函数。
//
// 形状与 StartRecovery 完全一致（注入 scan 函数 + 返回停止函数），
// 是故意的：main.go 的优雅关闭链路就是靠这个形状串起来的——所有
// 后台任务都是「起的时候拿到 stop、关的时候 defer 它」，多一个
// 任务只是多一行 defer，不需要新的关闭机制。
//
// 为什么要这个任务：logs 表每请求一行、只增不减。SQLite 是单文件，
// 表越大每次插入（哪怕有索引）和统计扫描越慢，最终表现为「网关用
// 久了变卡」而没有任何显式故障——这类退化最难排查，定期清理是
// 唯一现实的解法。
//
// 失败只告警不中断：清理失败（比如库正忙）下一轮还会再来。为一个
// 后台维护任务搞停整个进程才是真麻烦。
func StartLogCleanup(scan LogCleanupScan, interval time.Duration) func() {
	if scan == nil {
		return func() {}
	}
	if interval <= 0 {
		interval = CleanupInterval
	}
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				cleanupOnce(scan)
			case <-stop:
				return
			}
		}
	}()
	// 返回的停止函数等 goroutine 真正退出才返回：main 随后会关数据库，
	// 不等的话清理任务可能在库已关闭后还去写它
	return func() {
		close(stop)
		wg.Wait()
	}
}

// cleanupOnce 跑一轮清理。panic 也要兜住：后台任务 panic 会直接
// 打挂整个进程（没有 Recovery 中间件保护它），而清理失败远不值
// 这个代价。
func cleanupOnce(scan LogCleanupScan) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("[日志清理] 执行时 panic", "err", r)
		}
	}()
	n, err := scan()
	if err != nil {
		slog.Warn("[日志清理] 本轮失败，下轮继续", "err", err)
		return
	}
	if n > 0 {
		// 带上删除行数：这是「库体积为什么降了」的唯一证据，
		// 也是排查「清理是不是根本没跑」的抓手。
		slog.Info("[日志清理] 已删除过期请求日志", "rows", n)
	}
}
