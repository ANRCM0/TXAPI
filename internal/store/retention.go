package store

import (
	"fmt"
	"time"

	"minigateway/internal/model"
)

// logRetentionBatchSize 是单次 DELETE 的行数上限。
//
// 为什么必须分批：一次 `DELETE FROM logs WHERE created_at < ?` 在
// 攒了几个月的日志时是一个大事务——SQLite 的 WAL 下也要把整批删除
// 记进 WAL 文件，期间写锁不释放，转发路径的日志写入（每请求一条）
// 会全部撞上 "database is locked"。分批把每次持锁时间压到毫秒级，
// 批与批之间让出写锁给在途请求。
//
// 1000 是按「一次事务 <10ms」估的，不是精确值：logs 行宽小（几十
// 字节级），1000 行的删除在普通磁盘上是微秒到毫秒级。
const logRetentionBatchSize = 1000

// PurgeExpiredLogs 删除早于 cutoff 的请求日志，返回删除的总行数。
//
// 分批策略：每批用子查询取「最老的 batchSize 条」的 id 再按 id 删。
// 为什么不直接 `DELETE ... WHERE created_at < ? LIMIT n`：SQLite 的
// DELETE 不支持 LIMIT 语法（只有编译了 SQLITE_ENABLE_UPDATE_DELETE_LIMIT
// 才有，且 glebarez/sqlite 不保证），而「先 SELECT id 再 DELETE BY id」
// 在任何 SQL 方言里都成立——以后换 PostgreSQL 这段代码不用改。
//
// 循环终止条件：某一批删除 0 行。用「实际删掉的行数」而不是
// 「预估总数」判断，避免 SELECT 和 DELETE 之间又有新数据落库
// （异步日志写入器一直在跑）导致永远删不干净的死循环。
//
// 返回值用于后台任务打日志（删了多少条），也是单测的断言对象。
func (s *Store) PurgeExpiredLogs(cutoff time.Time, maxBatches int) (int, error) {
	if maxBatches <= 0 {
		maxBatches = 1
	}
	total := 0
	for i := 0; i < maxBatches; i++ {
		// 子查询取本批要删的 id。按 id 排序保证每批删的是「最老的一
		// 段」，多批之间不重不漏——created_at 在同一毫秒内可能相同，
		// 按它排序会让两批删到同一行（白跑一轮）。
		var ids []uint
		if err := s.db.Model(&model.RequestLog{}).
			Where("created_at < ?", cutoff).
			Order("id ASC").
			Limit(logRetentionBatchSize).
			Pluck("id", &ids).Error; err != nil {
			return total, fmt.Errorf("查询过期日志 id 失败: %w", err)
		}
		if len(ids) == 0 {
			return total, nil
		}
		// 按主键删：走主键索引，且删除范围已经被上一句限定成 1000 行。
		res := s.db.Where("id IN ?", ids).Delete(&model.RequestLog{})
		if res.Error != nil {
			return total, fmt.Errorf("删除过期日志失败（已删 %d 行）: %w", total, res.Error)
		}
		if res.RowsAffected == 0 {
			// 取到 id 却一行没删掉：只可能是并发下这些行刚被别处删了。
			// 继续循环没有意义（同样的 id 会再取到一次同样的空结果），
			// 直接收工。
			return total, nil
		}
		total += int(res.RowsAffected)
	}
	return total, nil
}

// CountExpiredLogs 统计早于 cutoff 的日志行数，供清理任务汇报
// 「还剩多少没删」和单测断言用。
func (s *Store) CountExpiredLogs(cutoff time.Time) (int64, error) {
	var n int64
	if err := s.db.Model(&model.RequestLog{}).
		Where("created_at < ?", cutoff).Count(&n).Error; err != nil {
		return 0, fmt.Errorf("统计过期日志数失败: %w", err)
	}
	return n, nil
}

// PurgeOnce 是后台清理任务跑一轮要做的全部事：算截止时间 → 分批删 →
// 返回本轮删了多少行。抽成导出方法（而不是在 main 里写闭包）是为了让
// 单测能直接验证「保留天数 → 截止时间 → 实际删除」这条完整链路，
// 也让 main.go 的闭包保持一行（wiring 代码不该有业务逻辑）。
//
// retentionDays <= 0 表示关闭自动清理：返回 (0, nil) 且不碰数据库。
// 0 是合法配置（见 config.LogRetentionDays），不能当成「删全部」。
func (s *Store) PurgeOnce(retentionDays int) (int, error) {
	if retentionDays <= 0 {
		return 0, nil
	}
	cutoff := time.Now().AddDate(0, 0, -retentionDays)
	// 单轮批数上限：防止 logs 表里攒了几年数据时，一个 tick 里
	// 跑几千个事务把日志写入饿死。删不掉的下一轮继续，反正每天
	// 都会跑——清理是收敛过程，不需要一轮到底。
	const maxBatchesPerRun = 200
	return s.PurgeExpiredLogs(cutoff, maxBatchesPerRun)
}
