package scheduler

import (
	"fmt"
	"testing"
	"time"

	dbmodel "minigateway/internal/model"
)

// 本文件验证调度器最核心的两个算法：加权随机分流、可用性过滤。
//
// 为什么这两个值得单独测：它们的 bug 不会让请求失败，只会让流量分布
// 悄悄偏离预期——「weight=10 的 Key 只拿到 5% 流量」这种事在集成测试
// 里跑 10 次根本看不出来，跑 1000 次统计才现形。而这正是本项目「凭据
// 池按权重分流」这个卖点的兑现处，错了整个功能就是假的。

// creds 构造一组测试凭据。权重按传入顺序一一对应。
func creds(weights ...int) []dbmodel.Credential {
	out := make([]dbmodel.Credential, len(weights))
	for i, w := range weights {
		out[i] = dbmodel.Credential{
			ID:     uint(i + 1),
			Weight: w,
			Status: dbmodel.CredentialActive,
			APIKey: fmt.Sprintf("sk-test-%d", i+1),
		}
	}
	return out
}

// TestWeightedDistribution 验证权重 10:5:1 的三个凭据分流比例接近 10:5:1。
//
// 容差 ±20%（相对值）：加权随机是概率性的，样本 1000 次下标准差约
// sqrt(1000 × p × (1-p))，对 p=0.625 约 15 次（1.5%），20% 的容差
// 留足了统计波动又足够紧——真把权重搞成「排序取第一个」的话，
// 比例会是 1000:0:0，一眼就能抓住。
func TestWeightedDistribution(t *testing.T) {
	pool := creds(10, 5, 1)
	now := time.Now()

	const rounds = 1000
	counts := map[uint]int{}
	for i := 0; i < rounds; i++ {
		c, ok := Pick(pool, nil, now)
		if !ok {
			t.Fatalf("第 %d 次选择失败：三把凭据都该可用", i)
		}
		counts[c.ID]++
	}

	total := 0
	for _, n := range counts {
		total += n
	}
	if total != rounds {
		t.Fatalf("总次数不对: 得到 %d, 期望 %d", total, rounds)
	}

	// 期望比例：10/16, 5/16, 1/16
	for id, w := range map[uint]int{1: 10, 2: 5, 3: 1} {
		want := float64(w) / 16.0
		got := float64(counts[id]) / float64(rounds)
		if got < want*0.8 || got > want*1.2 {
			t.Errorf("凭据 %d 分流比例偏差超容差: 得到 %.3f, 期望 %.3f (±20%%)，实际选中 %d/%d 次",
				id, got, want, counts[id], rounds)
		} else {
			t.Logf("凭据 %d (权重 %d): %.3f (期望 %.3f)", id, w, got, want)
		}
	}
}

// TestWeightedDistributionNotSorted 验证「排序取第一个」这个错误实现
// 一定被抓到。
//
// 这个测试看着多余，其实是防回归的关键：它锁定了「低权重凭据必须能
// 拿到流量」这个性质。任何把 Pick 改成「取权重最高那个」的重构都会
// 在这里挂掉，而不会等到线上发现「weight=1 的 Key 三个月没被用过」。
func TestWeightedDistributionNotSorted(t *testing.T) {
	pool := creds(10, 5, 1)
	now := time.Now()
	// 低权重凭据也必须被选到过：排序取第一个的话它一次都不会出现
	seen := map[uint]bool{}
	for i := 0; i < 300; i++ {
		c, _ := Pick(pool, nil, now)
		seen[c.ID] = true
	}
	for id := uint(1); id <= 3; id++ {
		if !seen[id] {
			t.Errorf("凭据 %d 在 300 次选择里一次都没被选中——权重退化成优先级了", id)
		}
	}
}

// TestFilterUnavailable 验证四类不可用凭据全被排除。
//
// 覆盖：冷却中、已拉黑、已停用、空 APIKey。少任何一类都会让坏 Key
// 继续被选中——而「选了坏 Key」的后果是把一次本可成功的请求变成
// 401/429，用户侧看到的只是「偶发失败」。
func TestFilterUnavailable(t *testing.T) {
	now := time.Now()
	pool := []dbmodel.Credential{
		// 冷却中：截止时间在未来
		{ID: 1, Weight: 10, Status: dbmodel.CredentialCooldown,
			CooldownUntil: now.Add(time.Minute).Unix(), APIKey: "sk-test"},
		// 已拉黑
		{ID: 2, Weight: 10, Status: dbmodel.CredentialBanned, APIKey: "sk-test"},
		// 已停用
		{ID: 3, Weight: 10, Status: dbmodel.CredentialDisabled, APIKey: "sk-test"},
		// 空 APIKey：配了权重但没填 Key
		{ID: 4, Weight: 10, Status: dbmodel.CredentialActive, APIKey: ""},
		// 唯一可用的
		{ID: 5, Weight: 1, Status: dbmodel.CredentialActive, APIKey: "sk-test"},
	}
	for i := 0; i < 50; i++ {
		c, ok := Pick(pool, nil, now)
		if !ok {
			t.Fatalf("第 %d 次: 应该还能选出凭据 5", i)
		}
		if c.ID != 5 {
			t.Fatalf("第 %d 次选出了不该选中的凭据 %d (status=%s, apiKey=%q)",
				i, c.ID, c.Status, c.APIKey)
		}
	}

	// 全部不可用时返回 false，而不是 panic 或返回 nil 指针
	allBad := pool[:4]
	for i := 0; i < 10; i++ {
		if c, ok := Pick(allBad, nil, now); ok {
			t.Fatalf("全不可用时仍返回了凭据 %d", c.ID)
		}
	}
}

// TestPickExclude 验证 exclude 能把指定凭据排除掉。
//
// 这是重试逻辑的地基：换一把 Key 重试时，绝不能又选回刚失败的那把。
func TestPickExclude(t *testing.T) {
	pool := creds(1, 1, 1)
	now := time.Now()
	// 排除 1 和 2，只剩 3
	for i := 0; i < 30; i++ {
		c, ok := Pick(pool, map[uint]bool{1: true, 2: true}, now)
		if !ok {
			t.Fatalf("第 %d 次: 凭据 3 该被选中", i)
		}
		if c.ID != 3 {
			t.Fatalf("第 %d 次选出了被排除的凭据 %d", i, c.ID)
		}
	}
	// 全排除 → 选不出
	if c, ok := Pick(pool, map[uint]bool{1: true, 2: true, 3: true}, now); ok {
		t.Fatalf("全部排除后仍返回凭据 %d", c.ID)
	}
	// nil exclude 等于不排除
	if _, ok := Pick(pool, nil, now); !ok {
		t.Fatal("nil exclude 应该正常选择")
	}
}

// TestPickCooldownExpired 验证冷却到期后凭据重新可用。
//
// 边界：CooldownUntil == now 那一瞬算可用（IsUsable 用 <=）。这是恢复
// 任务的契约——后台扫描把 cooldown_until 设成过去的时间后，下一次
// Pick 必须能选到它，否则凭据池会单向枯竭。
func TestPickCooldownExpired(t *testing.T) {
	base := time.Now()
	pool := []dbmodel.Credential{
		{ID: 1, Weight: 1, Status: dbmodel.CredentialCooldown,
			CooldownUntil: base.Add(time.Minute).Unix(), APIKey: "sk-test"},
		{ID: 2, Weight: 1, Status: dbmodel.CredentialActive, APIKey: "sk-test"},
	}
	// 冷却未到期：只有 2 可用
	c, _ := Pick(pool, nil, base)
	if c.ID != 2 {
		t.Fatalf("冷却中选出了 %d, 期望 2", c.ID)
	}
	// 正好到期：1 重新可用（两个都可能被选中，但不能报错）
	if _, ok := Pick(pool, nil, base.Add(time.Minute)); !ok {
		t.Fatal("冷却到期后应该能选到凭据 1")
	}
	// 明确验证 1 可被选中
	seen1 := false
	for i := 0; i < 100 && !seen1; i++ {
		if c, _ := Pick(pool, nil, base.Add(2*time.Minute)); c.ID == 1 {
			seen1 = true
		}
	}
	if !seen1 {
		t.Fatal("冷却过期很久后凭据 1 仍选不中——恢复逻辑失效")
	}
}

// TestEffectiveWeight 验证权重 ≤ 0 按 1 处理。
//
// 为什么不能按 0：权重 0 的凭据永远选不中，却还占着渠道里的一个名额；
// 更糟的是全部权重为 0 时 Σweight = 0，rand.Intn(0) 直接 panic——
// 那会让一次转发请求把网关进程搞崩。
func TestEffectiveWeight(t *testing.T) {
	cases := []struct{ in, want int }{
		{-5, 1}, {0, 1}, {1, 1}, {7, 7}, {1000, 1000},
	}
	for _, c := range cases {
		if got := effectiveWeight(c.in); got != c.want {
			t.Errorf("effectiveWeight(%d) = %d, 期望 %d", c.in, got, c.want)
		}
	}
	// 全 0 权重不能 panic，且能选出来
	pool := []dbmodel.Credential{
		{ID: 1, Weight: 0, Status: dbmodel.CredentialActive, APIKey: "sk-test"},
		{ID: 2, Weight: -1, Status: dbmodel.CredentialActive, APIKey: "sk-test"},
	}
	for i := 0; i < 20; i++ {
		if _, ok := Pick(pool, nil, time.Now()); !ok {
			t.Fatalf("第 %d 次: 全 0 权重应仍可选中（按 1 处理）", i)
		}
	}
}

// TestPickReturnsCopy 验证返回的凭据是副本，改它不会污染入参。
//
// 这看着像过度测试，但它保护的是一个真实的不变量：渠道快照是多请求
// 共享的只读数据。如果 Pick 返回快照里的元素指针，调用方一旦改字段
// （比如反馈健康状态时）就会和别的请求的读撞成 data race。
func TestPickReturnsCopy(t *testing.T) {
	pool := creds(1)
	before := pool[0]
	c, _ := Pick(pool, nil, time.Now())
	c.Status = dbmodel.CredentialBanned
	c.FailCount = 99
	if pool[0].Status != before.Status || pool[0].FailCount != before.FailCount {
		t.Fatal("Pick 返回的不是副本：改返回值污染了入参快照")
	}
}
