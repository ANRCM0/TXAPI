package scheduler

import (
	"log/slog"
	"net/http"
	"sync"
	"time"

	dbmodel "minigateway/internal/model"
)

// 健康状态机的可调参数。
//
// 抽成常量而不是散落的魔法数：退避基数、封顶值、拉黑阈值三者是联动的
// （阈值 5 × 基数 30s ≈ 最坏 5 次失败后被拉黑，期间累计冷却约 8 分钟），
// 放一起才能看清整套退避曲线的形状。
const (
	// BanThreshold 是连续失败多少次后拉黑。默认 5：一次网络抖动不至于
	// 拉黑（否则一把 Key 会被偶发的 502 永久废掉），而持续故障在 5 次内
	// 就会被摘掉（默认重试 2 次的场景下约 2-3 个请求就能触发）。
	BanThreshold = 5

	// CooldownBase 是指数退避的基数。第一次失败冷却 30s，之后每次翻倍。
	CooldownBase = 30 * time.Second

	// CooldownMax 是冷却上限（封顶）。不封顶的话 fail_count 到 20 就是
	// 30s × 2^19 ≈ 180 天，等于永久拉黑却还占着 cooldown 状态——
	// 排查时看到「在冷却」会以为很快回来，实际永远回不来。
	CooldownMax = time.Hour
)

// ShouldPenalizeCredential 判断这次失败该不该记在凭据头上。
//
// 这是整个健康状态机里最容易写错的一环。分类依据是「谁有能力修好它」：
//
//	该罚（凭据/上游的问题，换一把 Key 很可能就好）：
//	  401   Key 无效/已撤销      —— 这把 Key 自己的问题
//	  403   权限不足             —— 同上，账号侧被限
//	  429   限流                 —— 这把 Key 的配额用满了
//	  408   超时                 —— 上游没响应，和 5xx 同类
//	  5xx   上游故障             —— 该上游整体不健康（网络层错误映射成 502）
//
//	不该罚（客户端的问题，换任何一把 Key 都一样错）：
//	  400   参数错误             —— 用户的 JSON 缺字段/类型不对
//	  404   模型不存在           —— 请求了一个上游没有的模型
//	  422   语义错误             —— 参数合法但业务上不成立
//
// 为什么必须区分：把用户的参数错误算到凭据头上，会让一把好 Key 因为几个
// 畸形请求就被拖进冷却、进而被拉黑。表现是「网关突然说没有可用渠道」，
// 而真正的病因在客户端——这类故障极难排查，因为凭据本身根本没坏。
func ShouldPenalizeCredential(statusCode int) bool {
	switch statusCode {
	case http.StatusBadRequest, // 400 参数错误
		http.StatusNotFound,            // 404 模型不存在
		http.StatusUnprocessableEntity: // 422 语义错误
		return false
	}
	// 其余一律算凭据的锅：401/403/429/408 和所有 5xx 都在这里。
	// 网络层错误（连不上、TLS 失败）在 doRelay 里已被映射成 502，
	// 所以不需要在这里特判「没有状态码」的情况。
	return true
}

// HealthUpdate 是一次健康状态变更的快照。
type HealthUpdate struct {
	CredentialID uint
	FailCount    int
	// CooldownUntil 是冷却截止时间，Unix 秒；0 表示不在冷却中。
	CooldownUntil int64
	Status        string
	LastError     string
	LastUsedAt    int64
}

// Changed 判断这次更新是否真的改变了调度行为。
//
// 用来挡掉「每个成功请求都写一次库」：一把本来就全绿的凭据，成功一次
// 只有 last_used_at 会变，而 last_used_at 纯属排查用的时间戳，不值得
// 为它每个请求付一次 SQLite 事务。只在状态真的迁移时才落库 + 重载。
func (u HealthUpdate) Changed(from *dbmodel.Credential) bool {
	return u.Status != from.Status ||
		u.FailCount != from.FailCount ||
		u.CooldownUntil != from.CooldownUntil
}

// NextSuccess 算出「这次成功之后」凭据应该处于什么状态（纯函数）。
//
// fail_count / cooldown_until 一并清零而不是只改 status：只改 status
// 会留下「状态是 active 但 fail_count 还是 4」的凭据，下次一失败就直接
// 跳到拉黑阈值——「解封了但没真解封」是最难排查的一类故障。
func NextSuccess(cred *dbmodel.Credential, now time.Time) HealthUpdate {
	return HealthUpdate{
		CredentialID:  cred.ID,
		FailCount:     0,
		CooldownUntil: 0,
		Status:        dbmodel.CredentialActive,
		LastUsedAt:    now.Unix(),
	}
}

// NextFailure 算出「这次失败之后」凭据应该处于什么状态（纯函数）。
//
// statusCode 不该罚时（见 ShouldPenalizeCredential）只记 last_error，
// 健康状态原样不动。返回的 HealthUpdate.Changed 会因此为 false，
// 调用方据此跳过落库。
func NextFailure(cred *dbmodel.Credential, statusCode int, errMsg string, now time.Time) HealthUpdate {
	next := HealthUpdate{
		CredentialID:  cred.ID,
		FailCount:     cred.FailCount,
		CooldownUntil: cred.CooldownUntil,
		Status:        cred.Status,
		LastError:     errMsg,
		LastUsedAt:    now.Unix(),
	}
	if !ShouldPenalizeCredential(statusCode) {
		return next
	}
	next.FailCount = cred.FailCount + 1
	if next.FailCount >= BanThreshold {
		// 连续失败到阈值：拉黑。banned 不自动恢复（见 recovery.go），
		// 必须人工确认 Key 修好了再放行——自动恢复一把反复坏的 Key
		// 只会让它继续污染流量份额。
		next.Status = dbmodel.CredentialBanned
		next.CooldownUntil = 0
	} else {
		// 指数退避：第 n 次失败冷却 base × 2^(n-1)，即 30s/60s/120s/240s…
		// 封顶在 CooldownMax。给故障留出自愈的时间窗，又不让一把间歇性
		// 坏的 Key 长期占用流量份额。
		next.Status = dbmodel.CredentialCooldown
		next.CooldownUntil = now.Add(backoff(next.FailCount)).Unix()
	}
	return next
}

// RecordSuccess 记录一次成功转发，把凭据状态重置回全绿。
func RecordSuccess(cred *dbmodel.Credential, now time.Time) {
	if cred == nil {
		return
	}
	submit(NextSuccess(cred, now), cred)
}

// RecordFailure 记录一次失败转发，按状态码决定是否惩罚这把凭据。
//
// statusCode 不该罚时只更新 last_error，不动健康状态。
func RecordFailure(cred *dbmodel.Credential, statusCode int, errMsg string, now time.Time) {
	if cred == nil {
		return
	}
	next := NextFailure(cred, statusCode, errMsg, now)
	if next.Changed(cred) {
		// debug 而不是 info：状态迁移在故障期会非常密集（每个失败
		// 请求一条），info 级别会把请求摘要冲掉；但它又是排查
		// 「这把 Key 为什么被冷却」的唯一现场，所以不能没有。
		slog.Debug("[凭据健康] 凭据失败，状态迁移",
			"credential_id", cred.ID,
			"status_code", statusCode,
			"fail_count", next.FailCount,
			"next_status", next.Status,
			"cooldown_until", time.Unix(next.CooldownUntil, 0).Format(time.RFC3339),
			"error", errMsg)
	} else {
		slog.Debug("[凭据健康] 凭据失败但属客户端错误，不惩罚",
			"credential_id", cred.ID, "status_code", statusCode, "error", errMsg)
	}
	submit(next, cred)
}

// backoff 算第 n 次失败（n 从 1 开始）对应的冷却时长。
//
// 单独抽出来是为了单测能直接验证退避曲线（1×2×4×8…且封顶），
// 不必绕道 RecordFailure。
func backoff(failCount int) time.Duration {
	if failCount < 1 {
		failCount = 1
	}
	d := CooldownBase
	for i := 1; i < failCount; i++ {
		d *= 2
		if d >= CooldownMax {
			return CooldownMax
		}
	}
	if d > CooldownMax {
		return CooldownMax
	}
	return d
}

// 状态变更的异步落库通道。
//
// 为什么要异步 + 为什么要单 worker 串行：
//   - 异步：状态写在响应返回路径上，同步写 SQLite 会让每个失败请求多付
//     一次事务开销（WAL 下也是 fsync 级），拖慢的是客户端的响应；
//   - 串行：SQLite 是单写者（store 把连接池压到 1），并发写只会互相抢
//     锁；而且串行能让「落库 → 重载渠道池」严格按序发生，不会出现
//     后写的先重载、把新状态覆盖回旧值。
//
// 队列满时丢弃并告警：健康状态是优化手段不是正确性依赖，丢一条的后果
// 是这把 Key 的冷却晚几秒生效，远好过让转发路径阻塞。
const healthQueueSize = 256

var (
	healthCh chan HealthUpdate
	stopCh   chan struct{}
	wg       sync.WaitGroup

	persistMu  sync.RWMutex
	persister  HealthPersister
	reloadPool func()
)

// HealthPersister 把凭据的新健康状态写进存储层。
//
// 用函数变量而不是直接依赖 store：scheduler 是叶子包，不该知道存储层的
// 存在（和 relay.SetLogRecorder 同一套路）。由 main 注入
// store.UpdateCredentialHealth。未注入时（单元测试）所有状态变更只算不落。
//
// 返回 error 是为了让落库失败能被看见（worker 里会告警）。健康状态是
// 优化手段不是正确性依赖，所以失败不重试、不阻塞、不影响响应——
// 但绝不能静默：连续失败意味着整个调度状态机在空转，必须留痕。
type HealthPersister func(u HealthUpdate) error

// SetHealthPersister 注入健康状态的持久化实现，并启动后台 worker。
//
// 幂等：重复注入后者覆盖前者（测试里换 persister 的场景）。
func SetHealthPersister(fn HealthPersister) {
	persistMu.Lock()
	persister = fn
	started := healthCh != nil
	persistMu.Unlock()
	if started || fn == nil {
		return
	}
	healthCh = make(chan HealthUpdate, healthQueueSize)
	stopCh = make(chan struct{})
	wg.Add(1)
	go worker()
}

// SetPoolReloader 注入「状态变更后重载渠道池」的回调。
//
// 为什么需要：调度器改的是数据库，转发层读的是内存里的渠道快照。
// 不重载的话会出现「库里已经 cooldown、转发还在用它」的窗口——
// 那个窗口里的每个请求都会重复踩同一个坏 Key。
func SetPoolReloader(fn func()) {
	persistMu.Lock()
	defer persistMu.Unlock()
	reloadPool = fn
}

// submit 把一次状态变更交给后台 worker。
//
// 刻意**不**修改调用方传入的 cred：relay 持有的凭据快照是多请求共享
// 只读数据，在这里原地改字段会和并发读撞成 data race（-race 必挂）。
// 新状态统一走「落库 → 重载渠道池」这条路回到转发层，重载本身在
// registry 的写锁内完成，是安全的发布点。
func submit(next HealthUpdate, from *dbmodel.Credential) {
	if !next.Changed(from) {
		// 没有任何调度行为变化（常见于「本来就全绿的凭据又成功了一次」
		// 和「不该罚的失败」）：不落库、不重载。last_used_at 的更新
		// 顺延到下次真的有状态迁移时一起写。
		return
	}
	persistMu.RLock()
	ch := healthCh
	persistMu.RUnlock()
	if ch == nil {
		// 没注入 persister（单元测试）：纯内存算完即止
		return
	}
	select {
	case ch <- next:
	default:
		// 队列满：丢弃 + 告警。见 healthQueueSize 的说明。
		slog.Warn("[凭据健康] 状态队列已满，丢弃一次状态更新", "credential_id", next.CredentialID)
	}
}

// worker 串行处理状态变更：落库 → 重载渠道池。
//
// 批量合并重载：一次排空 channel 里积压的全部更新再重载一次，而不是每条
// 都重载。失败风暴时（比如整条上游挂了、每请求都失败）会把重载次数从
// 「失败次数」压到「worker 轮次」，避免 reloadCache 的全表 Preload 被
// 反复触发。
func worker() {
	defer wg.Done()
	for {
		select {
		case u := <-healthCh:
			batch := []HealthUpdate{u}
			// 非阻塞排空：只合并「已经等在队列里」的，不为合并而等待
			for len(batch) < 64 {
				select {
				case more := <-healthCh:
					batch = append(batch, more)
					continue
				default:
				}
				break
			}
			flush(batch)
		case <-stopCh:
			// 退出前排空积压：进程关闭时最后一批状态变更必须落库，
			// 否则「刚被拉黑的 Key 重启后又变回 active」，故障会复现
			for {
				select {
				case u := <-healthCh:
					flush([]HealthUpdate{u})
					continue
				default:
				}
				return
			}
		}
	}
}

// flush 落一批状态变更，然后重载一次渠道池。
func flush(batch []HealthUpdate) {
	persistMu.RLock()
	p := persister
	reload := reloadPool
	persistMu.RUnlock()
	if p == nil {
		return
	}
	for _, u := range batch {
		func() {
			defer func() {
				if r := recover(); r != nil {
					// 持久化的 panic 绝不能扩散：worker 死了整个健康
					// 状态机就静默失效，而且会把进程带崩
					slog.Error("[凭据健康] 持久化凭据状态时 panic", "credential_id", u.CredentialID, "err", r)
				}
			}()
			if err := p(u); err != nil {
				// 告警但不重试、不阻塞：状态是优化手段，响应早就发出去了。
				// 留痕是因为连续失败意味着调度状态机在空转（库里永远是旧状态），
				// 那种「看起来有调度其实没有」的故障只能靠日志发现。
				slog.Warn("[凭据健康] 持久化凭据状态失败", "credential_id", u.CredentialID, "err", err)
			}
		}()
	}
	if reload != nil {
		reload()
	}
}

// Stop 停止后台 worker 并等积压排空。接 main 的优雅关闭链路。
//
// 必须在 store.Close() **之前**调用：worker 还要写库，库关了写入必然失败。
// main 的关闭顺序是 srv.Shutdown（等在途请求）→ scheduler.Stop → st.Close。
func Stop() {
	persistMu.RLock()
	ch := healthCh
	persistMu.RUnlock()
	if ch == nil {
		return
	}
	close(stopCh)
	wg.Wait()
	persistMu.Lock()
	healthCh = nil
	stopCh = nil
	persistMu.Unlock()
}
