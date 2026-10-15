package scheduler

import (
	"net/http"
	"testing"
	"time"

	dbmodel "minigateway/internal/model"
)

// 本文件验证健康状态机：退避曲线、拉黑阈值、错误分类。
//
// 这是整个调度器里最容易写错的地方，而且错法都很隐蔽：
//   - 退避不封顶 → 失败多了冷却到几天，等于永久禁用但状态还显示 cooldown；
//   - 错误分类不当 → 客户端的 400 把好 Key 全拉黑；
//   - 拉黑后还自动恢复 → 反复坏的 Key 一直污染流量。
// 三个都会让「凭据池」这个卖点变成负资产，所以必须逐个钉死。

// TestErrorClassification 验证 400 不惩罚、429/401/500 惩罚。
//
// 分类的核心原则：惩罚的是「换一把 Key 就能好」的失败。用户的参数错误
// 换任何一把 Key 都一样错，罚它就是拿好 Key 给烂请求陪葬。
func TestErrorClassification(t *testing.T) {
	penalize := []int{
		http.StatusUnauthorized,        // 401 Key 无效
		http.StatusForbidden,           // 403 权限不足
		http.StatusTooManyRequests,     // 429 限流
		http.StatusRequestTimeout,      // 408 超时
		http.StatusInternalServerError, // 500
		http.StatusBadGateway,          // 502（网络层错误也映射到这）
		http.StatusServiceUnavailable,  // 503
		http.StatusGatewayTimeout,      // 504
	}
	for _, code := range penalize {
		if !ShouldPenalizeCredential(code) {
			t.Errorf("状态码 %d 应该惩罚凭据，但没有", code)
		}
	}

	notPenalize := []int{
		http.StatusBadRequest, // 400 参数错误
		http.StatusNotFound,   // 404 模型不存在
		499, 200, 413, 409,
		http.StatusUnprocessableEntity, // 422 语义错误
	}
	for _, code := range notPenalize {
		if ShouldPenalizeCredential(code) {
			t.Errorf("状态码 %d 不该惩罚凭据（客户端错误），但被判为要罚", code)
		}
	}
}

// TestCooldownBackoff 验证退避序列 30s×2^(n-1) 且封顶 1 小时。
//
// 序列本身要验证（1×2×4×8 是指数退避的全部意义），封顶同样要验证：
// 不封顶的话第 20 次失败的冷却是 30s × 2^19 ≈ 180 天，状态显示
// cooldown 会让人以为「等一会儿就好」，实际永远回不来。
func TestCooldownBackoff(t *testing.T) {
	want := []time.Duration{
		30 * time.Second, // 第 1 次
		1 * time.Minute,  // 第 2 次
		2 * time.Minute,  // 第 3 次
		4 * time.Minute,  // 第 4 次
		8 * time.Minute,  // 第 5 次
		16 * time.Minute, // 第 6 次
		32 * time.Minute, // 第 7 次
		1 * time.Hour,    // 第 8 次：封顶（32m×2 = 64m > 1h）
		1 * time.Hour,    // 之后一直封顶
		1 * time.Hour,
	}
	for i, w := range want {
		got := backoff(i + 1)
		if got != w {
			t.Errorf("第 %d 次失败的退避: 得到 %v, 期望 %v", i+1, got, w)
		}
	}
	// 极端 fail_count 也不能溢出成 0 或负数
	for _, n := range []int{0, -1, 100, 1000} {
		d := backoff(n)
		if d <= 0 {
			t.Errorf("backoff(%d) = %v，必须为正", n, d)
		}
		if d > CooldownMax {
			t.Errorf("backoff(%d) = %v 超过封顶值 %v", n, d, CooldownMax)
		}
	}
}

// TestBanThreshold 验证连续失败到达阈值后转 banned，且之前是 cooldown。
//
// 同时验证「达阈值前不走 banned」：阈值是 5，第 4 次失败还该是 cooldown，
// 否则一把 Key 被两次偶发失败就永久拉黑，池子会迅速枯竭。
func TestBanThreshold(t *testing.T) {
	now := time.Now()
	cred := &dbmodel.Credential{
		ID: 7, Weight: 1, Status: dbmodel.CredentialActive,
		APIKey: "sk-test-7",
	}

	for i := 1; i < BanThreshold; i++ {
		next := NextFailure(cred, http.StatusBadGateway, "upstream 502", now)
		if next.Status != dbmodel.CredentialCooldown {
			t.Fatalf("第 %d 次失败: 状态 %q, 期望 %s（还没到拉黑阈值 %d）",
				i, next.Status, dbmodel.CredentialCooldown, BanThreshold)
		}
		if next.FailCount != i {
			t.Fatalf("第 %d 次失败: fail_count=%d, 期望 %d", i, next.FailCount, i)
		}
		if next.CooldownUntil <= now.Unix() {
			t.Fatalf("第 %d 次失败: 冷却截止时间没设在未来", i)
		}
		// 把 next 应用回 cred，模拟连续失败
		cred.FailCount = next.FailCount
		cred.Status = next.Status
		cred.CooldownUntil = next.CooldownUntil
	}

	// 第 BanThreshold 次：拉黑
	next := NextFailure(cred, http.StatusUnauthorized, "401 invalid key", now)
	if next.Status != dbmodel.CredentialBanned {
		t.Fatalf("第 %d 次失败: 状态 %q, 期望 %s", BanThreshold, next.Status, dbmodel.CredentialBanned)
	}
	if next.FailCount != BanThreshold {
		t.Fatalf("拉黑时 fail_count=%d, 期望 %d", next.FailCount, BanThreshold)
	}
	// 拉黑后冷却时间清零：banned 不走冷却恢复路径，留着 cooldown_until
	// 会让「恢复任务」把它误判成可恢复（见 RecoverCooledDownCredentials）
	if next.CooldownUntil != 0 {
		t.Fatalf("拉黑后 cooldown_until=%d, 期望 0（否则恢复任务会误放行）", next.CooldownUntil)
	}
	// 拉黑后不再可用
	if cred.IsUsable(now) {
		// 注意：这里验的是「应用了 next 之后」的凭据
		t.Log("cred 尚未应用 next，跳过 IsUsable 断言")
	}
	cred.Status = next.Status
	cred.CooldownUntil = next.CooldownUntil
	if cred.IsUsable(now) {
		t.Fatal("banned 的凭据不该被 IsUsable 判为可用")
	}
}

// TestFailureDoesNotPenalizeClientError 验证 400 不改健康状态。
//
// 这是错误分类在状态机层面的落地：只测 ShouldPenalizeCredential 不够，
// 要验证「返回的 HealthUpdate 真的什么都没改」——否则 NextFailure 里
// 漏了那个分支的话，分类函数再对也没用。
func TestFailureDoesNotPenalizeClientError(t *testing.T) {
	now := time.Now()
	cred := &dbmodel.Credential{
		ID: 9, Weight: 1, Status: dbmodel.CredentialActive,
		APIKey: "sk-test",
	}
	for _, code := range []int{http.StatusBadRequest, http.StatusNotFound, http.StatusUnprocessableEntity} {
		next := NextFailure(cred, code, "client error", now)
		if next.Changed(cred) {
			t.Errorf("状态码 %d 的失败改变了健康状态: %+v（不该惩罚凭据）", code, next)
		}
		if next.FailCount != cred.FailCount {
			t.Errorf("状态码 %d: fail_count 从 %d 变成 %d", code, cred.FailCount, next.FailCount)
		}
		if next.Status != cred.Status {
			t.Errorf("状态码 %d: 状态从 %s 变成 %s", code, cred.Status, next.Status)
		}
	}
	// 但 last_error 要记下来：不惩罚不等于不记录，排查「这把 Key 为什么
	// 被绕过」时 last_error 是唯一线索
	if next := NextFailure(cred, http.StatusBadRequest, "client error", now); next.LastError != "client error" {
		t.Error("不该惩罚的失败也应记录 last_error")
	}
}

// TestSuccessResets 验证成功把状态完全清零。
//
// 关键在「完全」：fail_count、cooldown_until、status 三个一起清。只清
// status 会留下「active 但 fail_count=4」的凭据，下次一失败直接跳到
// 拉黑阈值——「解封了但没真解封」是最难排查的一类故障。
func TestSuccessResets(t *testing.T) {
	now := time.Now()
	cred := &dbmodel.Credential{
		ID: 11, Weight: 1, Status: dbmodel.CredentialCooldown,
		FailCount: 3, CooldownUntil: now.Add(time.Hour).Unix(),
		APIKey: "sk-test",
	}
	next := NextSuccess(cred, now)
	if next.Status != dbmodel.CredentialActive {
		t.Errorf("成功后状态 %q, 期望 %s", next.Status, dbmodel.CredentialActive)
	}
	if next.FailCount != 0 {
		t.Errorf("成功后 fail_count=%d, 期望 0", next.FailCount)
	}
	if next.CooldownUntil != 0 {
		t.Errorf("成功后 cooldown_until=%d, 期望 0", next.CooldownUntil)
	}
	if !next.Changed(cred) {
		t.Error("从冷却恢复到 active 是状态变化，应该 Changed")
	}
	// 应用后必须立刻可用
	cred.Status = next.Status
	cred.FailCount = next.FailCount
	cred.CooldownUntil = next.CooldownUntil
	if !cred.IsUsable(now) {
		t.Fatal("成功重置后的凭据应该立刻可用")
	}
}

// TestSuccessNoopWhenHealthy 验证「本来就全绿又成功一次」不产生状态变更。
//
// 这条挡的是「每个成功请求都写一次库」：健康反馈跑在每个成功请求上，
// 不判 Changed 的话每次转发都要付一次 SQLite 事务，白烧 IO。
func TestSuccessNoopWhenHealthy(t *testing.T) {
	now := time.Now()
	cred := &dbmodel.Credential{
		ID: 13, Weight: 1, Status: dbmodel.CredentialActive,
		APIKey: "sk-test-13",
	}
	if next := NextSuccess(cred, now); next.Changed(cred) {
		t.Errorf("全绿凭据的成功不该产生状态变更: %+v", next)
	}
}

// TestChangedIgnoresLastFields 验证 Changed 只看调度相关字段。
//
// last_error / last_used_at 是排查用的时间戳，它们变了不该触发落库
// 和渠道池重载——否则「记录一次错误信息」会让整个渠道池全量重载一遍。
func TestChangedIgnoresLastFields(t *testing.T) {
	cred := &dbmodel.Credential{ID: 1, Status: dbmodel.CredentialActive, APIKey: "sk-test"}
	u := HealthUpdate{
		CredentialID: 1,
		Status:       dbmodel.CredentialActive,
		FailCount:    0,
		LastError:    "some error",
		LastUsedAt:   12345,
	}
	if u.Changed(cred) {
		t.Error("只改 last_error/last_used_at 不该算作状态变化")
	}
	u.Status = dbmodel.CredentialCooldown
	if !u.Changed(cred) {
		t.Error("状态变化必须被 Changed 识别")
	}
	u.Status = dbmodel.CredentialActive
	u.FailCount = 1
	if !u.Changed(cred) {
		t.Error("fail_count 变化必须被 Changed 识别")
	}
	u.FailCount = 0
	u.CooldownUntil = 999
	if !u.Changed(cred) {
		t.Error("cooldown_until 变化必须被 Changed 识别")
	}
}

// TestRecordWithoutPersister 验证不注入 persister 时状态机照常工作。
//
// 单元测试的场景：算法要和落库解耦，验证算法不需要建库。
// 同时保证生产上「忘了注入 persister」不会 panic——只会静默不落库，
// 而静默不落库的后果（状态不生效）有日志可查。
func TestRecordWithoutPersister(t *testing.T) {
	// 确保没有 persister（测试间可能互相影响，显式清掉）
	h := NewHealthWorker(nil, nil)
	defer h.Close()
	cred := &dbmodel.Credential{ID: 21, Weight: 1, Status: dbmodel.CredentialActive, APIKey: "sk-test"}

	// 不该 panic
	h.RecordFailure(cred, http.StatusUnauthorized, "401", time.Now())
	h.RecordSuccess(cred, time.Now())

	// RecordSuccess 后状态机算出的结果是全绿（提交的是 next 的语义，
	// 这里验证调用没炸且 cred 未被原地修改——改 cred 会造成 data race）
	if cred.Status != dbmodel.CredentialActive {
		t.Errorf("Record* 不该原地修改传入的凭据: status=%s", cred.Status)
	}
}

// TestRecordSuccessPersisted 验证注入 persister 后成功会被落库。
func TestRecordSuccessPersisted(t *testing.T) {
	got := make(chan HealthUpdate, 4)
	h := NewHealthWorker(func(u HealthUpdate) error {
		got <- u
		return nil
	}, nil)
	defer h.Close()

	cred := &dbmodel.Credential{
		ID: 31, Weight: 1, Status: dbmodel.CredentialCooldown,
		FailCount: 2, CooldownUntil: time.Now().Add(time.Hour).Unix(),
		APIKey: "sk-test",
	}
	h.RecordSuccess(cred, time.Now())

	select {
	case u := <-got:
		if u.CredentialID != 31 {
			t.Errorf("落库的凭据 ID=%d, 期望 31", u.CredentialID)
		}
		if u.Status != dbmodel.CredentialActive || u.FailCount != 0 || u.CooldownUntil != 0 {
			t.Errorf("落库的状态不对: %+v", u)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("2 秒内没收到落库调用")
	}
}

// TestRecordFailurePersisted 验证失败落库时带上了退避后的冷却时间。
func TestRecordFailurePersisted(t *testing.T) {
	got := make(chan HealthUpdate, 4)
	h := NewHealthWorker(func(u HealthUpdate) error {
		got <- u
		return nil
	}, nil)
	defer h.Close()

	now := time.Now()
	cred := &dbmodel.Credential{ID: 41, Weight: 1, Status: dbmodel.CredentialActive, APIKey: "sk-test"}
	h.RecordFailure(cred, http.StatusTooManyRequests, "429 rate limited", now)

	select {
	case u := <-got:
		if u.FailCount != 1 {
			t.Errorf("fail_count=%d, 期望 1", u.FailCount)
		}
		if u.Status != dbmodel.CredentialCooldown {
			t.Errorf("状态=%s, 期望 %s", u.Status, dbmodel.CredentialCooldown)
		}
		wantUntil := now.Add(CooldownBase).Unix()
		if u.CooldownUntil != wantUntil {
			t.Errorf("cooldown_until=%d, 期望 %d (now+%v)", u.CooldownUntil, wantUntil, CooldownBase)
		}
		if u.LastError != "429 rate limited" {
			t.Errorf("last_error=%q, 期望记录错误信息", u.LastError)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("2 秒内没收到落库调用")
	}
}
