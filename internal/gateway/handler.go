package gateway

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"minigateway/internal/billing"
	"minigateway/internal/metrics"
	dbmodel "minigateway/internal/model"
	"minigateway/internal/protocol/meta"
	"minigateway/internal/protocol/model"
	"minigateway/internal/server/middleware"
)

// captureLimit 是响应捕获缓冲的上限（64KB）。
//
// 只在「上游没返回 usage」时用来估算 completion tokens，不需要
// 完整响应；设上限是为了防止一个超大响应把内存吃光——
// 估算本来就允许不准。
const captureLimit = 64 * 1024

// HandleChatCompletions 处理 POST /v1/chat/completions。
//
// 关键设计：请求体只解析 model/stream/max_tokens 三个字段，然后**原样
// 透传**给上游。
//
// 为什么不完整建模请求体：LLM 请求的参数在快速演进（新出的 reasoning、
// tools、response_format…）。如果网关用结构体反序列化再序列化，
// 任何未声明的字段都会被静默丢掉，用户会遇到「某个参数在网关后面
// 失效了」这种极难排查的问题。透明代理是网关的基本素养。
// max_tokens 是唯一例外——预扣额度需要它算费用上限。
func (g *Gateway) HandleChatCompletions(c *gin.Context) {
	g.handleRelay(c, meta.ModeChatCompletions)
}

// HandleEmbeddings 处理 POST /v1/embeddings（文本转向量）。
//
// 与对话入口共用同一条转发链路（选渠道 → 预扣 → 转发 → 结算 → 记账），
// 唯一的区别是 Mode：适配器靠它决定打 /v1/embeddings 而不是
// /v1/chat/completions（见 openai 适配器的 GetRequestURL）。
//
// 计费上的差别不是特判出来的，而是数据自然决定的：embeddings 响应
// 的 usage 只有 {prompt_tokens, total_tokens}，没有 completion_tokens，
// 反序列化后 CompletionTokens 天然是 0，计费公式里输出项自动为 0
// ——「只按输入计价」是结构的结果，不需要在计费里加分支。
//
// 注意：embedding 模型名通常与对话模型不同（text-embedding-3-small
// 之类），渠道必须把该模型配进 models 列表，否则路由选不到渠道。
func (g *Gateway) HandleEmbeddings(c *gin.Context) {
	g.handleRelay(c, meta.ModeEmbeddings)
}

// handleRelay 是 /v1 各转发入口的公共实现，mode 区分端点。
//
// 为什么抽出来而不是两份拷贝：两个入口的差异只有 Mode 一个值，
// 而鉴权、额度预扣、结算、记账这些环节必须**完全一致**——
// 拷贝一份就意味着以后改计费逻辑要记得改两处，漏一处就是
// 「embeddings 请求不记账」这种极难发现的账目漏洞。
func (g *Gateway) handleRelay(c *gin.Context, mode int) {
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			writeError(c, http.StatusRequestEntityTooLarge, "请求体超过大小限制", "request_too_large")
			return
		}
		writeError(c, http.StatusBadRequest, "读取请求体失败: "+err.Error(), "invalid_request_error")
		return
	}
	if len(body) == 0 {
		writeError(c, http.StatusBadRequest, "请求体为空", "invalid_request_error")
		return
	}

	// 只解析路由/计费必需的字段
	var req model.ModelRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeError(c, http.StatusBadRequest, "请求体不是合法 JSON: "+err.Error(), "invalid_request_error")
		return
	}
	if req.Model == "" {
		writeError(c, http.StatusBadRequest, "缺少 model 字段", "invalid_request_error")
		return
	}

	result := state(c)
	allowedModels := result.Identity.AllowedModels
	tokenName := result.Identity.TokenName
	requestID := result.Identity.RequestID

	// 请求摘要用 info：一条请求一行，是日志的骨干。只放「进来时
	// 就知道」的字段——结果字段（status/latency/tokens/cost）在
	// finishRequest 里补，那时才拿得到。
	//
	// 为什么拆成两条而不是等结束打一条完整的：流式请求可以挂几十秒，
	// 期间如果进程被杀或上游卡住，只有入口这条日志能证明「请求到过
	// 网关」。两条日志用同一个 request_id 串起来。
	slog.Info("[请求] 收到转发请求",
		"request_id", requestID,
		"token", tokenName,
		"model", req.Model,
		"endpoint", modeName(mode),
		"stream", req.Stream)

	// 记录开始时间，用于算整条链路耗时（含重试）
	startedAt := time.Now()

	// 包一层 writer 记录回写给客户端的字节：只有「上游没返回 usage」
	// 时才需要它估算 completion tokens。成本接近零（一次内存拷贝，
	// 且封顶 64KB），比在每条响应路径上判断「这次要不要捕获」省事。
	cw := &captureWriter{ResponseWriter: c.Writer, limit: captureLimit}
	c.Writer = cw

	// 请求前额度处理：检查 + 预扣。
	//
	// 必须在转发之前：超额时直接回 403，一个字节都不发给上游——
	// 上游的 Key 是按量收费的，转发注定被拒的请求等于白烧钱（SPEC 4.2）。
	pre, err := g.preRequest(c, &req)
	if errors.Is(err, billing.ErrQuotaExceeded) {
		// 记账先行：403 也是请求，日志里要能看到「被额度拦下」，
		// 否则排查「为什么突然全部 403」时账本一片空白。
		// 状态码和错误信息先写进 context，finishRequest 才能
		// 按 403（而不是默认的 500）记账。
		state(c).StatusCode = http.StatusForbidden
		state(c).ErrorMessage = err.Error()
		g.finishRequest(c, &req, startedAt, pre, body)
		writeError(c, http.StatusForbidden, err.Error(), "quota_exceeded")
		return
	}
	if err != nil {
		// 额度子系统自身故障（读库失败等）：放行，不拦请求。
		// 计费挂了不该让转发也挂——服务可用性优先，与
		// 「价格缺失不报错」是同一条原则。但必须响亮的告警。
		// error 级别：这是网关自身子系统的故障，不是上游问题。
		slog.Error("[额度] 预检失败，放行本次请求",
			"request_id", requestID, "model", req.Model, "err", err)
		pre = nil
	}

	// 用 flag + defer 保证「无论 Relay 正常返回还是 panic，都要结算」：
	// 预扣了钱不结账，令牌额度会被永久占用（表现为「没用几次就 403」）。
	// panic 会被 Recovery 中间件吞掉，不走 return 之后的代码，
	// 只有 defer 能在展开过程中执行到。
	finished := false
	defer func() {
		if !finished {
			g.finishRequest(c, &req, startedAt, pre, body)
		}
	}()

	g.Relay(c, &req, body, allowedModels, mode, g.attemptTimeout)

	finished = true
	// 收尾放在 Relay 之后而不是用 defer 直接包：流式响应是在 Relay
	// 内部边收边发的，函数返回时响应早已写完；非流式的 c.Data 也在
	// Relay 内部完成。此时结算和记账不会影响客户端拿到的任何字节。
	g.finishRequest(c, &req, startedAt, pre, body)
}

// modeName 把转发模式转成日志里看的名字。
func modeName(mode int) string {
	if mode == meta.ModeEmbeddings {
		return "embeddings"
	}
	return "chat"
}

// preRequest 做请求前的额度检查与预扣。返回的快照要传给 finishRequest。
//
// tokenID/userID 为 0 时跳过对应目标（未登录/无主令牌不该让请求失败）。
func (g *Gateway) preRequest(c *gin.Context, req *model.ModelRequest) (*billing.PreDeduct, error) {
	if g.billing == nil {
		return nil, nil
	}
	tokenID := state(c).Identity.TokenID
	userID := state(c).Identity.UserID
	if tokenID == 0 && userID == 0 {
		return nil, nil
	}
	// 预扣上限按「客户端请求的模型名」估：选渠道（和模型映射）发生在
	// Relay 内部，请求前还知道不——这只是个保守上限，结算时用
	// 实际模型的真实价格修正。
	return g.billing.PreRequest(tokenID, userID, req.MaxTokens, req.Model)
}

// finishRequest 收尾：估算用量 → 算费 → 结算额度差额 → 写日志。
//
// 无论 Relay 成功、失败还是 panic 都会走到这里。
func (g *Gateway) finishRequest(c *gin.Context, req *model.ModelRequest, startedAt time.Time, pre *billing.PreDeduct, reqBody []byte) {
	status := relayStatus(c)
	tokenID := state(c).Identity.TokenID
	userID := state(c).Identity.UserID

	usage := relayUsage(c)
	isEstimated := false
	cost := 0.0
	priceMissing := false

	// 只有成功响应才计费：失败请求 cost=0（结算时全额退回预扣），
	// price_missing 也只在真的查过价时才有意义——给一次 403 打
	// 「缺价」标记会让统计数字虚高。
	if status == http.StatusOK {
		if usage == nil {
			// 上游没返回 usage（或没配 stream_options.include_usage）：
			// 按字符数估算并标记。prompt 从请求体抽文本，
			// completion 从回写给客户端的内容抽文本。
			est := billing.EstimateUsage(
				billing.PromptTextFromBody(reqBody),
				billing.CompletionTextFromResponse(captureBytes(c)),
			)
			usage = &est
			isEstimated = true
		}
		if g.billing != nil {
			// PriceOf 的第二个返回值是「是否找到价格」——取反才是
			// price_missing 标记。曾经直接把「找到」赋值给「缺失」，
			// 导致有价格的模型全被打上缺价标记（集成测试抓到的）。
			priceFound := false
			cost, priceFound = g.billing.PriceOf(usage, relayActualModel(c, req.Model), req.Model, relayChannelGroup(c))
			priceMissing = !priceFound
		}
	}
	// 失败请求不计费：cost 保持 0，结算时全额退回预扣。
	// 上游返回错误时 token 通常也没被计费，网关跟着免费是合理对齐。

	// 结算：把预扣和实际的差额调整掉
	if g.billing != nil && pre != nil {
		if err := g.billing.Settle(tokenID, userID, pre, cost); err != nil {
			// error：结算失败意味着预扣的钱没被修正，令牌额度会被
			// 永久占用（表现为「没用几次就 403」）。钱相关的问题
			// 不能只是 warn。
			slog.Error("[结算] 额度结算失败，预扣可能未释放",
				"request_id", state(c).Identity.RequestID,
				"token_id", tokenID, "user_id", userID, "err", err)
		}
	}

	// TPM 事后修正：把限流窗口里的「预估值」换成真实用量。
	//
	// 放在这里而不是中间件里：真实 usage 只有这一侧拿得到（流式响应
	// 的 usage 要等流结束才知道，中间件的 defer 在 c.Next() 返回时就
	// 跑了，拿不到）。失败请求也补——预估值已经占了窗口的位置，
	// 不退回去等于失败请求白扣 TPM 配额。
	if usage != nil {
		middleware.FinishTokens(c, g.limiter, usage.TotalTokens)
	}

	g.writeRequestLog(c, req, startedAt, cost, usage, isEstimated, priceMissing)
	// 请求结果摘要 + 指标埋点放在最后：两个都只需要「最终状态」，
	// 而且必须和 writeRequestLog 看到同一份数据——摘要是给人看的
	// 这条日志、指标是给机器看的那条，两者对不上比缺一个更难排查。
	g.finishObservability(c, req, startedAt, status, cost, usage, priceMissing)
}

// finishObservability 打请求结果日志（按结果分级）并记 Prometheus 指标。
//
// 分级规则（B4-1 的要求）：
//
//	200        info  正常请求，一行摘要
//	4xx        warn  客户端侧被拒（含网关自己的 403 额度拦截）
//	5xx/无码   error 上游故障或网关自身故障
//
// 为什么 4xx 是 warn 而不是 info：客户端会收到错误，通常意味着
// 配置或用法有问题，需要人看一眼；但它不是网关故障。
// 为什么 403 也按 warn：额度耗尽是「预期内的拒绝」，但运维需要
// 看到它才能及时充值——埋在 info 里会被正常流量冲掉。
//
// 指标只在这里埋一次（而不是每次转发尝试都埋）：重试时中间那次
// 失败的渠道信息已经由 scheduler 的凭据失败指标和 relay 的 warn
// 日志覆盖；请求级指标记的是「这次请求最终结果」，重复记会把
// gateway_requests_total 撑成重试次数，rate() 算出来的 QPS 是假的。
func (g *Gateway) finishObservability(c *gin.Context, req *model.ModelRequest, startedAt time.Time,
	status int, cost float64, usage *model.Usage, priceMissing bool) {
	latency := time.Since(startedAt)
	channel := state(c).ChannelName
	rid := state(c).Identity.RequestID

	attrs := []any{
		"request_id", rid,
		"token_id", state(c).Identity.TokenID,
		"channel_id", state(c).ChannelID,
		"credential_id", state(c).CredentialID,
		"channel", channel,
		"model", req.Model,
		"latency_ms", latency.Milliseconds(),
		"status", status,
	}
	if usage != nil {
		attrs = append(attrs,
			"prompt_tokens", usage.PromptTokens,
			"completion_tokens", usage.CompletionTokens,
			"total_tokens", usage.TotalTokens)
	}
	if cost > 0 {
		attrs = append(attrs, "cost", cost)
	}
	if priceMissing {
		// 缺价必须出现在日志里：计费数字会偏低，而这是唯一能
		// 提醒「去补价格表」的信号（见 SPEC 4.5）。
		attrs = append(attrs, "price_missing", true)
	}
	// error 只在真的失败时带上：重试成功的请求，Relay 会把中间那次
	// 失败的 error_message 留在 context 里（账本需要它记录「这次请求
	// 中间踩过什么」）。但把它打在 status=200 的摘要里会让人以为这次
	// 请求失败了——日志的读者看的是摘要，不是账本。
	if status >= 400 && state(c).ErrorMessage != "" {
		attrs = append(attrs, "error", state(c).ErrorMessage)
	}

	switch {
	case status >= 500:
		slog.Error("[请求] 失败（服务端错误）", attrs...)
	case status >= 400:
		slog.Warn("[请求] 被拒绝（客户端错误）", attrs...)
	default:
		slog.Info("[请求] 完成", attrs...)
	}

	// 指标：实际模型名当 model 标签（价格和 token 都是它产生的），
	// 客户端请求名只在日志里——否则 model_mapping 会让两个名字
	// 的流量被算成两个指标，看不出真实分布。
	g.metrics.RecordRequest(actualModelFor(c, req.Model), channel, status, latency)
	if usage != nil {
		g.metrics.RecordTokens(actualModelFor(c, req.Model), channel,
			metrics.TokenTypePrompt, int64(usage.PromptTokens))
		g.metrics.RecordTokens(actualModelFor(c, req.Model), channel,
			metrics.TokenTypeCompletion, int64(usage.CompletionTokens))
	}
	g.metrics.RecordCost(actualModelFor(c, req.Model), channel, cost)
}

// actualModelFor 取映射后的上游模型名（无映射时回落客户端请求名）。
//
// 单独包一层而不是直接调 relayActualModel：那个函数第二参数是
// fallback，语义一样但名字读不出「这是给指标用的」。
func actualModelFor(c *gin.Context, fallback string) string {
	return relayActualModel(c, fallback)
}

// captureWriter 把回写给客户端的字节复制一份（封顶），供估算用。
//
// 内嵌 gin.ResponseWriter 而不是重新实现：Flusher/Hijacker 等能力
// 全部原样透传——流式转发依赖 Flush，少实现一个方法流式就废了。
type captureWriter struct {
	gin.ResponseWriter
	buf   bytes.Buffer
	limit int
}

func (w *captureWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *captureWriter) Write(p []byte) (int, error) {
	w.capture(p)
	return w.ResponseWriter.Write(p)
}

// WriteString 必须一起重写：内嵌接口的同名方法不会走到我们重写的
// Write，而流式转发（SSE）走的正是 WriteString。
func (w *captureWriter) WriteString(s string) (int, error) {
	w.capture([]byte(s))
	return w.ResponseWriter.WriteString(s)
}

func (w *captureWriter) capture(p []byte) {
	if room := w.limit - w.buf.Len(); room > 0 {
		if len(p) > room {
			p = p[:room]
		}
		w.buf.Write(p)
	}
}

// captureBytes 从上下文取本次请求捕获到的响应字节。
// 类型断言失败（没包过 writer，比如直接构造 context 的测试）时返回 nil。
func captureBytes(c *gin.Context) []byte {
	if cw, ok := c.Writer.(*captureWriter); ok {
		return cw.buf.Bytes()
	}
	return nil
}

// Log recording is injected by gateway.Options.
//
// 用函数变量而不是直接依赖 store：relay 是纯转发层，不该知道存储层的存在。
// main 在启动时把 store.WriteLog（内部是异步批量写入）注入进来，
// 测试时不注入就是空操作。

// writeRequestLog 组装并写一条请求日志。
//
// 刻意不阻塞主流程：记账失败只打日志，不影响已经发给客户端的响应。
// 用户请求已经成功了，网关因为自己的账本坏了就报错是不讲道理的。
func (g *Gateway) writeRequestLog(c *gin.Context, req *model.ModelRequest, startedAt time.Time,
	cost float64, usage *model.Usage, isEstimated, priceMissing bool) {
	if g.logRecorder == nil {
		return
	}
	// 从 context 取 Relay 写入的结果；没拿到说明中途 panic 被 Recovery
	// 吞了——那种情况状态码无从得知，按 500 记（绝不能默认 200，
	// 否则一次彻底失败的请求会被记成成功，账本失去意义）。
	status := relayStatus(c)
	channelID := state(c).ChannelID
	credentialID := state(c).CredentialID
	tokenID := state(c).Identity.TokenID
	userID := state(c).Identity.UserID

	actualModel := relayActualModel(c, req.Model)
	rid := state(c).Identity.RequestID
	errMsg := state(c).ErrorMessage

	entry := &dbmodel.RequestLog{
		RequestID:    rid,
		UserID:       userID,
		TokenID:      tokenID,
		ChannelID:    channelID,
		CredentialID: credentialID,
		OriginModel:  req.Model,
		ActualModel:  actualModel,
		IsStream:     req.Stream,
		Status:       status,
		ErrorMessage: errMsg,
		LatencyMs:    time.Since(startedAt).Milliseconds(),
		Cost:         cost,
		IsEstimated:  isEstimated,
		PriceMissing: priceMissing,
	}
	if usage != nil {
		entry.PromptTokens = usage.PromptTokens
		entry.CompletionTokens = usage.CompletionTokens
		entry.TotalTokens = usage.TotalTokens
	}
	g.logRecorder(entry)
}

// relayStatus 取本次请求的最终状态码，默认 500。
//
// 默认值选 500 而不是 200：拿不到状态码只有一种情况——Relay 还没
// 设置就被 panic 打断了（Recovery 中间件会吞掉 panic），
// 那是服务器故障，不是成功。
func relayStatus(c *gin.Context) int { return state(c).StatusCode }
func relayUsage(c *gin.Context) *model.Usage {
	u := state(c).Usage
	if u != nil && u.TotalTokens == 0 {
		u.TotalTokens = u.PromptTokens + u.CompletionTokens
	}
	return u
}
func relayActualModel(c *gin.Context, fallback string) string {
	if m := state(c).ActualModel; m != "" {
		return m
	}
	return fallback
}
func relayChannelGroup(c *gin.Context) string {
	if g := state(c).ChannelGroup; g != "" {
		return g
	}
	return "default"
}

// ctxUint 从上下文取一个 uint 值，取不到返回 0。
func ctxUint(c *gin.Context, key string) uint {
	if v, ok := c.Get(key); ok {
		if n, ok := v.(uint); ok {
			return n
		}
	}
	return 0
}

// ListModels 处理 GET /v1/models，返回该令牌可访问的模型清单。
//
// 客户端（Cherry Studio、NextChat 这类）靠这个接口自动填充模型下拉框。
func (g *Gateway) ListModels(c *gin.Context) {
	allowedModels := state(c).Identity.AllowedModels

	seen := map[string]bool{}
	data := make([]gin.H, 0)

	for _, ch := range g.reg.allChannels() {
		for _, m := range ch.models {
			if m == "*" {
				continue
			}
			if seen[m] || !modelAllowed(m, allowedModels) {
				continue
			}
			seen[m] = true
			data = append(data, gin.H{
				"id":       m,
				"object":   "model",
				"owned_by": ch.name,
			})
		}
	}

	c.JSON(http.StatusOK, gin.H{"object": "list", "data": data})
}
