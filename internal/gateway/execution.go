package gateway

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"minigateway/internal/access"
	"minigateway/internal/billing"
	"minigateway/internal/metrics"
	dbmodel "minigateway/internal/model"
	"minigateway/internal/protocol"
	"minigateway/internal/protocol/model"
)

const captureLimit = 64 * 1024

func (g *Gateway) executeScope(c *executionScope, req protocol.Request) {
	body := req.Body()

	result := c.result

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
		"model", req.Model(),
		"endpoint", string(req.Operation()),
		"stream", req.Stream())

	// 记录开始时间，用于算整条链路耗时（含重试）
	startedAt := time.Now()
	ctx, cancel := context.WithTimeout(c.ctx, g.totalTimeout)
	defer cancel()
	c.ctx = ctx

	// 包一层 writer 记录回写给客户端的字节：只有「上游没返回 usage」
	// 时才需要它估算 completion tokens。成本接近零（一次内存拷贝，
	// 且封顶 64KB），比在每条响应路径上判断「这次要不要捕获」省事。
	c.output.captureLimit = captureLimit

	// 请求前额度处理：检查 + 预扣。
	//
	// 必须在转发之前：超额时直接回 403，一个字节都不发给上游——
	// 上游的 Key 是按量收费的，转发注定被拒的请求等于白烧钱（SPEC 4.2）。
	if result.Identity.Policy != nil && !result.Identity.Policy.Allows(req.Model()) {
		result.StatusCode = http.StatusForbidden
		result.ErrorMessage = "model is not authorized for this user and API key"
		g.finishRequest(c, req, startedAt, nil, body)
		writeError(c, http.StatusForbidden, result.ErrorMessage, "forbidden")
		return
	}
	if g.billingMode == billing.ModeStrict && g.strictReady() {
		limits, err := billing.ParseStrictRequest(body, req.Operation() == protocol.Embeddings)
		if err != nil {
			result.StatusCode = http.StatusBadRequest
			result.ErrorMessage = err.Error()
			g.finishRequest(c, req, startedAt, nil, body)
			writeError(c, result.StatusCode, result.ErrorMessage, "unsupported_billing_dimension")
			return
		}
		result.strictRequest = &limits
	}
	requestLease, admissionErr := g.acquire(ctx, c.output, AdmissionScope{Level: RequestAdmission,
		UserID: result.Identity.UserID, TokenID: result.Identity.TokenID, Model: req.Model(), Operation: req.Operation()})
	if admissionErr != nil {
		writeError(c, admissionErr.StatusCode, admissionErr.Error.Message, admissionErr.Error.Type)
		g.finishRequest(c, req, startedAt, nil, body)
		return
	}
	defer requestLease()
	c.requestAdmitted = true
	if p, ok := access.PrincipalFromContext(ctx); ok && g.limiter != nil && !access.HasRateAdmission(ctx, p.TokenID) {
		now := time.Now()
		if p.RPMLimit > 0 {
			if allowed, retry := g.limiter.AllowRequest(ctx, access.TokenBucketKey(p.TokenID), p.RPMLimit, now); !allowed {
				c.output.setMetadata("Retry-After", strconv.Itoa(retry))
				writeError(c, 429, "API key request rate exceeded", "rate_limit_error")
				g.finishRequest(c, req, startedAt, nil, body)
				return
			}
		}
		if p.TPMLimit > 0 {
			estimate := billing.EstimateUsage(requestPromptText(req), "").PromptTokens
			if allowed, retry := g.limiter.AllowTokens(ctx, access.TokenUsageBucketKey(p.TokenID), p.TPMLimit, estimate, now); !allowed {
				c.output.setMetadata("Retry-After", strconv.Itoa(retry))
				writeError(c, 429, "API key token rate exceeded", "rate_limit_error")
				g.finishRequest(c, req, startedAt, nil, body)
				return
			}
			c.ctx = access.WithTokenReservation(ctx, p.TokenID, estimate)
		}
		c.ctx = access.WithRateAdmission(c.ctx, p.TokenID)
	}
	pre, err := g.preRequest(c, req)
	if errors.Is(err, billing.ErrQuotaExceeded) {
		// 记账先行：403 也是请求，日志里要能看到「被额度拦下」，
		// 否则排查「为什么突然全部 403」时账本一片空白。
		// 状态码和错误信息先写进 context，finishRequest 才能
		// 按 403（而不是默认的 500）记账。
		c.result.StatusCode = http.StatusForbidden
		c.result.ErrorMessage = err.Error()
		g.finishRequest(c, req, startedAt, pre, body)
		writeError(c, http.StatusForbidden, err.Error(), "quota_exceeded")
		return
	}
	if err != nil {
		if g.billingMode == billing.ModeStrict {
			slog.Error("strict budget reservation failed; request rejected", "request_id", requestID, "err", err)
			result.StatusCode = http.StatusServiceUnavailable
			result.ErrorMessage = "strict billing could not reserve the request budget"
			g.finishRequest(c, req, startedAt, nil, body)
			writeError(c, result.StatusCode, result.ErrorMessage, "billing_unavailable")
			return
		}
		// 额度子系统自身故障（读库失败等）：放行，不拦请求。
		// 计费挂了不该让转发也挂——服务可用性优先，与
		// 「价格缺失不报错」是同一条原则。但必须响亮的告警。
		// error 级别：这是网关自身子系统的故障，不是上游问题。
		slog.Error("[额度] 预检失败，放行本次请求",
			"request_id", requestID, "model", req.Model(), "err", err)
		pre = pre.PriceOnly()
	}
	result.reservation = pre
	result.BillingID, result.PriceVersion = pre.BillingID(), pre.PriceVersion()
	if pre.BillingID() != "" {
		c.output.setMetadata("X-TXAPI-Billing-ID", pre.BillingID())
	}
	if pre.PriceVersion() != "" {
		c.output.setMetadata("X-TXAPI-Price-Version", pre.PriceVersion())
	}

	// 用 flag + defer 保证「无论 Relay 正常返回还是 panic，都要结算」：
	// 预扣了钱不结账，令牌额度会被永久占用（表现为「没用几次就 403」）。
	// panic 会被 Recovery 中间件吞掉，不走 return 之后的代码，
	// 只有 defer 能在展开过程中执行到。
	finished := false
	defer func() {
		if !finished {
			g.finishRequest(c, req, startedAt, pre, body)
		}
	}()

	g.relay(c, req, allowedModels, g.attemptTimeout)

	finished = true
	// 收尾放在 Relay 之后而不是用 defer 直接包：流式响应是在 Relay
	// 内部边收边发的，函数返回时响应早已写完；非流式的 c.Data 也在
	// Relay 内部完成。此时结算和记账不会影响客户端拿到的任何字节。
	g.finishRequest(c, req, startedAt, pre, body)
}

// preRequest 做请求前的额度检查与预扣。返回的快照要传给 finishRequest。
//
// legacy 跳过 ID=0 的目标；strict 要求可信用户与 Key 归属都存在。
func (g *Gateway) preRequest(c *executionScope, req protocol.Request) (*billing.PreDeduct, error) {
	if g.billingMode == billing.ModeStrict {
		if !g.strictReady() {
			return nil, billing.ErrStrictUnavailable
		}
		if c.claim != nil {
			port, ok := g.strictBilling.(IntentBilling)
			if !ok {
				return nil, billing.ErrStrictUnavailable
			}
			return port.ResumeGenerationIntent(c.ctx, *c.claim, c.result.Identity.UserID, c.result.Identity.TokenID, string(req.Operation()), req.Model())
		}
		return g.strictBilling.Reserve(c.ctx, c.result.Identity.TokenID, c.result.Identity.UserID, req.Model())
	}
	if g.billing == nil || g.billingMode != billing.ModeLegacy {
		return nil, nil
	}
	tokenID := c.result.Identity.TokenID
	userID := c.result.Identity.UserID
	// legacy 只估算请求模型的输出费用，不是严格上界；选渠道后
	// 用同一价格快照中的实际模型价格修正。
	return g.billing.PreRequest(tokenID, userID, requestMaxTokens(req), req.Model())
}

// finishRequest 收尾：估算用量 → 算费 → 结算额度差额 → 写日志。
//
// 无论 Relay 成功、失败还是 panic 都会走到这里。
func (g *Gateway) finishRequest(c *executionScope, req protocol.Request, startedAt time.Time, pre *billing.PreDeduct, reqBody []byte) {
	status := scopeStatus(c)
	tokenID := c.result.Identity.TokenID
	userID := c.result.Identity.UserID

	usage := relayUsage(c)
	rawUsage := usage
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
				requestPromptText(req),
				requestCompletionText(req, captureBytes(c)),
			)
			usage = &est
			isEstimated = true
		}
		if g.billing != nil && g.billingMode == billing.ModeLegacy {
			// PriceOf 的第二个返回值是「是否找到价格」——取反才是
			// price_missing 标记。曾经直接把「找到」赋值给「缺失」，
			// 导致有价格的模型全被打上缺价标记（集成测试抓到的）。
			priceFound := false
			cost, priceFound = g.billing.PriceOfSnapshot(pre, usage, relayActualModel(c, req.Model()), req.Model(), relayChannelGroup(c))
			priceMissing = !priceFound
		}
	}
	// legacy 失败时退回预扣；strict 对执行过但结果未知的请求保留占用。

	// 结算：把预扣和实际的差额调整掉
	if g.billing != nil && pre != nil && g.billingMode == billing.ModeLegacy {
		if err := g.billing.Settle(tokenID, userID, pre, cost); err != nil {
			// error：结算失败意味着预扣的钱没被修正，令牌额度会被
			// 永久占用（表现为「没用几次就 403」）。钱相关的问题
			// 不能只是 warn。
			slog.Error("[结算] 额度结算失败，预扣可能未释放",
				"request_id", c.result.Identity.RequestID,
				"token_id", tokenID, "user_id", userID, "err", err)
		}
	}
	if g.billingMode == billing.ModeStrict && pre.BillingID() != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		charge, billingState, err := g.strictBilling.Finish(ctx, pre, rawUsage, relayActualModel(c, req.Model()), relayChannelGroup(c),
			status == http.StatusOK && c.result.Termination == "success")
		cancel()
		c.result.BillingState = billingState
		if err != nil {
			c.result.BillingState = billing.Pending
			slog.Error("strict settlement failed; durable budget hold retained", "billing_id", pre.BillingID(), "err", err)
		} else {
			cost = float64(charge) / float64(billing.UnitScale)
		}
	}

	// TPM 事后修正：把限流窗口里的「预估值」换成真实用量。
	//
	// 放在这里而不是中间件里：真实 usage 只有这一侧拿得到（流式响应
	// 的 usage 要等流结束才知道，中间件的 defer 在 c.Next() 返回时就
	// 跑了，拿不到）。失败请求也补——预估值已经占了窗口的位置，
	// 不退回去等于失败请求白扣 TPM 配额。
	if usage != nil {
		access.FinishTokens(c.ctx, g.limiter, usage.TotalTokens)
	}

	g.writeRequestLog(c, req, startedAt, cost, usage, isEstimated, priceMissing, pre)
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
func (g *Gateway) finishObservability(c *executionScope, req protocol.Request, startedAt time.Time,
	status int, cost float64, usage *model.Usage, priceMissing bool) {
	latency := time.Since(startedAt)
	channel := c.result.ChannelName
	rid := c.result.Identity.RequestID

	attrs := []any{
		"request_id", rid,
		"token_id", c.result.Identity.TokenID,
		"channel_id", c.result.ChannelID,
		"credential_id", c.result.CredentialID,
		"channel", channel,
		"model", req.Model(),
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
	if status >= 400 && c.result.ErrorMessage != "" {
		attrs = append(attrs, "error", c.result.ErrorMessage)
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
	g.metrics.RecordRequest(actualModelFor(c, req.Model()), channel, status, latency)
	if usage != nil {
		g.metrics.RecordTokens(actualModelFor(c, req.Model()), channel,
			metrics.TokenTypePrompt, int64(usage.PromptTokens))
		g.metrics.RecordTokens(actualModelFor(c, req.Model()), channel,
			metrics.TokenTypeCompletion, int64(usage.CompletionTokens))
	}
	g.metrics.RecordCost(actualModelFor(c, req.Model()), channel, cost)
}

// actualModelFor 取映射后的上游模型名（无映射时回落客户端请求名）。
//
// 单独包一层而不是直接调 relayActualModel：那个函数第二参数是
// fallback，语义一样但名字读不出「这是给指标用的」。
func actualModelFor(c *executionScope, fallback string) string {
	return relayActualModel(c, fallback)
}

func captureBytes(c *executionScope) []byte { return c.output.capture.Bytes() }

// Log recording is injected by gateway.Options.
//
// 用函数变量而不是直接依赖 store：relay 是纯转发层，不该知道存储层的存在。
// main 在启动时把 store.WriteLog（内部是异步批量写入）注入进来，
// 测试时不注入就是空操作。

// writeRequestLog 组装并写一条请求日志。
//
// 普通日志允许有界丢弃，不影响已提交的响应；严格账本已单独同步持久化。
func (g *Gateway) writeRequestLog(c *executionScope, req protocol.Request, startedAt time.Time,
	cost float64, usage *model.Usage, isEstimated, priceMissing bool, pre *billing.PreDeduct) {
	if g.logRecorder == nil {
		return
	}
	// 从 context 取 Relay 写入的结果；没拿到说明中途 panic 被 Recovery
	// 吞了——那种情况状态码无从得知，按 500 记（绝不能默认 200，
	// 否则一次彻底失败的请求会被记成成功，账本失去意义）。
	status := scopeStatus(c)
	channelID := c.result.ChannelID
	credentialID := c.result.CredentialID
	tokenID := c.result.Identity.TokenID
	userID := c.result.Identity.UserID

	actualModel := relayActualModel(c, req.Model())
	rid := c.result.Identity.RequestID
	errMsg := c.result.ErrorMessage

	entry := &dbmodel.RequestLog{
		RequestID:    rid,
		UserID:       userID,
		TokenID:      tokenID,
		ChannelID:    channelID,
		CredentialID: credentialID,
		OriginModel:  req.Model(),
		ActualModel:  actualModel,
		IsStream:     req.Stream(),
		Status:       status,
		ErrorMessage: errMsg,
		LatencyMs:    time.Since(startedAt).Milliseconds(),
		Cost:         cost,
		PriceVersion: pre.PriceVersion(),
		BillingID:    pre.BillingID(),
		BillingState: c.result.BillingState,
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
func scopeStatus(c *executionScope) int { return c.result.StatusCode }
func relayUsage(c *executionScope) *model.Usage {
	u := c.result.Usage
	if u != nil && u.TotalTokens == 0 {
		u.TotalTokens = u.PromptTokens + u.CompletionTokens
	}
	return u
}
func relayActualModel(c *executionScope, fallback string) string {
	if m := c.result.ActualModel; m != "" {
		return m
	}
	return fallback
}
func relayChannelGroup(c *executionScope) string {
	if g := c.result.ChannelGroup; g != "" {
		return g
	}
	return "default"
}
