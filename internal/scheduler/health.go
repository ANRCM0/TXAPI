package scheduler

import (
	"log/slog"
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
	return statusCode == 401 || statusCode == 403 || statusCode == 408 || statusCode == 429 || (statusCode >= 500 && statusCode <= 599)
}

// HealthUpdate 是一次健康状态变更的快照。
type HealthUpdate struct {
	ObservedAt         int64
	ExpectedGeneration int64
	ExpectedUpdatedAt  time.Time
	CredentialID       uint
	FailCount          int
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
		CredentialID:       cred.ID,
		ExpectedGeneration: cred.HealthGeneration, ExpectedUpdatedAt: cred.UpdatedAt,
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
		CredentialID:       cred.ID,
		ExpectedGeneration: cred.HealthGeneration, ExpectedUpdatedAt: cred.UpdatedAt,
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
func (h *HealthWorker) RecordSuccess(cred *dbmodel.Credential, now time.Time) {
	if cred == nil {
		return
	}
	h.submit(healthEvent{credential: *cred, now: now, success: true})
}

// RecordFailure 记录一次失败转发，按状态码决定是否惩罚这把凭据。
//
// statusCode 不该罚时只更新 last_error，不动健康状态。
func (h *HealthWorker) RecordFailure(cred *dbmodel.Credential, statusCode int, errMsg string, now time.Time) {
	if cred == nil {
		return
	}
	if ShouldPenalizeCredential(statusCode) {
		slog.Debug("[凭据健康] 凭据失败待串行应用", "credential_id", cred.ID, "status_code", statusCode, "error", errMsg)
	} else {
		slog.Debug("[凭据健康] 凭据失败但属客户端错误，不惩罚", "credential_id", cred.ID, "status_code", statusCode, "error", errMsg)
	}
	h.submit(healthEvent{credential: *cred, statusCode: statusCode, message: errMsg, now: now})
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
