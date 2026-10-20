// Package metrics 提供进程内的 Prometheus 指标：转发路径上做原子计数，
// /metrics 抓取时按 exposition 纯文本格式输出。
//
// 为什么手写 exposition 而不引 prometheus/client_golang：
//  1. 本项目从 P1 起就刻意把第三方依赖压在「gin + gorm + sqlite +
//     yaml」四个，每一个新依赖都是供应链面和升级面。可观测性属于
//     「锦上添花」，不值得为它破例；
//  2. 所需指标全是「带标签的 counter / histogram / gauge」，
//     exposition 格式是个几十行的纯文本协议（# HELP/# TYPE +
//     name{label="v"} value），自己写出来的代码量少于引入依赖后的
//     API 学习和调优成本；
//  3. 手写版只覆盖「本网关自己埋的点」，不需要 client_golang 的
//     collector 注册表、默认 Go 运行时指标、推送网关那些能力。
//     等真要多协议暴露或上 Pushgateway 时，再换成官方库——本包
//     只被 relay 和 server 各调一两个函数，替换成本很低。
//
// 已知的取舍（写清楚，免得后人以为是漏了）：
//   - 没有 Go 运行时指标（goroutine 数、GC 暂停）。真要的话可以之后
//     在 Render 里加一段 runtime.MemStats 的 gauge，格式完全一样；
//   - 标签基数靠调用方自律：model × channel × status 的组合数是
//     有限的（渠道数十、模型数十），不会像「按 request_id 打标签」
//     那样爆炸。这一点必须在新增埋点时守住。
//
// 并发模型：埋点路径只有 map 读（RWMutex 读锁）+ 计数器 atomic 加，
// 没有任何写在请求路径上抢锁——转发路径加锁会把 P2/P5 好不容易
// 优化掉的延迟又加回来。写锁只在「首次见到某个标签组合」时出现，
// 属于极低频。
package metrics

import (
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// 指标名。抽成常量而不是散落字符串：拼错的指标名在 Prometheus 侧
// 表现为「查询不到」而不是报错，是最难发现的一类埋点故障。
const (
	MetricRequestsTotal   = "gateway_requests_total"
	MetricDurationSeconds = "gateway_request_duration_seconds"
	MetricTokensTotal     = "gateway_tokens_total"
	MetricCostTotal       = "gateway_cost_total"
	MetricCredentials     = "gateway_credentials"
	MetricCredFailures    = "gateway_credential_failures_total"
	MetricAttemptsTotal   = "gateway_attempts_total"
	MetricAttemptDuration = "gateway_attempt_duration_seconds"
	MetricRouteSelections = "gateway_route_selections_total"
	MetricRouteDuration   = "gateway_route_selection_duration_seconds"
)

// TokenTypePrompt / TokenTypeCompletion 是 gateway_tokens_total 的
// type 标签取值。用常量保证埋点和文档不会写成 prompt/completions
// 两种拼法（标签拼错 = 查询静默返回空）。
const (
	TokenTypePrompt     = "prompt"
	TokenTypeCompletion = "completion"
)

// DurationBuckets 是耗时直方图的分桶（秒）。
//
// 按 LLM 转发的真实分布挑的：绝大多数非流式请求在 1s 内（前四档），
// 流式请求和长上下文落在 1~30s，60s 以上基本等于要告警了。
// 固定桶而不是指数自动分桶：分桶形状固定才能跨时间比较 p99 的变化，
// 而且抓取端不用算对数。
var DurationBuckets = []float64{0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60}

var routeDurationBuckets = []float64{0.000001, 0.00001, 0.0001, 0.001, 0.01, 0.05, 0.1, 1}

// CredentialStat 是「某渠道下某状态的凭据数」的一条 gauge 样本。
//
// 由 relay 从内存渠道池快照算出后交给 SetCredentialStats——metrics
// 不该知道渠道池怎么存（和 relay.SetLogRecorder 同一套解耦思路）。
type CredentialStat struct {
	Channel string
	Status  string
	Count   int64
}

// Registry 持有全部指标。包级默认实例 + Default() 供 relay 直接调，
// 测试里用 New() 拿独立实例避免互相污染。
type Registry struct {
	mu sync.RWMutex
	// 三个 map 的 value 都是指针，取出来之后做 atomic 操作不需要
	// 再持锁——这是「请求路径不加锁」的实现基础。
	requests   map[string]*counter
	durations  map[string]*histogram
	tokens     map[string]*counter
	costs      map[string]*counter
	credFail   map[string]*counter
	credential map[string]*gauge

	// credStats 由外部注入的凭据状态快照（SetCredentialStats）。
	// gauge 类型的指标值只能由「当前状态」决定，不能靠增量累加，
	// 所以这里存快照而不是累加器。
	credStats        func() []CredentialStat
	logStats         func() LogQueueStats
	runtimeStats     func() RuntimeStats
	attempts         map[string]*counter
	attemptDurations map[string]*histogram
	routeSelections  map[string]*counter
	routeDurations   map[string]*histogram
	shadowRecorded   atomic.Bool
	shadowReasons    [11]atomic.Uint64
	shadowSamples    [2]atomic.Uint64
}

var shadowReasons = [...]string{"compared_equal", "compared_different", "disabled", "pinned", "pool_size", "slots_busy", "stale_epoch", "retired_active", "untracked_active", "saturated", "unknown"}

// RecordShadowComparison accepts only fixed aggregate reasons, never target labels.
// Eligible means enabled, unpinned, pool size 2..8, before state checks.
func (r *Registry) RecordShadowComparison(reason string) {
	index := len(shadowReasons) - 1
	switch reason {
	case "compared_equal":
		index = 0
	case "compared_different":
		index = 1
	case "disabled":
		index = 2
	case "pinned":
		index = 3
	case "pool_size":
		index = 4
	case "slots_busy":
		index = 5
	case "stale_epoch":
		index = 6
	case "retired_active":
		index = 7
	case "untracked_active":
		index = 8
	case "saturated":
		index = 9
	}
	r.shadowReasons[index].Add(1)
	r.shadowRecorded.Store(true)
}

// RecordShadowSample records a bounded aggregate HTTP observation.
func (r *Registry) RecordShadowSample(errorSample bool) {
	index := 0
	if errorSample {
		index = 1
	}
	r.shadowSamples[index].Add(1)
	r.shadowRecorded.Store(true)
}

func (r *Registry) renderShadow(b *strings.Builder) {
	if !r.shadowRecorded.Load() {
		return
	}
	var counts [11]uint64
	var eligible uint64
	for i := range counts {
		counts[i] = r.shadowReasons[i].Load()
		if i < 2 || i >= 5 && i < 10 {
			eligible += counts[i]
		}
	}
	writeFamilyHeader(b, "gateway_shadow_comparisons_total", "counter", "Shadow comparison outcomes with fixed reasons")
	for i, reason := range shadowReasons {
		fmt.Fprintf(b, "gateway_shadow_comparisons_total{reason=\"%s\"} %d\n", reason, counts[i])
	}
	for _, metric := range []struct {
		name, help string
		value      uint64
	}{
		{"gateway_shadow_eligible_total", "Enabled unpinned comparisons with pool size 2..8 before state checks", eligible},
		{"gateway_shadow_compared_total", "Shadow comparisons evaluated", counts[0] + counts[1]},
		{"gateway_shadow_matched_total", "Shadow comparisons matching the authoritative target", counts[0]},
	} {
		writeFamilyHeader(b, metric.name, "counter", metric.help)
		fmt.Fprintf(b, "%s %d\n", metric.name, metric.value)
	}
	writeFamilyHeader(b, "gateway_shadow_samples_total", "counter", "Shadow HTTP samples with fixed outcomes")
	fmt.Fprintf(b, "gateway_shadow_samples_total{outcome=\"clean_http_200\"} %d\n", r.shadowSamples[0].Load())
	fmt.Fprintf(b, "gateway_shadow_samples_total{outcome=\"http_error\"} %d\n", r.shadowSamples[1].Load())
}

type LogQueueStats struct {
	Depth    int
	Capacity int
	Dropped  uint64
	Failed   uint64
}

func (r *Registry) SetLogStatsProvider(fn func() LogQueueStats) {
	r.mu.Lock()
	r.logStats = fn
	r.mu.Unlock()
}

// Attempts count executed upstream tries, not local admission rejections.
func (r *Registry) RecordAttempt(channel, termination string, status int, duration time.Duration) {
	if channel == "" {
		channel = "-"
	}
	r.counterFor(&r.attempts, label(MetricAttemptsTotal, "channel", channel, "termination", termination, "status", strconv.Itoa(status))).Inc()
	r.histogramFor(&r.attemptDurations, label(MetricAttemptDuration, "channel", channel)).Observe(duration.Seconds())
}

// RecordRouteSelection observes target selection, before attempt admission or network I/O.
func (r *Registry) RecordRouteSelection(outcome, picker string, duration time.Duration) {
	switch outcome {
	case "selected", "no_candidates", "no_usable_credentials", "credential_selection_failed",
		"model_unavailable", "capability_unavailable", "continuation_unavailable":
	default:
		outcome = "unknown"
	}
	switch picker {
	case "none", "weighted", "extension", "weighted_fallback":
	default:
		picker = "unknown"
	}
	if duration < 0 {
		duration = 0
	}
	r.counterFor(&r.routeSelections, label(MetricRouteSelections, "outcome", outcome, "picker", picker)).Inc()
	r.histogramFor(&r.routeDurations, label(MetricRouteDuration, "picker", picker)).Observe(duration.Seconds())
}

// Default 是进程内唯一的指标注册表。
var Default = New()

// New 建一个空注册表。
func New() *Registry {
	return &Registry{
		requests:         map[string]*counter{},
		durations:        map[string]*histogram{},
		tokens:           map[string]*counter{},
		costs:            map[string]*counter{},
		credFail:         map[string]*counter{},
		credential:       map[string]*gauge{},
		attempts:         map[string]*counter{},
		attemptDurations: map[string]*histogram{},
		routeSelections:  map[string]*counter{},
		routeDurations:   map[string]*histogram{},
	}
}

// SetCredentialStatsProvider 注入「当前凭据状态分布」的提供者。
//
// 注入而不是 metrics 自己查库/扫渠道池：metrics 是叶子包，它连
// relay 和 store 的存在都不该知道。provider 在每次抓取时调用，
// 所以状态永远是实时的，不需要额外的失效逻辑。
func (r *Registry) SetCredentialStatsProvider(fn func() []CredentialStat) {
	r.mu.Lock()
	r.credStats = fn
	r.mu.Unlock()
}

// RecordRequest 记一次转发结果。
//
// status 用 int 而不是 string：调用方手里本来就是 HTTP 状态码，
// 转字符串的活放在埋点里做一次，避免每个调用点都写 strconv.Itoa
// （写错格式的标签比写错代码更难发现）。
func (r *Registry) RecordRequest(model, channel string, status int, latency time.Duration) {
	if model == "" {
		// 没选到渠道就失败的请求（403 额度拦截、503 无可用渠道）
		// 也要计数——这类「网关自己拒了」的量恰恰是排障时最想看的。
		// channel 用空串会生成 channel="" 的标签，合法但难读，
		// 统一落成 "-"（Prometheus 惯例：缺失值用 - 占位）。
		model = "-"
	}
	if channel == "" {
		channel = "-"
	}
	r.counterFor(&r.requests, label(MetricRequestsTotal,
		"model", model, "channel", channel, "status", strconv.Itoa(status))).Inc()
	r.histogramFor(&r.durations, label(MetricDurationSeconds,
		"model", model, "channel", channel)).Observe(latency.Seconds())
}

// RecordTokens 记 token 用量，typ 取 TokenTypePrompt / TokenTypeCompletion。
func (r *Registry) RecordTokens(model, channel, typ string, n int64) {
	if n <= 0 {
		return
	}
	if model == "" {
		model = "-"
	}
	if channel == "" {
		channel = "-"
	}
	r.counterFor(&r.tokens, label(MetricTokensTotal,
		"model", model, "channel", channel, "type", typ)).Add(n)
}

// RecordCost 累计费用。用「微元」（×1e6）存成整数而不是 float64：
//
// atomic 没有浮点加，用浮点只能上锁；而费用是钱，累加误差会让
// 长期跑下来的总数和 logs 表对不上。1e-6 元的精度对计费展示
// （通常两位小数）绰绰有余。
func (r *Registry) RecordCost(model, channel string, cost float64) {
	if cost <= 0 {
		return
	}
	if model == "" {
		model = "-"
	}
	if channel == "" {
		channel = "-"
	}
	micro := int64(cost * 1e6)
	r.counterFor(&r.costs, label(MetricCostTotal,
		"model", model, "channel", channel)).Add(micro)
}

// RecordCredentialFailure 记一次「记在凭据头上」的失败。
//
// 只在 relay 判定该罚时才调（scheduler.ShouldPenalizeCredential）：
// 把 400 这类客户端错误也计进来，会让「哪个 Key 坏了」的曲线
// 被用户侧的畸形请求污染，正好毁掉这个指标的用途。
func (r *Registry) RecordCredentialFailure(channel string, credentialID uint) {
	if channel == "" {
		channel = "-"
	}
	r.counterFor(&r.credFail, label(MetricCredFailures,
		"channel", channel, "credential", strconv.FormatUint(uint64(credentialID), 10))).Inc()
}

// SetCredentialStats 直接设置凭据状态快照（测试用）。
// 生产路径走 SetCredentialStatsProvider 的惰性求值。
func (r *Registry) SetCredentialStats(stats []CredentialStat) {
	r.mu.Lock()
	r.credential = map[string]*gauge{}
	for _, s := range stats {
		r.credential[label(MetricCredentials, "channel", s.Channel, "status", s.Status)] = &gauge{v: s.Count}
	}
	r.mu.Unlock()
}

// counter 是 64 位原子计数器。
//
// 用 atomic.Int64 而不是 atomic.Uint64：cost 存的是微元、可能为负
// （不会，但 Int64 的语义更适合「可增可减的量」），且 Dec 在测试里
// 有用。计数器溢出在这类量级上不可能发生。
type counter struct{ v atomic.Int64 }

func (c *counter) Inc()        { c.v.Add(1) }
func (c *counter) Add(n int64) { c.v.Add(n) }

// gauge 是可被整体覆盖的当前值。只在抓取路径读、在 Set* 路径写，
// 所以不需要原子——写侧有 r.mu 串行化。
type gauge struct{ v int64 }

// histogram 是固定桶的累计计数器 + 和 + 计数。
//
// buckets 里各元素是「≤ 上界」的累计值（Prometheus 的 le 语义），
// 所以 Observe 要做的是「找到第一个 ≥ 观测值的桶，把它和之后所有
// 桶都 +1」——这正是 cumulative 的含义，也是直方图能被 rate() 和
// histogram_quantile() 正确使用的前提。
type histogram struct {
	upper    []float64
	buckets  []atomic.Int64
	sumNanos atomic.Int64
	count    atomic.Int64
}

func newHistogram() *histogram {
	return histogramWithBuckets(DurationBuckets)
}

func histogramWithBuckets(upper []float64) *histogram {
	return &histogram{upper: append([]float64(nil), upper...), buckets: make([]atomic.Int64, len(upper))}
}

func (h *histogram) Observe(seconds float64) {
	idx := len(h.upper) // 落在所有桶之外 → 只进 +Inf
	for i, b := range h.upper {
		if seconds <= b {
			idx = i
			break
		}
	}
	for i := idx; i < len(h.buckets); i++ {
		h.buckets[i].Add(1)
	}
	// 和用纳秒存整数：浮点原子不存在，而秒级 float64 累加在长跑下
	// 会丢精度。纳秒整数转回秒只在输出时做一次除法。
	h.sumNanos.Add(int64(seconds * 1e9))
	h.count.Add(1)
}

// counterFor 取（或建）某个标签组合的计数器。建表走写锁，命中走读锁。
func (r *Registry) counterFor(m *map[string]*counter, key string) *counter {
	r.mu.RLock()
	c, ok := (*m)[key]
	r.mu.RUnlock()
	if ok {
		return c
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	// 双检：两个请求同时首次见到同一标签组合时，后拿到写锁的那个
	// 必须看到前一个已经建好的计数器，否则前者的增量会被覆盖丢失。
	if c, ok := (*m)[key]; ok {
		return c
	}
	c = &counter{}
	(*m)[key] = c
	return c
}

func (r *Registry) histogramFor(m *map[string]*histogram, key string) *histogram {
	r.mu.RLock()
	h, ok := (*m)[key]
	r.mu.RUnlock()
	if ok {
		return h
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if h, ok := (*m)[key]; ok {
		return h
	}
	if strings.HasPrefix(key, MetricRouteDuration+"\x00") {
		h = histogramWithBuckets(routeDurationBuckets)
	} else {
		h = newHistogram()
	}
	(*m)[key] = h
	return h
}

// label 拼出 map 的 key：指标名 + 各标签的「键\x00值」序列。
//
// 用 \x00 分隔而不是直接拼字符串：模型名里出现 channel 名的字符
// 组合时（比如 model="a,channel=b"），裸拼接会产生两个不同的标签
// 组合映射到同一个 key 的碰撞，计数器会互相污染。\x00 不会出现在
// 模型名/渠道名里。
func label(name string, kv ...string) string {
	var b strings.Builder
	b.WriteString(name)
	for i := 0; i+1 < len(kv); i += 2 {
		b.WriteString("\x00")
		b.WriteString(kv[i])
		b.WriteString("\x00")
		b.WriteString(kv[i+1])
	}
	return b.String()
}

// Render 按 Prometheus exposition 格式（text/plain; version=0.0.4）
// 把当前指标写出去。
//
// 输出顺序确定（按键排序）：同一份指标两次抓取结果逐字节相同，
//
//	diff 工具和「指标值有没有变」的人工比对才能用。
func (r *Registry) Render(w io.Writer) error {
	r.mu.RLock()
	defer r.mu.RUnlock()

	// 每次抓取重新算一遍凭据状态：状态是「现在有多少把在冷却」，
	// 累加式做不到（一把 Key 从 cooldown 回 active 是 -1 而不是 +1）。
	stats := map[string]*gauge{}
	if r.credStats != nil {
		for _, s := range r.credStats() {
			stats[label(MetricCredentials, "channel", s.Channel, "status", s.Status)] = &gauge{v: s.Count}
		}
	}

	var b strings.Builder

	r.renderShadow(&b)
	r.renderRuntime(&b)
	writeFamilyHeader(&b, MetricAttemptsTotal, "counter", "Executed upstream attempts, including retries")
	writeCounters(&b, MetricAttemptsTotal, r.attempts, 1)
	writeFamilyHeader(&b, MetricAttemptDuration, "histogram", "Upstream attempt duration in seconds")
	writeHistograms(&b, MetricAttemptDuration, r.attemptDurations)
	writeFamilyHeader(&b, MetricRouteSelections, "counter", "Target selections before attempt admission; labels have fixed values")
	writeCounters(&b, MetricRouteSelections, r.routeSelections, 1)
	writeFamilyHeader(&b, MetricRouteDuration, "histogram", "Target selection duration in seconds")
	writeHistograms(&b, MetricRouteDuration, r.routeDurations)
	if r.logStats != nil {
		queue := r.logStats()
		for _, metric := range []struct {
			name, kind, help string
			value            uint64
		}{
			{"gateway_log_queue_depth", "gauge", "Queued access logs", uint64(queue.Depth)},
			{"gateway_log_queue_capacity", "gauge", "Maximum queued access logs", uint64(queue.Capacity)},
			{"gateway_logs_dropped_total", "counter", "Access logs dropped at a full or closed queue", queue.Dropped},
			{"gateway_logs_failed_total", "counter", "Access logs that failed final database writes", queue.Failed},
		} {
			writeFamilyHeader(&b, metric.name, metric.kind, metric.help)
			fmt.Fprintf(&b, "%s %d\n", metric.name, metric.value)
		}
	}

	writeFamilyHeader(&b, MetricRequestsTotal, "counter",
		"转发请求总数（按模型/渠道/最终状态码分标签）")
	writeCounters(&b, MetricRequestsTotal, r.requests, 1)

	writeFamilyHeader(&b, MetricDurationSeconds, "histogram",
		"转发耗时分布（秒，含重试；从收到请求到响应写完）")
	writeHistograms(&b, MetricDurationSeconds, r.durations)

	writeFamilyHeader(&b, MetricTokensTotal, "counter",
		"token 用量累计（type=prompt/completion）")
	writeCounters(&b, MetricTokensTotal, r.tokens, 1)

	writeFamilyHeader(&b, MetricCostTotal, "counter",
		"费用累计（元）")
	writeCosts(&b, r.costs)

	writeFamilyHeader(&b, MetricCredentials, "gauge",
		"各渠道下各状态的凭据数（active/cooldown/banned/disabled）")
	writeGauges(&b, MetricCredentials, stats)

	writeFamilyHeader(&b, MetricCredFailures, "counter",
		"凭据失败累计（只计该罚的失败：401/403/429/408/5xx）")
	writeCounters(&b, MetricCredFailures, r.credFail, 1)

	_, err := io.WriteString(w, b.String())
	return err
}

func writeFamilyHeader(b *strings.Builder, name, typ, help string) {
	fmt.Fprintf(b, "# HELP %s %s\n", name, help)
	// TYPE 行是 Prometheus 判断指标类型的唯一依据：counter 会被
	// rate()/increase() 接受，gauge 会被当瞬时值。写错类型查询
	// 不会报错但结果全错，所以每个 family 都必须显式声明。
	fmt.Fprintf(b, "# TYPE %s %s\n", name, typ)
}

// writeCounters 输出一组 counter。scale 是输出前的除数（tokens 直接
// 输出，费用走 writeCosts 用 1e6）。
func writeCounters(b *strings.Builder, name string, m map[string]*counter, scale int64) {
	keys := sortedKeys(m)
	for _, k := range keys {
		labels := labelsFromKey(k)
		v := m[k].v.Load() / scale
		fmt.Fprintf(b, "%s%s %d\n", name, labels, v)
	}
}

// writeCosts 输出费用。计数器里存的是微元整数，这里除回元并保留
// 6 位小数——6 位是存储精度，展示端要几位是自己格式化的事。
func writeCosts(b *strings.Builder, m map[string]*counter) {
	for _, k := range sortedKeys(m) {
		micro := m[k].v.Load()
		fmt.Fprintf(b, "%s%s %s\n", MetricCostTotal, labelsFromKey(k),
			strconv.FormatFloat(float64(micro)/1e6, 'f', 6, 64))
	}
}

func writeGauges(b *strings.Builder, name string, m map[string]*gauge) {
	for _, k := range sortedKeys(m) {
		fmt.Fprintf(b, "%s%s %d\n", name, labelsFromKey(k), m[k].v)
	}
}

func writeHistograms(b *strings.Builder, name string, m map[string]*histogram) {
	for _, k := range sortedKeys(m) {
		h := m[k]
		pairs := sortedLabelPairs(k)
		// 桶必须按 le 升序、且最后一档是 +Inf——histogram_quantile()
		// 靠这个顺序做插值，缺 +Inf 会让「超过最大桶的观测」凭空消失，
		// 表现为 p99 永远不高于 60s。
		//
		// le 必须和其他标签在**同一对花括号内**：写成
		// `name_bucket{model="m"},le="0.05"` 是非法格式，Prometheus
		// 会整行报 parse error——整个 target 抓不上来，比少一个指标
		// 严重得多。（这正是手写 exposition 最容易踩的坑，由
		// TestHistogramFormat 盯着。）
		for i, upper := range h.upper {
			fmt.Fprintf(b, "%s_bucket%s %d\n", name,
				bracesSorted(append(pairs, labelPair("le", strconv.FormatFloat(upper, 'g', -1, 64)))),
				h.buckets[i].Load())
		}
		fmt.Fprintf(b, "%s_bucket%s %d\n", name,
			bracesSorted(append(pairs, labelPair("le", "+Inf"))), h.count.Load())
		fmt.Fprintf(b, "%s_sum%s %s\n", name, braces(pairs),
			strconv.FormatFloat(float64(h.sumNanos.Load())/1e9, 'f', 6, 64))
		fmt.Fprintf(b, "%s_count%s %d\n", name, braces(pairs), h.count.Load())
	}
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	// 按「去掉指标名后的标签串」排序，输出才稳定可 diff。
	sort.Strings(keys)
	return keys
}

// labelsFromKey 把内部 key 还原成 `{k="v",k="v"}`。
//
// 标签值必须转义：Prometheus 的 text 格式里 " \ 换行 三个字符在
// 标签值里有特殊含义，不转义会生成抓取端解析失败的输出——那种
// 故障表现为「整个 /metrics 抓不上来」，而不是少一个指标。
func labelsFromKey(key string) string {
	return braces(sortedLabelPairs(key))
}

// sortedLabelPairs 拆出 key 里的标签并按键名字典序返回 `k="v"` 列表。
//
// 排序是为了输出稳定：Prometheus 不要求标签有序，但同一状态两次
// 抓取逐字节相同才能用 diff 比对「指标值变了没有」。不排序的话
// 每次抓取的标签顺序都不同，diff 会满屏假差异。
func sortedLabelPairs(key string) []string {
	parts := strings.Split(key, "\x00")
	// parts[0] 是指标名，后面是 键/值 交替
	if len(parts) <= 1 {
		return nil
	}
	pairs := make([]string, 0, (len(parts)-1)/2)
	for i := 1; i+1 < len(parts); i += 2 {
		pairs = append(pairs, labelPair(parts[i], escapeLabelValue(parts[i+1])))
	}
	sort.Strings(pairs)
	return pairs
}

// braces 把 `k="v"` 列表包成花括号形式（调用方保证已排序）。
// 空列表返回空串——没有标签的指标行不能写成 `{}`（虽然合法，
// 但 diff 和肉眼都可读性更差）。
func braces(pairs []string) string {
	if len(pairs) == 0 {
		return ""
	}
	return "{" + strings.Join(pairs, ",") + "}"
}

// bracesSorted 是「追加 le 之后重新排序再包花括号」的组合。
//
// le 必须和别的标签一起排序而不是追加在末尾：整行标签顺序一致，
// 同一个 (model,channel) 的 bucket 行与 _sum/_count 行才长得一样，
// 用文本工具按前缀聚合（如 grep + sort | uniq -c）才不会把同一组
// 标签算成两类。
func bracesSorted(pairs []string) string {
	sort.Strings(pairs)
	return braces(pairs)
}

func labelPair(k, v string) string { return k + `="` + v + `"` }

func escapeLabelValue(v string) string {
	// 顺序不能反：先转义反斜杠本身，否则会把转义引入的 \ 再转一遍。
	v = strings.ReplaceAll(v, `\`, `\\`)
	v = strings.ReplaceAll(v, "\n", `\n`)
	v = strings.ReplaceAll(v, `"`, `\"`)
	return v
}
