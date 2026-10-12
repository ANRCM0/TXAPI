package scheduler

import (
	"testing"
	"time"

	dbmodel "minigateway/internal/model"
)

// 本文件验证会话亲和：同会话恒定、凭据增删后尽量稳定、目标不可用时降级。
//
// 亲和的价值不只是「避免上下文错乱」：上游按账号缓存 prompt，同一对话
// 固定打同一把 Key 才能吃到缓存命中——省钱又提速。所以「稳定」是这个
// 功能的全部意义，稳定性一破（比如重启后全部重排），亲和就等于没做。

// TestAffinityStable 验证同一个 sessionID 恒定命中同一把凭据。
func TestAffinityStable(t *testing.T) {
	pool := creds(3, 2, 1)
	now := time.Now()
	const session = "session-abc-123"

	first, ok := PickWithAffinity(pool, session, nil, now)
	if !ok {
		t.Fatal("首次选择失败")
	}
	// 连发 200 次，credential_id 必须恒定
	for i := 0; i < 200; i++ {
		c, ok := PickWithAffinity(pool, session, nil, now)
		if !ok {
			t.Fatalf("第 %d 次选择失败", i)
		}
		if c.ID != first.ID {
			t.Fatalf("第 %d 次选中凭据 %d, 期望恒定的 %d", i, c.ID, first.ID)
		}
	}
	t.Logf("会话 %s 恒定命中凭据 %d", session, first.ID)
}

// TestAffinityDistributes 验证不同会话会分散到不同凭据上。
//
// 只有这一条成立，亲和才没有退化成「所有会话都打同一把 Key」——
// 那等于权重配置完全失效，是最坏的情况（既没吃到缓存又没分流）。
func TestAffinityDistributes(t *testing.T) {
	pool := creds(3, 2, 1)
	now := time.Now()
	counts := map[uint]int{}
	const sessions = 600
	for i := 0; i < sessions; i++ {
		c, ok := PickWithAffinity(pool, sessionName(i), nil, now)
		if !ok {
			t.Fatalf("会话 %d 选择失败", i)
		}
		counts[c.ID]++
	}
	if len(counts) < 2 {
		t.Fatalf("600 个会话只落在 %d 把凭据上，亲和退化成单点了", len(counts))
	}
	t.Logf("600 个会话的分布: %v", counts)
	// 三把凭据都该有用到（权重最小的也该有，一致性哈希不该把它饿死）
	for id := uint(1); id <= 3; id++ {
		if counts[id] == 0 {
			t.Errorf("凭据 %d 一个会话都没分到——权重被哈希分布忽略了", id)
		}
	}
}

// TestAffinityStableAfterRemoval 验证删掉一把凭据后，其余会话的映射尽量不变。
//
// 这是「一致性哈希」四个字唯一的验收标准。用取模（hash % len)）实现的话，
// 分母一变所有会话全部重排，上游缓存全废——每次加 Key 都要付出一次
// 全量缓存作废的代价，那还不如不做亲和。
func TestAffinityStableAfterRemoval(t *testing.T) {
	now := time.Now()
	pool := creds(3, 2, 1)

	// 记录删之前的映射
	const sessions = 300
	before := make(map[string]uint, sessions)
	for i := 0; i < sessions; i++ {
		s := sessionName(i)
		c, _ := PickWithAffinity(pool, s, nil, now)
		before[s] = c.ID
	}

	// 删掉一把（模拟运维把一个 Key 从池子里摘掉）
	removed := pool[1]
	after := make([]dbmodel.Credential, 0, len(pool)-1)
	for _, c := range pool {
		if c.ID != removed.ID {
			after = append(after, c)
		}
	}

	changed := 0
	for i := 0; i < sessions; i++ {
		s := sessionName(i)
		c, ok := PickWithAffinity(after, s, nil, now)
		if !ok {
			t.Fatalf("会话 %s 在删凭据后选不出来了", s)
		}
		if c.ID != before[s] {
			changed++
		}
	}
	// 一致性哈希的期望：只有原本命中被删那把的会话会变，约 1/3。
	// 阈值放到 50%：既足够紧（取模实现会接近 100% 变化），
	// 又给哈希分布的统计波动留了余量。
	ratio := float64(changed) / float64(sessions)
	if ratio > 0.5 {
		t.Errorf("删掉一把凭据后 %.0f%% 的会话映射变了（期望 ≤50%%，一致性哈希应只有命中被删那把的会话受影响）",
			ratio*100)
	}
	t.Logf("删掉凭据 %d 后，%d/%d (%.0f%%) 个会话的映射发生变化",
		removed.ID, changed, sessions, ratio*100)
}

// TestAffinityFallback 验证亲和目标不可用时降级到加权随机，不报错不卡死。
//
// 场景：会话原本命中凭据 A，A 进了冷却。请求必须照常成功（打到别的
// Key 上），而不是因为「亲和目标不可用」就失败——亲和是优化，不是
// 正确性约束。
func TestAffinityFallback(t *testing.T) {
	now := time.Now()
	pool := []dbmodel.Credential{
		{ID: 1, Weight: 5, Status: dbmodel.CredentialActive, APIKey: "sk-test"},
		{ID: 2, Weight: 5, Status: dbmodel.CredentialCooldown,
			CooldownUntil: now.Add(time.Hour).Unix(), APIKey: "sk-test"},
		{ID: 3, Weight: 5, Status: dbmodel.CredentialBanned, APIKey: "sk-test"},
	}
	// 找出一个亲和命中「坏凭据」的会话
	var stuck string
	for i := 0; i < 500; i++ {
		s := sessionName(i)
		c, _ := PickWithAffinity(pool, s, nil, now)
		if c.ID != 1 {
			stuck = s
			break
		}
	}
	if stuck == "" {
		t.Skip("500 个会话都命中凭据 1，没有构造出降级场景（哈希分布使然，非 bug）")
	}

	// 这把 Key 被拉黑后，同一会话必须仍然选得出凭据（降级到 1）
	c, ok := PickWithAffinity(pool, stuck, nil, now)
	if !ok {
		t.Fatal("亲和目标不可用时应该降级选择，而不是返回 false")
	}
	if c.ID != 1 {
		t.Fatalf("会话 %s 降级后选中 %d, 期望 1（唯一可用的）", stuck, c.ID)
	}
	t.Logf("会话 %s 的亲和目标不可用，已降级到凭据 %d", stuck, c.ID)
}

// TestAffinityAllUnavailable 验证全部凭据不可用时返回 false。
//
// 降级的底线：没有可降级的目标时必须明确说「选不出」，让调用方走
// 「没有可用渠道」那条路——返回一个 nil 或 panic 都会让转发层炸。
func TestAffinityAllUnavailable(t *testing.T) {
	now := time.Now()
	pool := []dbmodel.Credential{
		{ID: 1, Weight: 5, Status: dbmodel.CredentialBanned, APIKey: "sk-test"},
		{ID: 2, Weight: 5, Status: dbmodel.CredentialCooldown,
			CooldownUntil: now.Add(time.Hour).Unix(), APIKey: "sk-test"},
	}
	for i := 0; i < 20; i++ {
		if c, ok := PickWithAffinity(pool, sessionName(i), nil, now); ok {
			t.Fatalf("全不可用时仍返回凭据 %d", c.ID)
		}
	}
}

// TestAffinityEmptySession 验证空 sessionID 直接走纯加权随机。
//
// 什么时候会空：客户端没传 X-Session-Id，且请求体里抽不出首条 user
// 消息。这时必须降级到 Pick，而不是硬造一个每次都变的 ID（那会让亲和
// 彻底失效，还白付一次哈希）。
func TestAffinityEmptySession(t *testing.T) {
	pool := creds(1, 1, 1)
	now := time.Now()
	seen := map[uint]bool{}
	for i := 0; i < 200; i++ {
		c, ok := PickWithAffinity(pool, "", nil, now)
		if !ok {
			t.Fatalf("第 %d 次: 空 sessionID 应降级到加权随机", i)
		}
		seen[c.ID] = true
	}
	if len(seen) < 2 {
		t.Fatal("空 sessionID 应该走加权随机（多把凭据都被选到），而不是固定在某一把")
	}
}

// TestAffinityExclude 验证 exclude 在亲和路径上也生效。
//
// 重试场景：第一把 Key 失败了，重试时 exclude 里已经有它。如果亲和
// 路径忽略 exclude，重试会又选回刚失败的那把——无限重试同一把坏 Key。
func TestAffinityExclude(t *testing.T) {
	pool := creds(1, 1, 1)
	now := time.Now()
	// 找出每个会话的亲和目标，排除掉它之后必须选到别的
	for i := 0; i < 30; i++ {
		s := sessionName(i)
		target, ok := PickWithAffinity(pool, s, nil, now)
		if !ok {
			t.Fatalf("会话 %s 首次选择失败", s)
		}
		c, ok := PickWithAffinity(pool, s, map[uint]bool{target.ID: true}, now)
		if !ok {
			t.Fatalf("会话 %s 排除 %d 后选不出来了", s, target.ID)
		}
		if c.ID == target.ID {
			t.Fatalf("会话 %s 重试又选回了已失败的凭据 %d", s, target.ID)
		}
	}
}

// TestSessionIDFromHeader 验证请求头优先于消息哈希。
func TestSessionIDFromHeader(t *testing.T) {
	msgs := []Message{{Role: "user", Content: "你好"}}
	fromHeader := SessionID("my-session-1", 42, msgs)
	fromBody := SessionID("", 42, msgs)
	if fromHeader == fromBody {
		t.Fatal("显式会话 ID 和消息哈希派生出了同一个值，优先级没生效")
	}
	// 同一个头 + 同一个令牌必须稳定
	for i := 0; i < 10; i++ {
		if got := SessionID("my-session-1", 42, msgs); got != fromHeader {
			t.Fatal("同一个会话 ID 派生出不同结果")
		}
	}
}

// TestSessionIDFromBody 验证兜底路径：同令牌 + 同首条消息 → 同会话。
//
// 这一条是「不传 X-Session-Id 的客户端也能吃到亲和」的保证。带上令牌
// ID 是为了让两个令牌问同一个问题时落到不同凭据，否则它们会互相挤占
// 同一把 Key 的缓存。
func TestSessionIDFromBody(t *testing.T) {
	msgs := []Message{
		{Role: "system", Content: "你是助手"},
		{Role: "user", Content: "第一轮问题"},
		{Role: "assistant", Content: "回答"},
		{Role: "user", Content: "第二轮问题"},
	}
	a := SessionID("", 42, msgs)
	b := SessionID("", 42, msgs)
	if a == "" {
		t.Fatal("有 user 消息时应派生出会话 ID")
	}
	if a != b {
		t.Fatal("同令牌 + 同对话应派生出相同会话 ID")
	}
	// 后续轮次追加消息不该改变会话 ID（只取首条 user 消息）
	msgs2 := append(append([]Message{}, msgs...), Message{Role: "user", Content: "第三轮问题"})
	if c := SessionID("", 42, msgs2); c != a {
		t.Fatal("追加后续轮次后会话 ID 变了——亲和会在多轮对话里失效")
	}
	// 换令牌应该换会话（避免两个令牌挤占同一把 Key 的缓存）
	if d := SessionID("", 43, msgs); d == a {
		t.Fatal("不同令牌派生出相同会话 ID——会互相挤占缓存")
	}
}

// TestSessionIDNoUserMessage 验证没有 user 消息时返回空串。
//
// 空串是「不做亲和」的信号，调用方据此降级到纯加权随机。
func TestSessionIDNoUserMessage(t *testing.T) {
	if got := SessionID("", 42, []Message{{Role: "system", Content: "x"}}); got != "" {
		t.Fatalf("没有 user 消息时应返回空串，得到 %q", got)
	}
	if got := SessionID("", 42, nil); got != "" {
		t.Fatalf("空消息列表应返回空串，得到 %q", got)
	}
	// 空 content 的 user 消息也算没有
	if got := SessionID("", 42, []Message{{Role: "user", Content: ""}}); got != "" {
		t.Fatalf("空 content 应返回空串，得到 %q", got)
	}
}

// TestSessionIDMultimodal 验证多模态消息（content 是数组）也能派生会话。
//
// content 为数组时不能反序列化失败——带图片的请求本来是该正常转发的，
// 不该被网关自己的会话亲和逻辑拦掉。
func TestSessionIDMultimodal(t *testing.T) {
	msgs := []Message{{
		Role: "user",
		Content: []any{
			map[string]any{"type": "text", "text": "描述这张图"},
			map[string]any{"type": "image_url", "image_url": map[string]any{"url": "http://x/y.png"}},
		},
	}}
	a := SessionID("", 42, msgs)
	if a == "" {
		t.Fatal("多模态消息应能派生出会话 ID")
	}
	if b := SessionID("", 42, msgs); a != b {
		t.Fatal("同一多模态消息应派生稳定会话 ID")
	}
}

// sessionName 生成测试用的会话名。
func sessionName(i int) string {
	return "sess-" + string(rune('a'+i%26)) + "-" + itoaSmall(i)
}

// itoaSmall 是小整数转字符串（避免为测试引入 strconv 的心智负担）。
func itoaSmall(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [8]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
