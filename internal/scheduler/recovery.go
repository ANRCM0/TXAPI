package scheduler

import (
	"log/slog"
	"sync"
	"time"
)

// RecoveryScan 扫描并恢复冷却到期的凭据，返回本轮恢复了几把。
//
// 由 main 注入 store 的实现（见 store.RecoverCooledDownCredentials）。
// 注入而不是直接调 store：scheduler 是叶子包，不该知道存储层的存在。
type RecoveryScan func() (int, error)

// RecoveryInterval 是后台扫描的间隔。
//
// 默认 30s：冷却最短也是 CooldownBase（30s），扫得比冷却粒度更频没有
// 意义——只会白白给 SQLite 加读压力。30s 意味着凭据最多在到期后 30s
// 内被恢复，对「间歇性故障的 Key」这个场景完全够用。
const RecoveryInterval = 30 * time.Second

// StartRecovery 启动后台恢复任务，返回停止函数。
//
// 恢复什么：状态为 cooldown 且 cooldown_until 已到期的凭据 → active。
// 不恢复什么：banned。拉黑是「这把 Key 反复坏」的判定，必须人工确认
// 修好了再放行（管理接口 POST /api/credentials/:id/status）——自动恢复
// 一把反复坏的 Key，只会让它继续占用流量份额，把故障期拉长。
//
// 恢复时保留 fail_count 不清零：退避曲线要连续，刚恢复的 Key 再失败
// 应该直接进入更长的冷却，而不是从头再来一遍 30s。fail_count 的清零发生
// 在下次成功转发时（见 RecordSuccess），那时才真的确认这把 Key 是好的。
//
// 为什么要这个任务：没有它，池子会单向枯竭。每把 Key 只要失败够多次
// 就永远停在 cooldown，最终所有渠道都选不出凭据，网关表现为「没有可用
// 渠道」——而运维看到的是「Key 都是好的啊」，因为故障早就过去了。
func StartRecovery(scan RecoveryScan, interval time.Duration) func() {
	if scan == nil {
		return func() {}
	}
	if interval <= 0 {
		interval = RecoveryInterval
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
				recoverOnce(scan)
			case <-stop:
				return
			}
		}
	}()
	// 返回的停止函数等 goroutine 真正退出才返回：main 的优雅关闭链路
	// 随后会关数据库，不等的话恢复任务可能在库已关闭后还去读它
	return func() {
		close(stop)
		wg.Wait()
	}
}

// recoverOnce 跑一轮恢复。失败只告警不中断：扫描失败（比如库正忙）
// 下一轮还会再来，让一个瞬时故障把整个后台任务搞停才是真麻烦。
func recoverOnce(scan RecoveryScan) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("[凭据恢复] 扫描时 panic", "err", r)
		}
	}()
	n, err := scan()
	if err != nil {
		slog.Warn("[凭据恢复] 扫描失败", "err", err)
		return
	}
	if n > 0 {
		slog.Info("[凭据恢复] 冷却到期凭据已恢复", "count", n)
	}
}
