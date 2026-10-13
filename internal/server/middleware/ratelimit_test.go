package middleware

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"minigateway/internal/model"
)

func init() {
	gin.SetMode(gin.TestMode)
}

// bytesReader 把字符串包成请求体。
func bytesReader(s string) io.Reader {
	return bytes.NewBufferString(s)
}

// newTestEngine 组装一个只挂限流中间件的引擎，token 走上下文直接注入。
//
// 为什么绕过 Auth：本文件测的是限流本身，鉴权有它自己的测试。
// 直接往上下文塞 TokenView 等价于「Auth 已经跑完」——
// 真实链路里 TokenRateLimit 也只在 Auth 之后执行，注入投影
// 正是复刻那个前提。
func newTestEngine(t *testing.T, limiter Limiter, view model.TokenView) *gin.Engine {
	t.Helper()
	e := gin.New()
	e.POST("/v1/chat/completions", func(c *gin.Context) {
		c.Set(CtxTokenView, view)
		c.Set(CtxTokenID, view.ID)
		c.Next()
	}, TokenRateLimit(limiter), func(c *gin.Context) {
		c.String(http.StatusOK, "ok")
	})
	return e
}

// post 发一个请求，返回状态码和 Retry-After 头。
func post(t *testing.T, e *gin.Engine) (int, string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		bytesReader(`{"model":"gpt-4o-mini","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	e.ServeHTTP(w, req)
	return w.Code, w.Header().Get("Retry-After")
}

// TestRateLimitAllowsUnderLimit：不超限全部放行。
func TestRateLimitAllowsUnderLimit(t *testing.T) {
	limiter := NewMemoryLimiter()
	e := newTestEngine(t, limiter, model.TokenView{ID: 1, Name: "t", RPMLimit: 10})

	for i := 0; i < 10; i++ {
		code, retry := post(t, e)
		if code != http.StatusOK {
			t.Fatalf("第 %d 次请求应放行，实际 %d", i+1, code)
		}
		if retry != "" {
			t.Fatalf("放行时不该有 Retry-After，实际 %q", retry)
		}
	}
}

// TestRateLimitRejectsOverLimit：超限返回 429 且带 Retry-After。
func TestRateLimitRejectsOverLimit(t *testing.T) {
	limiter := NewMemoryLimiter()
	e := newTestEngine(t, limiter, model.TokenView{ID: 2, Name: "t", RPMLimit: 3})

	for i := 0; i < 3; i++ {
		if code, _ := post(t, e); code != http.StatusOK {
			t.Fatalf("第 %d 次请求应放行，实际 %d", i+1, code)
		}
	}
	// 第 4 次必须被拒，且必须给出重试等待时间
	code, retry := post(t, e)
	if code != http.StatusTooManyRequests {
		t.Fatalf("第 4 次请求应 429，实际 %d", code)
	}
	if retry == "" {
		t.Fatal("429 必须带 Retry-After 头，否则客户端只会立刻重试变成忙循环")
	}
	// Retry-After 必须是「多少秒后可以重试」的正整数
	secs, err := strconv.Atoi(retry)
	if err != nil || secs < 1 {
		t.Fatalf("Retry-After 应为正整数秒，实际 %q", retry)
	}
	if secs > 60 {
		t.Fatalf("Retry-After 不应超过窗口 60 秒，实际 %d", secs)
	}
}

// TestRateLimitErrorBodyIsOpenAIShape：429 的错误体沿用网关统一的
// OpenAI 风格，客户端按同一套结构解析。
func TestRateLimitErrorBodyIsOpenAIShape(t *testing.T) {
	limiter := NewMemoryLimiter()
	e := newTestEngine(t, limiter, model.TokenView{ID: 3, Name: "t", RPMLimit: 1})

	post(t, e)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytesReader(`{}`))
	w := httptest.NewRecorder()
	e.ServeHTTP(w, req)

	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("应 429，实际 %d", w.Code)
	}
	var body struct {
		Error struct {
			Message string `json:"message"`
			Type    string `json:"type"`
		} `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("错误体不是合法 JSON: %v (%s)", err, w.Body.String())
	}
	if body.Error.Type != "rate_limit_error" {
		t.Fatalf("错误类型应为 rate_limit_error，实际 %q", body.Error.Type)
	}
	// 消息里必须带阈值和令牌名：排查「为什么被限」时这是唯一线索
	if body.Error.Message == "" {
		t.Fatal("错误消息不能为空")
	}
}

// TestRateLimitWindowSlides：窗口滑动后计数恢复。
//
// 这是滑动窗口和固定窗口的关键差别：固定窗口在边界处会放行
// 2 倍流量（前 1 秒末 10 个 + 后 1 秒初 10 个），滑动窗口不会。
// 这里用短窗口直接验证恢复，避免测试等满 60 秒。
func TestRateLimitWindowSlides(t *testing.T) {
	// 短窗口的限流器：绕开 window 常量让测试能秒级跑完
	lim := &memoryLimiter{
		window:   100 * time.Millisecond,
		buckets:  map[string]*bucket{},
		lastSeen: map[string]time.Time{},
	}
	now := time.Now()

	// 窗口内打满：3 次全放行
	for i := 0; i < 3; i++ {
		ok, _ := lim.AllowRequest(context.Background(), "k", 3, now)
		if !ok {
			t.Fatalf("第 %d 次应放行", i+1)
		}
	}
	if ok, _ := lim.AllowRequest(context.Background(), "k", 3, now); ok {
		t.Fatal("窗口满了应该拒绝")
	}

	// 窗口滑过 100ms：最旧的记录过期，应该重新放行
	ok, _ := lim.AllowRequest(context.Background(), "k", 3, now.Add(150*time.Millisecond))
	if !ok {
		t.Fatal("窗口滑过后应恢复放行（滑动窗口必须能恢复）")
	}
}

// TestRateLimitZeroLimitMeansUnlimited：0 = 不限（与 Quota 的语义一致）。
func TestRateLimitZeroLimitMeansUnlimited(t *testing.T) {
	limiter := NewMemoryLimiter()
	e := newTestEngine(t, limiter, model.TokenView{ID: 4, Name: "t"}) // 两个阈值都是 0

	for i := 0; i < 50; i++ {
		if code, _ := post(t, e); code != http.StatusOK {
			t.Fatalf("不限流时第 %d 次请求应放行，实际 %d", i+1, code)
		}
	}
}

// TestRateLimitPerTokenIsolation：限流按令牌分桶，互不影响。
//
// 这条测的是「令牌级」相对 IP 级的价值所在：同一个 IP 上的两个
// 令牌，一个打满不该影响另一个。
func TestRateLimitPerTokenIsolation(t *testing.T) {
	limiter := NewMemoryLimiter()
	lim := limiter.(*memoryLimiter)
	base := time.Now()

	for i := 0; i < 2; i++ {
		ok, _ := lim.AllowRequest(context.Background(), tokenBucketKey(1), 2, base)
		if !ok {
			t.Fatalf("令牌 1 第 %d 次应放行", i+1)
		}
	}
	// 令牌 1 满了
	if ok, _ := lim.AllowRequest(context.Background(), tokenBucketKey(1), 2, base); ok {
		t.Fatal("令牌 1 应被限流")
	}
	// 令牌 2 不受影响
	if ok, _ := lim.AllowRequest(context.Background(), tokenBucketKey(2), 2, base); !ok {
		t.Fatal("令牌 2 不该被令牌 1 的用量影响（限流必须按令牌分桶）")
	}
}

// TestRateLimitTPMPreEstimateAndPostCorrection：TPM 请求前按预估值
// 计数、响应后按真实用量修正差额。
//
// 这是 TPM 的固有取舍（请求前拿不到真实 usage）：先按估算占位，
// 事后用 (实际 - 预估) 的差额把窗口里的总量收敛到真实值。
func TestRateLimitTPMPreEstimateAndPostCorrection(t *testing.T) {
	limiter := NewMemoryLimiter()
	lim := limiter.(*memoryLimiter)
	base := time.Now()

	// 预估 60：放行，窗口里记 60
	ok, _ := lim.AllowTokens(context.Background(), tokenBucketKey(5), 100, 60, base)
	if !ok {
		t.Fatal("预估 60 未超 100，应放行")
	}
	// 再来一次预估 60：60+60=120 > 100，应被拒
	if ok, _ := lim.AllowTokens(context.Background(), tokenBucketKey(5), 100, 60, base); ok {
		t.Fatal("预估累计超限应拒绝")
	}

	// 响应后真实用量只有 10：补差额 10-60 = -50，窗口总量回落
	lim.AddTokens(context.Background(), tokenBucketKey(5), 10-60, base)
	// 现在窗口里只有 10，还能再放一个预估 60 的
	if ok, _ := lim.AllowTokens(context.Background(), tokenBucketKey(5), 100, 60, base); !ok {
		t.Fatal("修正后应恢复放行（预估值偏大时必须能退回去）")
	}
}

// TestRateLimitRPMNotPollutedByTPMEvents：同时开 RPM 和 TPM 时，
// TPM 的记账（预估值 + 响应后补差）不能挤占 RPM 的请求数。
//
// 这条是回归测试：两个维度曾经共用一个桶，RPM 判定 len(events)
// 会把 TPM 的每一笔当成一次请求——rpm=3 的令牌实际每分钟只放过
// 1 个请求（1 笔 RPM + 1 笔预估 + 1 笔补差 = 3 笔）。手动验收
// （curl 连发）先暴露了这个行为，才补上分桶修复和本测试。
func TestRateLimitRPMNotPollutedByTPMEvents(t *testing.T) {
	limiter := NewMemoryLimiter()
	// RPM=3、TPM 放到「token 维度永远不限」的量级，只考察 RPM 是否准确
	view := model.TokenView{ID: 7, Name: "t", RPMLimit: 3, TPMLimit: 1 << 30}

	e := gin.New()
	e.POST("/v1/chat/completions", func(c *gin.Context) {
		c.Set(CtxTokenView, view)
		c.Set(CtxTokenID, view.ID)
		c.Next()
		// 模拟 relay 收尾：真实用量(30)与预估不同，会产生一笔补差记录
		FinishTokens(c, limiter, 30)
	}, TokenRateLimit(limiter), func(c *gin.Context) {
		c.String(http.StatusOK, "ok")
	})

	for i := 0; i < 3; i++ {
		if code, _ := post(t, e); code != http.StatusOK {
			t.Fatalf("第 %d 次请求应放行，实际 %d（TPM 的记账挤占了 RPM 计数）", i+1, code)
		}
	}
	code, retry := post(t, e)
	if code != http.StatusTooManyRequests {
		t.Fatalf("第 4 次请求应被 RPM 挡掉（429），实际 %d", code)
	}
	if retry == "" {
		t.Fatal("被 RPM 挡掉也必须带 Retry-After")
	}

	// RPM 桶里应恰好只有 3 笔请求记录，一笔 TPM 记录都不该混进来
	lim := limiter.(*memoryLimiter)
	lim.mu.Lock()
	n := len(lim.buckets[tokenBucketKey(7)].events)
	lim.mu.Unlock()
	if n != 3 {
		t.Fatalf("RPM 桶应恰好 3 笔请求记录，实际 %d（两个维度没有分桶）", n)
	}
}

// TestRateLimitTPMWinsWhenRPMUnlimited：RPM 不限（0）时只有 TPM
// 生效，且判据是 token 总量而不是请求笔数。
//
// 请求体 "hi" 的预估值是 1 token（3 个 ASCII 字符 × 0.25，向上取整），
// TPM=10 时恰好放 10 个请求、第 11 个被挡。若 RPM 也在偷偷计数，
// 或预估值没读到（按 0 计），放行数都会偏。
func TestRateLimitTPMWinsWhenRPMUnlimited(t *testing.T) {
	limiter := NewMemoryLimiter()
	view := model.TokenView{ID: 8, Name: "t", RPMLimit: 0, TPMLimit: 10}
	e := newTestEngine(t, limiter, view)

	allowed := 0
	for i := 0; i < 30; i++ {
		if code, _ := post(t, e); code == http.StatusOK {
			allowed++
			continue
		}
		break
	}
	if allowed != 10 {
		t.Fatalf("TPM=10、单请求预估 1 token 时应恰好放行 10 次，实际 %d", allowed)
	}
}

// TestRateLimitConcurrent：并发安全（配合 go test -race）。
//
// 限流表是共享可变状态，是最容易出数据竞争的地方。这个测试在
// -race 下跑，任何未加锁的读写都会直接失败。
func TestRateLimitConcurrent(t *testing.T) {
	limiter := NewMemoryLimiter()
	const limit = 100
	const workers = 50
	const perWorker = 10 // 总共 500 次请求，只有 100 次该放行

	var allowed int64
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < perWorker; j++ {
				ok, _ := limiter.AllowRequest(context.Background(), "concurrent", limit, time.Now())
				if ok {
					atomic.AddInt64(&allowed, 1)
				}
			}
		}()
	}
	wg.Wait()

	if got := atomic.LoadInt64(&allowed); got != limit {
		t.Fatalf("并发下放行数应恰好等于上限 %d，实际 %d（多了是竞态，少了是锁粒度问题）", limit, got)
	}
}

// TestRateLimitConcurrentTPM：TPM 的并发计数同样要安全。
func TestRateLimitConcurrentTPM(t *testing.T) {
	limiter := NewMemoryLimiter()
	const limit = 1000
	const workers = 20

	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				limiter.AllowTokens(context.Background(), "tpm", limit, 10, time.Now())
			}
		}()
	}
	wg.Wait()

	// 窗口里的总量必须恰好是 1000（上限），既没多算也没少算
	lim := limiter.(*memoryLimiter)
	lim.mu.Lock()
	used := 0
	for _, ev := range lim.buckets["tpm"].events {
		used += ev.n
	}
	lim.mu.Unlock()
	if used != limit {
		t.Fatalf("TPM 窗口内总量应恰好等于上限 %d，实际 %d", limit, used)
	}
}

// TestIPRateLimit：IP 级兜底限流。
func TestIPRateLimit(t *testing.T) {
	limiter := NewMemoryLimiter()
	e := gin.New()
	e.POST("/v1/chat/completions", IPRateLimit(limiter, 2), func(c *gin.Context) {
		c.String(http.StatusOK, "ok")
	})

	do := func() (int, string) {
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytesReader(`{}`))
		req.RemoteAddr = "203.0.113.7:12345"
		w := httptest.NewRecorder()
		e.ServeHTTP(w, req)
		return w.Code, w.Header().Get("Retry-After")
	}

	for i := 0; i < 2; i++ {
		if code, _ := do(); code != http.StatusOK {
			t.Fatalf("IP 限流下第 %d 次应放行，实际 %d", i+1, code)
		}
	}
	code, retry := do()
	if code != http.StatusTooManyRequests {
		t.Fatalf("IP 超限应 429，实际 %d", code)
	}
	if retry == "" {
		t.Fatal("IP 级 429 同样要带 Retry-After")
	}
}

// TestIPRateLimitZeroMeansUnlimited：IP 级 0 也不限。
func TestIPRateLimitZeroMeansUnlimited(t *testing.T) {
	limiter := NewMemoryLimiter()
	e := gin.New()
	e.POST("/v1/chat/completions", IPRateLimit(limiter, 0), func(c *gin.Context) {
		c.String(http.StatusOK, "ok")
	})

	for i := 0; i < 20; i++ {
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytesReader(`{}`))
		req.RemoteAddr = "203.0.113.8:12345"
		w := httptest.NewRecorder()
		e.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("IP 级不限时第 %d 次应放行，实际 %d", i+1, w.Code)
		}
	}
}

// TestTokenAndIPKeyNamespacesDontCollide：令牌 key 和 IP key 不能串。
//
// 两边共用同一个内存表，前缀是唯一的隔离手段。令牌 ID 是数字、
// IP 是点分十进制，看起来不会撞，但「tok:1」和「ip:1」这种
// 形状相同的 key 一旦前缀设计错了就会互相顶掉计数。
func TestTokenAndIPKeyNamespacesDontCollide(t *testing.T) {
	lim := &memoryLimiter{
		buckets:  map[string]*bucket{},
		lastSeen: map[string]time.Time{},
	}
	now := time.Now()

	if tokenBucketKey(1) == ipBucketKey("1") {
		t.Fatal("令牌 key 与 IP key 不得相同")
	}
	// 打满令牌 1 的配额
	for i := 0; i < 3; i++ {
		lim.AllowRequest(context.Background(), tokenBucketKey(1), 3, now)
	}
	if ok, _ := lim.AllowRequest(context.Background(), tokenBucketKey(1), 3, now); ok {
		t.Fatal("令牌 1 应被限流")
	}
	// IP 1 仍有完整配额
	if ok, _ := lim.AllowRequest(context.Background(), ipBucketKey("1"), 3, now); !ok {
		t.Fatal("IP 的计数不该被令牌的用量占用（key 命名空间串了）")
	}
}

// TestRateLimitEvictsOldKeys：内存表满时淘汰最久未更新的 key。
//
// 不淘汰的话，一个换着 IP 打的扫描器就能把表撑爆——IP 级限流
// 的 key 是客户端 IP，数量天然无界（令牌 ID 才有界）。
func TestRateLimitEvictsOldKeys(t *testing.T) {
	lim := &memoryLimiter{
		buckets:  map[string]*bucket{},
		lastSeen: map[string]time.Time{},
	}
	now := time.Now()

	// 塞满 maxKeys 个不同的 key
	for i := 0; i < maxKeys; i++ {
		lim.AllowRequest(context.Background(), fmt.Sprintf("ip:%d", i), 100, now)
	}
	// 再触发一个新的 key，必须淘汰掉一个最旧的
	lim.AllowRequest(context.Background(), "ip:new", 100, now)

	lim.mu.Lock()
	size := len(lim.buckets)
	lim.mu.Unlock()
	if size > maxKeys {
		t.Fatalf("内存表应被控制在 %d 条以内，实际 %d（不淘汰会被扫描器撑爆）", maxKeys, size)
	}
	if size != maxKeys {
		t.Fatalf("淘汰后应恰好保持 %d 条，实际 %d", maxKeys, size)
	}
}

// TestFinishTokensAppliesRealUsage：FinishTokens 把预估值修正成真实值。
func TestFinishTokensAppliesRealUsage(t *testing.T) {
	limiter := NewMemoryLimiter()
	view := model.TokenView{ID: 9, Name: "t", TPMLimit: 1000}

	e := gin.New()
	var sawEstimate bool
	e.POST("/v1/chat/completions", func(c *gin.Context) {
		c.Set(CtxTokenView, view)
		c.Set(CtxTokenID, view.ID)
		c.Next()
		// 模拟 relay 的收尾：拿真实用量修正窗口
		if _, ok := c.Get(ctxTPMEstimate); ok {
			sawEstimate = true
		}
		FinishTokens(c, limiter, 42)
	}, TokenRateLimit(limiter), func(c *gin.Context) {
		c.String(http.StatusOK, "ok")
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		bytesReader(`{"model":"gpt-4o-mini","messages":[{"role":"user","content":"hello world"}]}`))
	w := httptest.NewRecorder()
	e.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("应放行，实际 %d", w.Code)
	}
	if !sawEstimate {
		t.Fatal("TPM 开启时应把预估值写进上下文，供响应后修正")
	}

	// 窗口里应只剩 (真实 - 预估) 的差额，而不是两笔独立的记录。
	// 注意查的是 TPM 专用桶（tok:9:tpm），不是 RPM 桶（tok:9）——
	// 两个维度分桶后 RPM 的数不会被 token 量污染。
	lim := limiter.(*memoryLimiter)
	lim.mu.Lock()
	total := 0
	for _, ev := range lim.buckets[tpmBucketKey(9)].events {
		total += ev.n
	}
	lim.mu.Unlock()
	if total != 42 {
		t.Fatalf("窗口内 token 总量应收敛到真实值 42，实际 %d", total)
	}
}

// TestRateLimitSkippedWithoutTokenView：没挂 Auth 时令牌级限流放行。
//
// 装配错误（把限流挂在了 Auth 之前）不该让正常请求全被挡掉——
// 限流是保护措施，用一个 bug 换另一个 bug 没有意义。
func TestRateLimitSkippedWithoutTokenView(t *testing.T) {
	limiter := NewMemoryLimiter()
	e := gin.New()
	e.POST("/v1/chat/completions", TokenRateLimit(limiter), func(c *gin.Context) {
		c.String(http.StatusOK, "ok")
	})

	for i := 0; i < 5; i++ {
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytesReader(`{}`))
		w := httptest.NewRecorder()
		e.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("缺少令牌投影时应放行（限流不该因装配错误挡掉请求），实际 %d", w.Code)
		}
	}
}

// TestLimiterClosedRejects：关闭后一律拒绝。
//
// 优雅关闭时先 srv.Shutdown 等在途请求，这期间不该再有新请求
// 挤进来重新计数——否则窗口里会残留一批「进程马上要死了却还
// 算过」的记录，重启后计数从零开始反而更干净。
func TestLimiterClosedRejects(t *testing.T) {
	limiter := NewMemoryLimiter()
	if ok, _ := limiter.AllowRequest(context.Background(), "k", 10, time.Now()); !ok {
		t.Fatal("关闭前应放行")
	}
	limiter.(*memoryLimiter).Close()
	if ok, _ := limiter.AllowRequest(context.Background(), "k", 10, time.Now()); ok {
		t.Fatal("关闭后应拒绝")
	}
	// 补记也要停：进程都要死了，再改窗口没有意义
	limiter.AddTokens(context.Background(), "k", 5, time.Now())
	lim := limiter.(*memoryLimiter)
	lim.mu.Lock()
	size := len(lim.buckets)
	lim.mu.Unlock()
	if size != 0 {
		t.Fatalf("关闭后内存表应清空，实际 %d 条", size)
	}
}

// TestRateLimitCanceledContextDoesNotCount：客户端已断开时不占配额。
//
// 请求还没发出去客户端就撤了，这次计数留在窗口里纯属浪费——
// 窗口是给「真正打到了上游」的请求准备的。
func TestRateLimitCanceledContextDoesNotCount(t *testing.T) {
	limiter := NewMemoryLimiter()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if ok, _ := limiter.AllowRequest(ctx, "k", 10, time.Now()); ok {
		t.Fatal("上下文已取消应拒绝（没人等这个结果，不该占配额）")
	}
	lim := limiter.(*memoryLimiter)
	lim.mu.Lock()
	// 取消的请求在计数之前就被拦掉了，连 bucket 都不该建出来
	n := 0
	if b, ok := lim.buckets["k"]; ok {
		n = len(b.events)
	}
	lim.mu.Unlock()
	if n != 0 {
		t.Fatalf("取消的请求不该计数，实际记了 %d 笔", n)
	}
}
