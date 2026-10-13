package store

import (
	"path/filepath"
	"testing"
	"time"

	"minigateway/internal/config"
	"minigateway/internal/model"
)

// newTestStore 开一个临时库。日志清理的所有断言都围绕真实 SQLite
// （分批 DELETE 的行为在真库里才成立），不用 mock。
func newTestStore(t *testing.T) *Store {
	t.Helper()
	st, err := Open(config.DatabaseConfig{
		Driver: "sqlite",
		Path:   filepath.Join(t.TempDir(), "gateway.db"),
	})
	if err != nil {
		t.Fatalf("开库失败: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// insertLog 直接插一条指定时间的日志。绕过 WriteLog 是因为异步写入器
// 会把 CreatedAt 设成 now，而测试要的正是「40 天前」。
func insertLog(t *testing.T, st *Store, at time.Time) {
	t.Helper()
	if err := st.db.Create(&model.RequestLog{
		RequestID:   "test",
		OriginModel: "gpt-4o-mini",
		Status:      200,
		CreatedAt:   at,
	}).Error; err != nil {
		t.Fatalf("插入日志失败: %v", err)
	}
}

// TestPurgeExpiredLogs 是 B4-3 的验收场景：40 天前的日志被删、近期
// 日志保留。
//
// 保留天数取 30（默认值）而不是为了好算取 40：验收要证明的是
// 「按配置的天数划线」，划错一天都该被发现。
func TestPurgeExpiredLogs(t *testing.T) {
	st := newTestStore(t)
	const retentionDays = 30
	now := time.Now()

	// 三档：远超保留期（40 天前）、刚过线（31 天前）、保留期内（29 天前）
	insertLog(t, st, now.AddDate(0, 0, -40))
	insertLog(t, st, now.AddDate(0, 0, -40))
	insertLog(t, st, now.AddDate(0, 0, -31))
	insertLog(t, st, now.AddDate(0, 0, -29))
	insertLog(t, st, now.AddDate(0, 0, -1))
	insertLog(t, st, now)

	before, err := st.CountExpiredLogs(now.AddDate(0, 0, -retentionDays))
	if err != nil {
		t.Fatalf("统计失败: %v", err)
	}
	if before != 3 {
		t.Fatalf("清理前过期行数 = %d，期望 3", before)
	}

	n, err := st.PurgeOnce(retentionDays)
	if err != nil {
		t.Fatalf("清理失败: %v", err)
	}
	if n != 3 {
		t.Errorf("删除行数 = %d，期望 3", n)
	}

	total, err := st.CountLogs()
	if err != nil {
		t.Fatalf("统计总数失败: %v", err)
	}
	if total != 3 {
		t.Errorf("剩余行数 = %d，期望 3（29天前/1天前/现在）", total)
	}
	after, err := st.CountExpiredLogs(now.AddDate(0, 0, -retentionDays))
	if err != nil {
		t.Fatalf("统计失败: %v", err)
	}
	if after != 0 {
		t.Errorf("仍有 %d 行过期日志未删", after)
	}
}

// TestPurgeBatched 验证分批删除真的分批执行，而不是一次 DELETE 全删。
//
// 这是 B4-3 的核心要求（「必须分批，避免长事务锁表」）。验证方式：
// 塞超过一个批次（1000 行）的过期日志，用 maxBatches=1 调用，
// 结果必须**只删 1000 行**——一次全删的实现会删掉 1500 行。
func TestPurgeBatched(t *testing.T) {
	st := newTestStore(t)
	old := time.Now().AddDate(0, 0, -40)
	for i := 0; i < 1500; i++ {
		insertLog(t, st, old)
	}
	// cutoff 用「现在」而不是「40 天前」：要删的是所有 created_at
	// 早于现在的行（它们全是 40 天前的），用 old 当 cutoff 会把一行
	// 都匹配不上。
	cutoff := time.Now()
	n, err := st.PurgeExpiredLogs(cutoff, 1)
	if err != nil {
		t.Fatalf("清理失败: %v", err)
	}
	if n != logRetentionBatchSize {
		t.Errorf("单批删除行数 = %d，期望 %d", n, logRetentionBatchSize)
	}
	remaining, err := st.CountLogs()
	if err != nil {
		t.Fatalf("统计失败: %v", err)
	}
	if remaining != 500 {
		t.Errorf("剩余行数 = %d，期望 500", remaining)
	}
	// 再跑一批把剩下的删干净：证明多批之间不重不漏
	n2, err := st.PurgeExpiredLogs(cutoff, 10)
	if err != nil {
		t.Fatalf("二次清理失败: %v", err)
	}
	if n2 != 500 {
		t.Errorf("第二批删除行数 = %d，期望 500", n2)
	}
}

// TestPurgeRetentionDisabled 验证 log_retention_days=0 是「关闭」而不是
// 「删全部」。
//
// 这个歧义很危险：0 如果被当成保留 0 天，一次清理会把整张日志表
// 清空——计费流水永久丢失且没有任何提示。
func TestPurgeRetentionDisabled(t *testing.T) {
	st := newTestStore(t)
	insertLog(t, st, time.Now().AddDate(0, 0, -400))
	insertLog(t, st, time.Now())

	for _, days := range []int{0, -1} {
		n, err := st.PurgeOnce(days)
		if err != nil {
			t.Fatalf("days=%d 清理报错: %v", days, err)
		}
		if n != 0 {
			t.Errorf("days=%d 删除了 %d 行，期望 0（关闭清理）", days, n)
		}
	}
	total, _ := st.CountLogs()
	if total != 2 {
		t.Errorf("剩余行数 = %d，期望 2（一行都没该删）", total)
	}
}

// TestPurgeNoExpiredRows 验证没有过期行时是干净的 no-op。
//
// 后台任务每天都会跑一次，「没有可删的」是绝大多数轮次的正常状态，
// 它必须不报错、不写库。
func TestPurgeNoExpiredRows(t *testing.T) {
	st := newTestStore(t)
	insertLog(t, st, time.Now())
	n, err := st.PurgeOnce(30)
	if err != nil {
		t.Fatalf("清理失败: %v", err)
	}
	if n != 0 {
		t.Errorf("删除行数 = %d，期望 0", n)
	}
}

// TestPurgeIdempotent 验证连续跑两轮第二轮回删 0 行。
//
// 用「实际删除行数」而不是「预估总数」作为循环终止条件，就是为了
// 让任务幂等——否则并发下（异步日志写入器一直在插新行）会永远
// 停不下来。
func TestPurgeIdempotent(t *testing.T) {
	st := newTestStore(t)
	insertLog(t, st, time.Now().AddDate(0, 0, -40))
	if _, err := st.PurgeOnce(30); err != nil {
		t.Fatalf("第一轮失败: %v", err)
	}
	n, err := st.PurgeOnce(30)
	if err != nil {
		t.Fatalf("第二轮失败: %v", err)
	}
	if n != 0 {
		t.Errorf("第二轮删除了 %d 行，期望 0", n)
	}
}
