package access

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"
)

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
