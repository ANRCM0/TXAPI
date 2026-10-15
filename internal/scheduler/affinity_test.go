package scheduler

import (
	"strconv"
	"testing"
	"time"

	dbmodel "minigateway/internal/model"
)

// 本文件验证会话亲和：同会话恒定、凭据增删后尽量稳定、目标不可用时降级。
//
// 亲和的价值不只是「避免上下文错乱」：上游按账号缓存 prompt，同一对话
// 固定打同一把 Key 才能吃到缓存命中——省钱又提速。
func TestAffinityStable(t *testing.T) {
	now := time.Now()
	pool := creds(3, 2, 1)
	const session = "stable-session"
	first, ok := PickWithAffinity(pool, session, nil, now)
	if !ok {
		t.Fatal("亲和选择失败")
	}
	for i := 0; i < 50; i++ {
		c, ok := PickWithAffinity(pool, session, nil, now)
		if !ok {
			t.Fatalf("第 %d 次选择失败", i)
		}
		if c.ID != first.ID {
			t.Fatalf("第 %d 次选中凭据 %d, 期望 %d（同会话应恒定）", i, c.ID, first.ID)
		}
	}
	t.Logf("会话 %s 稳定命中凭据 %d", session, first.ID)
}

// TestAffinityDistributes 验证不同会话会散到不同凭据上（不是全挤在一把）。
func TestAffinityDistributes(t *testing.T) {
	now := time.Now()
	pool := creds(3, 2, 1)
	counts := map[uint]int{}
	for i := 0; i < 600; i++ {
		c, ok := PickWithAffinity(pool, sessionName(i), nil, now)
		if !ok {
			t.Fatal("选择失败")
		}
		counts[c.ID]++
	}
	t.Logf("600 个会话的分布: %v", counts)
	if len(counts) < 2 {
		t.Fatalf("600 个会话全落在一把凭据上: %v，亲和退化成常量", counts)
	}
}

// TestAffinityStableAfterRemoval 验证删掉一把凭据后，只有少部分会话重映射。
func TestAffinityStableAfterRemoval(t *testing.T) {
	now := time.Now()
	before := creds(3, 2, 1)
	after := creds(3, 1)

	moved, total := 0, 300
	for i := 0; i < total; i++ {
		s := sessionName(i)
		b, _ := PickWithAffinity(before, s, nil, now)
		a, _ := PickWithAffinity(after, s, nil, now)
		if b.ID != a.ID {
			moved++
		}
	}
	ratio := float64(moved) / float64(total)
	t.Logf("删掉凭据 2 后，%d/%d (%.0f%%) 个会话的映射发生变化", moved, total, ratio*100)
	if ratio > 0.5 {
		t.Errorf("删一把凭据导致 %.0f%% 的会话重映射，一致性哈希失效", ratio*100)
	}
}

// TestAffinityFallback 验证亲和目标不可用时降级到加权随机，不报错不卡死。
//
// 场景：会话原本命中凭据 2，2 进了冷却。请求必须照常成功（打到别的 Key
// 上），而不是因为「亲和目标不可用」就失败——亲和是优化，不是正确性约束。
func TestAffinityFallback(t *testing.T) {
	now := time.Now()

	// 第一步：在三把都健康的池子里，找一个亲和命中凭据 2 的会话。
	//
	// 为什么不能直接在「凭据 2 已坏」的池子里找：affinityTarget 只在可用
	// 凭据里定位（坏凭据已被 filterUsable 挡掉），那种池子里每个会话都会
	// 命中凭据 1，构造不出「原本粘在坏凭据上」的场景。必须先让亲和把会话
	// 钉在凭据 2 上，再把凭据 2 弄坏，才是真实的故障时序。
	healthy := []dbmodel.Credential{
		{ID: 1, Weight: 5, Status: dbmodel.CredentialActive, APIKey: "sk-test"},
		{ID: 2, Weight: 5, Status: dbmodel.CredentialActive, APIKey: "sk-test"},
		{ID: 3, Weight: 5, Status: dbmodel.CredentialActive, APIKey: "sk-test"},
	}
	var stuck string
	for i := 0; i < 500; i++ {
		s := sessionName(i)
		c, ok := PickWithAffinity(healthy, s, nil, now)
		if ok && c.ID == 2 {
			stuck = s
			break
		}
	}
	if stuck == "" {
		t.Fatal("500 个会话都没命中凭据 2，一致性哈希分布异常")
	}

	// 第二步：凭据 2 进冷却。同一会话必须仍然选得出凭据（降级），而不是
	// 因为「亲和目标不可用」就返回 false。
	degraded := []dbmodel.Credential{
		{ID: 1, Weight: 5, Status: dbmodel.CredentialActive, APIKey: "sk-test"},
		{ID: 2, Weight: 5, Status: dbmodel.CredentialCooldown,
			CooldownUntil: now.Add(time.Hour).Unix(), APIKey: "sk-test"},
		{ID: 3, Weight: 5, Status: dbmodel.CredentialActive, APIKey: "sk-test"},
	}
	c, ok := PickWithAffinity(degraded, stuck, nil, now)
	if !ok {
		t.Fatal("亲和目标不可用时应该降级选择，而不是返回 false")
	}
	if c.ID == 2 {
		t.Fatalf("会话 %s 仍选中已冷却的凭据 2，降级没生效", stuck)
	}
	// 降级后必须稳定：同一会话再选十次都该落在同一把上，否则「降级」会
	// 退化成每请求随机跳 Key，prompt 缓存照样打不中。
	for i := 0; i < 10; i++ {
		again, ok2 := PickWithAffinity(degraded, stuck, nil, now)
		if !ok2 || again.ID != c.ID {
			t.Fatalf("第 %d 次降级选择不稳定: 得到 %v (ok=%v), 期望 %d", i, again, ok2, c.ID)
		}
	}
	t.Logf("会话 %s 原本亲和凭据 2，该凭据冷却后稳定降级到凭据 %d", stuck, c.ID)
}

// TestAffinityAllUnavailable 验证全部凭据不可用时返回 false。
//
// 降级的底线：没有可降级的目标时必须明确说「选不出」，让调用方走
// 「没有可用渠道」那条路——返回 nil 或 panic 都会让转发层炸。
func TestAffinityAllUnavailable(t *testing.T) {
	now := time.Now()
	pool := []dbmodel.Credential{
		{ID: 1, Weight: 5, Status: dbmodel.CredentialBanned, APIKey: "sk-test"},
		{ID: 2, Weight: 5, Status: dbmodel.CredentialCooldown,
			CooldownUntil: now.Add(time.Hour).Unix(), APIKey: "sk-test"},
	}
	if _, ok := PickWithAffinity(pool, "any", nil, now); ok {
		t.Fatal("全部不可用时应该返回 false")
	}
}

// TestAffinityExclude 验证 exclude 里的凭据不会被亲和选中。
func TestAffinityExclude(t *testing.T) {
	now := time.Now()
	pool := []dbmodel.Credential{
		{ID: 1, Weight: 5, Status: dbmodel.CredentialActive, APIKey: "sk-test"},
		{ID: 2, Weight: 5, Status: dbmodel.CredentialActive, APIKey: "sk-test"},
	}
	// 会话优先打 1，把 1 排除后必须落到 2
	target, _ := PickWithAffinity(pool, "excl-session", nil, now)
	excl := map[uint]bool{target.ID: true}
	c, ok := PickWithAffinity(pool, "excl-session", excl, now)
	if !ok {
		t.Fatal("排除后仍该选出另一把")
	}
	if c.ID == target.ID {
		t.Fatalf("选中了被排除的凭据 %d", target.ID)
	}
}

// TestAffinityEmptySession 验证空 sessionID 也能工作（降级到纯加权随机）。
func TestAffinityEmptySession(t *testing.T) {
	now := time.Now()
	pool := creds(10, 5, 1)
	c, ok := PickWithAffinity(pool, "", nil, now)
	if !ok || c == nil {
		t.Fatal("空 sessionID 应该仍能选出凭据")
	}
}

// TestAffinityWeightedDistribution 验证亲和环的分流比例贴近权重。
//
// 这是之前缺失的一环：TestWeightedDistribution 只测了 Pick（纯加权随机），
// 但生产路径走的是 PickWithAffinity——main.go 注入的是它。少了这个测试，
// 环偏斜（虚拟节点数太小 + FNV 对短串雪崩性差）在单测里完全看不见，
// 只有在真实流量下按权重分流才会失真。
//
// 会话 ID 用真随机的 UUID 形态：亲和的分流取决于会话在环上的落点分布，
// 用 "session-0"、"session-1" 这类递增串会让落点扎堆，测出来的是
// 测试数据的性质而不是实现的性质。
func TestAffinityWeightedDistribution(t *testing.T) {
	now := time.Now()
	// 权重 5:1:1 —— 刻意让两把低权重凭据权重相同，这样「分得准不准」
	// 不会和「两把低权重凭据之间怎么分」纠缠在一起。
	pool := creds(5, 1, 1)
	weights := map[uint]int{}
	total := 0
	for _, c := range pool {
		weights[c.ID] = c.Weight
		total += c.Weight
	}

	const rounds = 20000
	counts := map[uint]int{}
	for i := 0; i < rounds; i++ {
		c, ok := PickWithAffinity(pool, randomSession(i), nil, now)
		if !ok {
			t.Fatalf("第 %d 次选择失败", i)
		}
		counts[c.ID]++
	}

	for id, w := range weights {
		got := float64(counts[id]) / float64(rounds)
		want := float64(w) / float64(total)
		// 容差 15%：环上分流不是精确概率，节点撒得够碎时偏差应在个位数
		// 百分点以内；15% 能抓住「偏斜到失真」（修之前权重 1 的凭据
		// 少了近 40% 流量），又不会被环的固有抖动打红。
		if rel := (got - want) / want; rel > 0.15 || rel < -0.15 {
			t.Errorf("凭据 %d 分流 %.2f%% 偏离期望 %.2f%% 超过 15%%（相对偏差 %.1f%%）",
				id, got*100, want*100, rel*100)
		} else {
			t.Logf("凭据 %d (权重 %d): %.2f%% (期望 %.2f%%)", id, w, got*100, want*100)
		}
	}
}

// TestAffinityRingCached 验证同一凭据池复用同一张环（不是每请求重建）。
//
// 环构建要排序上万个节点，每请求重建会把一致性哈希的固定开销摊在每个
// 请求上。这里用「同一池的两次选择返回相同指针指向的环」来间接验证——
// 直接比较环指针更直白，但 getRing 是内部函数，测试同包可直接调。
func TestAffinityRingCached(t *testing.T) {
	now := time.Now()
	pool := creds(3, 2, 1)
	usable := filterUsable(pool, nil, now)
	if len(usable) == 0 {
		t.Fatal("没有可用凭据")
	}
	key := poolKey(usable)
	r1 := getRing(usable, key)
	r2 := getRing(usable, key)
	if r1 != r2 {
		t.Fatal("同一凭据池应复用同一张环，实际每次都在重建")
	}
}

// randomSession 生成第 i 个随机形态的会话 ID。
//
// 用 splitmix64 之类的确定性 PRNG 而不是 math/rand 的全局源：
// 测试要可复现（失败时能重跑出同样的分布），又不能像 "session-%d"
// 那样让落点扎堆。这里用整数 i 做雪崩，得到统计上均匀又确定性的串。
func randomSession(i int) string {
	x := uint64(i)*0x9e3779b97f4a7c15 + 0xbf58476d1ce4e5b9
	x ^= x >> 30
	x *= 0xbf58476d1ce4e5b9
	x ^= x >> 27
	x *= 0x94d049bb133111eb
	x ^= x >> 31
	return "sess-" + strconv.FormatUint(x, 36)
}

// sessionName 生成第 i 个测试会话 ID。
//
// 用确定性名字而不是随机串：亲和测试要能在不同次运行里构造出同一个
// 「粘在某把凭据上」的会话，随机串会让 TestAffinityFallback 的
// 「先钉住再弄坏」时序不可复现。
func sessionName(i int) string {
	return "session-" + strconv.Itoa(i)
}

func TestAffinityCanonicalOrderAndBoundedRing(t *testing.T) {
	pool := creds(1, 3, 2)
	now := time.Now()
	for i := 0; i < 300; i++ {
		session := randomSession(i)
		before, _ := PickWithAffinity(pool, session, nil, now)
		reordered := []dbmodel.Credential{pool[2], pool[0], pool[1]}
		after, _ := PickWithAffinity(reordered, session, nil, now)
		if before.ID != after.ID {
			t.Fatalf("cached indices depended on snapshot order: %d %d", before.ID, after.ID)
		}
	}
	huge := creds(1000000, 1000000, 1)
	ring := buildRing(huge)
	if len(ring.nodes) > ringMaxNodes || len(ring.nodes) == 0 {
		t.Fatalf("unbounded ring nodes: %d", len(ring.nodes))
	}
}
