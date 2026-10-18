package middleware

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"minigateway/internal/access"
	"minigateway/internal/billing"
	"minigateway/internal/model"
	"minigateway/internal/protocol"
)

// 本文件实现请求前限流（P6 批次 3）。
//
// 为什么需要它（与 P5 计费的分工）：
// P5 的额度扣减是「响应后结算」——请求先打到上游、拿到真实用量才扣钱。
// 这个模型防不住「1000 个并发在结算前全部打到上游」：结算发生在响应
// 之后，那之前的上游调用早就把真实的 Key 烧掉了。额度管的是「花多少
// 钱」，限流管的是「来多快」，两者互补，谁也替代不了谁。
//
// 实现取舍：先做单机内存版（自用够用），但把接口抽出来，
// 多机部署时换一个 Redis 实现即可，中间件本身不用改。

// window 是限流窗口长度。RPM/TPM 的「每分钟」就是 60 秒。
const window = time.Minute

// maxKeys and maxBucketEvents bound limiter memory. Live windows are never
// evicted to admit a new identity; full tables reject conservatively.
const maxKeys = 4096
const maxBucketEvents = 4096

// ErrLimiterClosed 表示限流器已关闭，拒绝继续计数。
//
// 用错误而不是静默放行：调用方（中间件）需要在关闭时快速返回，
// 而「关闭后还放行」会让优雅关闭期间的请求绕过限流。
var ErrLimiterClosed = errors.New("限流器已关闭")

// Limiter 是限流器的接口。当前只有一个内存实现（memoryLimiter），
// 定义接口是为了多机部署时能换成 Redis 实现而不动中间件。
//
// 接口刻意做得很小（三个方法），因为 Redis 版的每个方法都要对应一条
// 命令或一段 Lua；方法越多，替换实现时要对齐的语义越多。
type Limiter = access.Limiter

// event 是滑动窗口里的一次计数记录。
type event struct {
	// at 是这次计数发生的时刻。
	at time.Time
	// n 是计入的量：请求计数为 1，token 计数为当时的 token 数。
	// 补记时用「实际 - 预估」的差额，可能是负数。
	n int
}

// bucket 是单个 key 的滑动窗口。
type bucket struct {
	// events 按时间升序保存窗口内的全部计数。
	// 用切片而不是环形缓冲：自用场景下每个 key 的窗口内事件数就是
	// 几十条（限 10 RPM 时最多 10 条），切片的追加和裁剪都是 O(n) 小常数；
	// 环形缓冲要处理索引回绕和过期段的跳过，复杂度换不来收益。
	events []event
}

// memoryLimiter 是 Limiter 的内存实现（默认实现）。
type memoryLimiter struct {
	mu sync.Mutex
	// window 是滑动窗口长度，默认 60 秒（RPM/TPM 的「每分钟」）。
	// 做成字段而不是只用常量：测试要用短窗口秒级验证「窗口滑动后
	// 恢复」，不能靠常量就必然要等满 60 秒。
	window   time.Duration
	buckets  map[string]*bucket
	lastSeen map[string]time.Time
	closed   bool
}

// NewMemoryLimiter 创建内存限流器（默认实现）。
func NewMemoryLimiter() Limiter {
	return &memoryLimiter{
		window:   window,
		buckets:  make(map[string]*bucket),
		lastSeen: make(map[string]time.Time),
	}
}

// Close 关闭限流器。之后的 Allow* 一律拒绝。
//
// 为什么需要关闭语义：优雅关闭时先 srv.Shutdown 等在途请求，
// 这期间不该再有新请求挤进来重新计数——否则窗口里会残留一批
// 「进程马上要死了却还算过」的记录，重启后计数从零开始反而更干净。
func (m *memoryLimiter) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closed = true
	m.buckets = make(map[string]*bucket)
	m.lastSeen = make(map[string]time.Time)
}

// AllowRequest 见 Limiter 接口。
func (m *memoryLimiter) AllowRequest(ctx context.Context, key string, limit int, now time.Time) (bool, int) {
	if limit <= 0 {
		// 0 = 不限，与令牌额度的语义一致。直接放行且不计数：
		// 不限的 key 不该占内存表的位置（那正是 maxKeys 要防的）。
		return true, 0
	}
	if err := ctx.Err(); err != nil {
		// 客户端已经断开：这次请求没人等结果，放行只会白白占一个配额位。
		return false, 0
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return false, 0
	}
	b := m.touch(key, now)
	if b == nil {
		return false, 1
	}
	b.prune(now.Add(-m.windowDuration()))
	if len(b.events) >= limit || len(b.events) >= maxBucketEvents {
		// 窗口满了。还要等多久 = 最旧那条记录滑出窗口的时间。
		oldest := b.events[0]
		if len(b.events) >= limit {
			oldest = b.events[len(b.events)-limit]
		}
		return false, m.retryAfter(oldest.at, now)
	}
	b.events = append(b.events, event{at: now, n: 1})
	return true, 0
}

// AllowTokens 见 Limiter 接口。
//
// 请求前只能按请求体**预估** token 数（billing.EstimateTokens），
// 响应后再用真实用量修正。取舍：预估偏高会提前触发限流（用户感觉
// 「被误伤」），预估偏低会放行超量请求。两个方向都选「宁可少放行」
// ——限流的目的是保护上游 Key，误伤的代价只是客户端重试一次。
func (m *memoryLimiter) AllowTokens(ctx context.Context, key string, limit, tokens int, now time.Time) (bool, int) {
	if limit <= 0 {
		return true, 0
	}
	if tokens < 0 {
		tokens = 0
	}
	if err := ctx.Err(); err != nil {
		return false, 0
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return false, 0
	}
	b := m.touch(key, now)
	if b == nil {
		return false, 1
	}
	b.prune(now.Add(-m.windowDuration()))
	if len(b.events) >= maxBucketEvents {
		return false, m.retryAfter(b.events[0].at, now)
	}
	used := 0
	for _, e := range b.events {
		used += e.n
	}
	if used+tokens > limit {
		// 预估量已经超了。等多久要看「哪条记录滑出后能腾出足够的量」，
		// 但逐个试算是 O(n)；自用场景下统一按最旧记录的过期时间给，
		// 客户端在这个时间之后重试大概率能过（窗口是滚动的）。
		if len(b.events) == 0 {
			return false, 1
		}
		oldest := b.events[0]
		return false, m.retryAfter(oldest.at, now)
	}
	b.events = append(b.events, event{at: now, n: tokens})
	return true, 0
}

// AddTokens 在响应后补记差额。
//
// 为什么是「差额」而不是「直接覆盖」：放行时已经把预估值记进窗口了，
// 再记一次全量会让窗口里的量翻倍。记 (实际 - 预估) 的差额，
// 无论预估值偏高还是偏低，窗口里的总量都收敛到真实值。
func (m *memoryLimiter) AddTokens(ctx context.Context, key string, delta int, now time.Time) {
	if delta == 0 {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return
	}
	b := m.touch(key, now)
	if b == nil {
		slog.Warn("token usage tracker full")
		return
	}
	b.prune(now.Add(-m.windowDuration()))
	if len(b.events) >= maxBucketEvents {
		b.events[len(b.events)-1].n += delta
		b.events[len(b.events)-1].at = now
	} else {
		b.events = append(b.events, event{at: now, n: delta})
	}
}

// touch 取（或建）一个 key 的窗口，并更新它的最近访问时间。
// 调用方必须已持有锁。
func (m *memoryLimiter) touch(key string, now time.Time) *bucket {
	b, ok := m.buckets[key]
	if !ok {
		// 建新条目前先清理过期桶：否则一次突发的新 key 洪水
		// （换着 IP 扫描）就能把内存表撑到 maxKeys 之外。
		if len(m.buckets) >= maxKeys {
			m.evictExpired(now)
			if len(m.buckets) >= maxKeys {
				return nil
			}
		}
		b = &bucket{}
		m.buckets[key] = b
	}
	m.lastSeen[key] = now
	return b
}

// evictExpired only reclaims expired windows; evicting a live identity would
// silently reset its limit. If all windows are live, admission fails closed.
func (m *memoryLimiter) evictExpired(now time.Time) {
	for key, b := range m.buckets {
		b.prune(now.Add(-m.windowDuration()))
		if len(b.events) == 0 {
			delete(m.buckets, key)
			delete(m.lastSeen, key)
		}
	}
}

// prune 丢掉窗口外的记录。
func (b *bucket) prune(cutoff time.Time) {
	if len(b.events) == 0 {
		return
	}
	// 记录按时间升序，从头找到第一条还在窗口内的即可。
	// 大多数调用 prune 时一条都不用删（窗口远未满），
	// 这个循环的开销就是一次比较。
	i := 0
	for i < len(b.events) && !b.events[i].at.After(cutoff) {
		i++
	}
	if i > 0 {
		b.events = append(b.events[:0], b.events[i:]...)
	}
}

// retryAfter 算出「target 这条记录滑出窗口后还要等多少秒」，至少 1 秒。
//
// 至少 1 秒：Retry-After: 0 会让客户端立刻重试，把限流变成
// 无间隔的忙循环，比不限流更糟。
//
// 做成 memoryLimiter 的方法而不是自由函数：窗口长度是实例属性
// （测试里会用短窗口），自由函数拿不到它。
func (m *memoryLimiter) windowDuration() time.Duration {
	if m.window <= 0 {
		return window
	}
	return m.window
}
func (m *memoryLimiter) retryAfter(target, now time.Time) int {
	d := target.Add(m.windowDuration()).Sub(now)
	secs := int(d.Seconds())
	if secs < 1 {
		return 1
	}
	return secs
}

// TokenRateLimit 是令牌级限流中间件（RPM + TPM）。
//
// 必须挂在 Auth **之后**：它要按令牌 ID 分桶，而令牌 ID 是 Auth
// 写进上下文的。挂在 Auth 之前拿不到令牌，只能退化成 IP 级限流，
// 「每个客户端一把钥匙」的粒度就丢了。
func TokenRateLimit(limiter Limiter) gin.HandlerFunc {
	return func(c *gin.Context) {
		if limiter == nil {
			c.Next()
			return
		}
		view, ok := tokenViewFromContext(c)
		if !ok {
			// 没挂 Auth（或 Auth 没写投影）：令牌级限流无从谈起。
			// 放行而不是拒绝——限流是保护措施，装配错误不该让它
			// 把正常请求全挡掉，那等于用一个 bug 换另一个 bug。
			c.Next()
			return
		}
		key := tokenBucketKey(view.ID)
		now := time.Now()

		// RPM 先判：请求数是「肯定要花的」，token 数只是预估。
		// 顺序反过来会让 RPM 超限的请求先占用一个 token 计数位。
		if view.RPMLimit > 0 {
			allowed, retry := limiter.AllowRequest(c.Request.Context(), key, view.RPMLimit, now)
			if !allowed {
				abortRateLimited(c, retry, fmt.Sprintf(
					"令牌 %q 每分钟请求数超过上限 %d", view.Name, view.RPMLimit))
				return
			}
		}
		if view.TPMLimit > 0 {
			// 请求前只能按请求体预估 token 数，真实 usage 要等上游响应。
			// 预估值先占位，响应后由 FinishTokens 用真实用量修正差额。
			//
			// TPM 必须用自己的桶（令牌桶 key 加 ":tpm" 后缀）：RPM 的
			// 判定是 len(events)（数「笔数」），而 TPM 记的是「token
			// 量」外加响应后补记的差额笔。两个维度共用一个桶时，TPM
			// 的每一笔都会被 RPM 当成一次请求数进去——一个同时开了
			// RPM=3 和 TPM 的令牌，实际每分钟只能过 1 个请求
			// （1 笔 RPM + 1 笔预估 + 1 笔补差 = 3 笔，第 2 个请求
			// 就被 RPM 挡掉）。
			est := estimatePromptTokens(c)
			tpmKey := tpmBucketKey(view.ID)
			allowed, retry := limiter.AllowTokens(c.Request.Context(), tpmKey, view.TPMLimit, est, now)
			if !allowed {
				abortRateLimited(c, retry, fmt.Sprintf(
					"令牌 %q 每分钟 token 数超过上限 %d（本次预估 %d）", view.Name, view.TPMLimit, est))
				return
			}
			// 把「这次已经按 est 记过了」存进上下文，响应后拿真实用量
			// 补差额。用上下文而不是闭包变量：中间件和补记逻辑
			// 在同一个请求作用域里，上下文是这个项目既有的做法
			// （见 relay 的 relayStatusKey 那套）。
			c.Request = c.Request.WithContext(access.WithTokenReservation(c.Request.Context(), view.ID, est))
			c.Set(ctxTPMEstimate, est)
		}
		c.Request = c.Request.WithContext(access.WithRateAdmission(c.Request.Context(), view.ID))
		c.Next()
	}
}

// FinishTokens 在响应结束后补记真实 token 用量。
//
// 由 relay 的收尾逻辑调用（它才知道真实 usage）。请求前记的是预估值，
// 这里把「真实 - 预估」的差额补进窗口，让 TPM 收敛到实际值。
//
// 放在 relay 侧调用而不是中间件里 defer：流式响应的 usage 只在流
// 结束时才知道，中间件的 defer 在 c.Next() 返回时就跑了，拿不到。
func FinishTokens(c *gin.Context, limiter Limiter, actualTokens int) {
	access.FinishTokens(c.Request.Context(), limiter, actualTokens)
}

// IPRateLimit 是 IP 级限流中间件，兜底防「没有令牌的暴力探测」。
//
// 为什么还需要一层：令牌级限流管得住「有令牌的滥用」，管不住
// 「拿一堆随机 key 试」——那种请求根本过不了 Auth，也就到不了
// 令牌级限流那一步，但每一次都实打实打了网关（查库、比对）。
// 按来源 IP 限流是这条路径上唯一的闸门。
//
// 挂在 Auth **之前**：它不依赖令牌信息，越早拦住越省。
func IPRateLimit(limiter Limiter, limit int) gin.HandlerFunc {
	return func(c *gin.Context) {
		if limiter == nil || limit <= 0 {
			c.Next()
			return
		}
		allowed, retry := limiter.AllowRequest(c.Request.Context(), ipBucketKey(c.ClientIP()), limit, time.Now())
		if !allowed {
			abortRateLimited(c, retry, fmt.Sprintf(
				"IP %s 每分钟请求数超过上限 %d", c.ClientIP(), limit))
			return
		}
		c.Next()
	}
}

// tokenBucketKey 拼出令牌级限流的 key。
//
// 为什么带 "tok:" 前缀而不是直接用 ID 字符串：IP 级限流用的是
// "ip:<addr>"，两种 key 在同一个内存表里，前缀把它们隔开，
// 免得一个数字形状的令牌 ID 撞上一个数字形状的 IP。
func tokenBucketKey(tokenID uint) string {
	return access.TokenBucketKey(tokenID)
}

// tpmBucketKey 拼出令牌级 TPM 限流的 key（令牌桶 key + ":tpm" 后缀）。
//
// 为什么 TPM 不能和 RPM 共用一个桶：RPM 判定的是 len(events)
// （「几笔请求」），TPM 记的是「多少 token」外加响应后补记的
// 差额笔。共用一个桶时 TPM 的每一笔都会被 RPM 当成一次请求，
// 两个维度的阈值会互相挤占（详见 TokenRateLimit 里的说明）。
// 后缀而不是前缀：tok:3:tpm 与 tok:3 同属令牌 3，肉眼可辨。
func tpmBucketKey(tokenID uint) string {
	return access.TokenUsageBucketKey(tokenID)
}

// ipBucketKey 拼出 IP 级限流的 key。
func ipBucketKey(ip string) string {
	return "ip:" + ip
}

// ctxTPMEstimate 存放「本次请求已按多少预估值记过 TPM」。
const ctxTPMEstimate = "tpm_estimate"

// tokenViewFromContext 取 Auth 写入的令牌投影。
func tokenViewFromContext(c *gin.Context) (model.TokenView, bool) {
	if p, ok := access.PrincipalFromContext(c.Request.Context()); ok {
		return model.TokenView{ID: p.TokenID, UserID: p.UserID, Name: p.TokenName, Policy: p.Policy, Models: p.Policy.Models, RPMLimit: p.RPMLimit, TPMLimit: p.TPMLimit}, true
	}

	v, ok := c.Get(CtxTokenView)
	if !ok {
		return model.TokenView{}, false
	}
	view, ok := v.(model.TokenView)
	return view, ok
}

// estimatePromptTokens 按请求体预估 prompt token 数。
//
// 复用 billing 包的字符估算（与 P5 的估算降级同一套逻辑）：
// 限流只需要一个量级正确的数，不值得为它引入真 tokenizer。
// 读不到 body（已经消费过/不是 JSON）时按 0 计——只影响 TPM
// 的这一次判定，RPM 仍然生效。
type replayedBody struct {
	io.Reader
	io.Closer
}

func estimatePromptTokens(c *gin.Context) int {
	if c.Request.Body == nil {
		return 0
	}
	original := c.Request.Body
	body, err := io.ReadAll(io.LimitReader(original, estimateBodyLimit))
	c.Request.Body = &replayedBody{Reader: io.MultiReader(bytes.NewReader(body), original), Closer: original}
	if err != nil {
		return 0
	}
	// 把读走的字节装回去：relay 的 handler 还要原样透传 body，
	// 中间件把它吃掉等于破坏了「透明代理」这个基本承诺。

	return billing.EstimateTokens(protocol.PromptTextFromBody(body))
}

// estimateBodyLimit 是估算时最多读多少字节。
//
// 限流只需要 prompt 的量级，读整个 body 是浪费；但截断了又怕
// 估少了。64KB 对绝大多数 prompt 足够，超出的部分按「已读部分」
// 估算，方向是低估——由响应后的真实用量补记修正。
const estimateBodyLimit = 64 * 1024

// abortRateLimited 回 429 + Retry-After。
//
// 错误格式沿用网关统一的 OpenAI 风格，和 relay.writeError 保持一致：
// 客户端的 SDK 能按同一套结构解析，不用为限流单独写一套。
func abortRateLimited(c *gin.Context, retryAfterSec int, msg string) {
	if retryAfterSec < 1 {
		retryAfterSec = 1
	}
	c.Header("Retry-After", strconv.Itoa(retryAfterSec))
	c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{
		"error": gin.H{"message": msg, "type": "rate_limit_error"},
	})
	// warn 而不是 info：限流意味着有客户端在超配额使用，属于
	// 需要人看一眼的事件；但它不是故障（网关在正常工作），
	// 所以不到 error。
	slog.Warn("[限流] 请求被限流",
		"method", c.Request.Method, "path", c.Request.URL.Path,
		"request_id", c.GetString(CtxRequestID), "reason", msg)
}
