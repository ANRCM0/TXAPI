package scheduler

import (
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

// TestStartLogCleanupRuns 验证任务按间隔执行、且传入的 scan 被真的
// 调到。
//
// 间隔用 10ms 而不是 24h：测的是「定时触发 + 停止」这个机制，
// 等的就是 ticker 响一次。真实间隔（CleanupInterval）的正确性由
// 「它是 24h」这个常量本身保证，不需要在测试里等一天。
func TestStartLogCleanupRuns(t *testing.T) {
	var calls int64
	var lastDeleted int64
	stop := StartLogCleanup(func() (int, error) {
		atomic.AddInt64(&calls, 1)
		atomic.StoreInt64(&lastDeleted, 5)
		return 5, nil
	}, 10*time.Millisecond)

	deadline := time.Now().Add(2 * time.Second)
	for atomic.LoadInt64(&calls) == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if atomic.LoadInt64(&calls) == 0 {
		t.Fatal("2 秒内清理任务一次都没执行")
	}
	stop()
}

// TestStartLogCleanupStop 验证停止函数能让 goroutine 真正退出。
//
// 为什么必须验证「真的退出」而不只是「stop 返回了」：main 的关闭
// 链路是 srv.Shutdown → stopLogCleanup → … → st.Close()。停止函数
// 如果不等 goroutine 退出就返回，清理任务会在数据库已关闭后还去
// 写它——那正是 recovery.go 的停止函数要 wg.Wait() 的原因，这里
// 必须保持一致。
func TestStartLogCleanupStop(t *testing.T) {
	var running int64
	stop := StartLogCleanup(func() (int, error) {
		atomic.AddInt64(&running, 1)
		time.Sleep(50 * time.Millisecond) // 模拟一次慢清理
		return 1, nil
	}, 10*time.Millisecond)

	// 等它至少跑过一轮，确保 goroutine 已经起来并在循环里
	deadline := time.Now().Add(2 * time.Second)
	for atomic.LoadInt64(&running) == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if atomic.LoadInt64(&running) == 0 {
		t.Fatal("任务从未执行")
	}
	// stop 必须能返回（wg.Wait 不会死等）：卡住会让 main 的优雅关闭
	// 永远走不到 st.Close，进程挂死
	done := make(chan struct{})
	go func() { stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("停止函数 3 秒未返回，goroutine 可能没退出")
	}
	// 停之后不应再有新调用（留 100ms 给可能正在飞的 tick）
	before := atomic.LoadInt64(&running)
	time.Sleep(100 * time.Millisecond)
	if after := atomic.LoadInt64(&running); after != before {
		t.Errorf("停止后仍在执行清理：停止前 %d 次，之后 %d 次", before, after)
	}
}

// TestStartLogCleanupErrorDoesNotStop 验证清理失败不会中断任务。
//
// 失败只告警：库正忙是常态（转发路径一直在写），为一次瞬时失败把
// 整个后台维护任务搞停才是真麻烦——下一轮还会再来。
func TestStartLogCleanupErrorDoesNotStop(t *testing.T) {
	var ok int64
	stop := StartLogCleanup(func() (int, error) {
		if atomic.AddInt64(&ok, 1) == 1 {
			return 0, errForTest
		}
		return 1, nil
	}, 10*time.Millisecond)
	defer stop()
	deadline := time.Now().Add(2 * time.Second)
	for atomic.LoadInt64(&ok) < 2 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if n := atomic.LoadInt64(&ok); n < 2 {
		t.Fatalf("首轮失败后任务停止了，只执行了 %d 轮", n)
	}
}

// TestStartLogCleanupNilScan 验证未注入实现时返回的停止函数可安全调用。
//
// main 的 defer 链是无条件执行的，stop 为 nil 会直接 panic 在关闭路径上。
func TestStartLogCleanupNilScan(t *testing.T) {
	stop := StartLogCleanup(nil, time.Second)
	stop() // 不应 panic
}

// TestStartLogCleanupSurvivesPanic 验证 scan panic 不会打挂进程。
//
// 后台 goroutine 的 panic 没有 Recovery 中间件兜底，会直接终止进程；
// 而清理失败的代价只是「这轮没删」，远不值一个进程。
func TestStartLogCleanupSurvivesPanic(t *testing.T) {
	var afterPanic int64
	stop := StartLogCleanup(func() (int, error) {
		if atomic.AddInt64(&afterPanic, 1) == 1 {
			panic("模拟清理崩溃")
		}
		return 1, nil
	}, 10*time.Millisecond)
	defer stop()
	// 进程没崩 + 第二轮执行成功 = panic 被兜住且循环继续
	deadline := time.Now().Add(2 * time.Second)
	for atomic.LoadInt64(&afterPanic) < 2 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if n := atomic.LoadInt64(&afterPanic); n < 2 {
		t.Fatalf("panic 后任务没继续，只执行了 %d 轮", n)
	}
}

// TestCleanupIntervalDefault 验证间隔为 0 时回落默认值而不是
// 「立刻无限循环」。
//
// interval <= 0 传给 time.NewTicker 会 panic，所以回落是必需的分支。
func TestCleanupIntervalDefault(t *testing.T) {
	var calls int64
	stop := StartLogCleanup(func() (int, error) {
		atomic.AddInt64(&calls, 1)
		return 0, nil
	}, 0)
	defer stop()
	// 默认 24h，2 秒内不该触发
	time.Sleep(200 * time.Millisecond)
	if n := atomic.LoadInt64(&calls); n != 0 {
		t.Errorf("间隔回落失败：2 秒内执行了 %d 轮", n)
	}
}

// TestCleanupIntervalValue 固定默认间隔为 24h。
//
// 写成断言而不是只读常量：这个值是「为什么每天而不是每小时」这个
// 决策的落点（见 CleanupInterval 的注释），改它应该是主动决定。
func TestCleanupIntervalValue(t *testing.T) {
	if CleanupInterval != 24*time.Hour {
		t.Errorf("CleanupInterval = %v，期望 24h", CleanupInterval)
	}
}

// errForTest 是供测试注入的固定错误值。
var errForTest = errors.New("模拟清理失败")
