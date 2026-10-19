package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"minigateway/internal/core"
	"net/http"
	"strings"
	"time"

	"minigateway/internal/billing"
	execution "minigateway/internal/foundation/contracts/execution/v1"
	executionv2 "minigateway/internal/foundation/contracts/execution/v2"
	executionv3 "minigateway/internal/foundation/contracts/execution/v3"
	usagehistory "minigateway/internal/foundation/contracts/usagehistory/v1"
	dbmodel "minigateway/internal/model"
	"minigateway/internal/scheduler"
)

// Relay 执行一次完整的转发，失败时换渠道重试。
//
// 这是网关的主流程，对应 one-api 的 controller/relay.go。
// 流程：选渠道 → 转换请求 → 发请求 → 解析响应 → 失败换渠道重试。
//
// The owned operation envelope binds payload, routing and capability facts for
// every attempt. Integer modes exist only at the legacy adaptor boundary.
func (g *Gateway) relay(c *executionScope, req execution.Request, allowedModels []string, timeout time.Duration) {
	result := c.result
	started := time.Now()
	ctx, cancel := context.WithTimeout(c.ctx, g.totalTimeout)
	defer cancel()
	c.ctx = ctx
	defer func() {
		result.TotalDuration = time.Since(started)
		c.syncOutput()
	}()

	// This exported coordinator also guards policy when called outside the HTTP
	// handler; an extension must never acquire capacity for an unauthorized model.
	if result.Identity.Policy != nil && !result.Identity.Policy.Allows(req.Model()) {
		result.StatusCode = http.StatusForbidden
		result.Error = &core.Error{StatusCode: http.StatusForbidden, Error: core.ErrorDetail{Message: "model is not authorized", Type: "forbidden"}}
		result.ErrorMessage = result.Error.Error.Message
		writeError(c, result.StatusCode, result.ErrorMessage, "forbidden")
		return
	}

	if g.billingMode == billing.ModeStrict && (!g.strictReady() || result.reservation.BillingID() == "" || result.strictRequest == nil) {
		result.StatusCode = http.StatusServiceUnavailable
		result.ErrorMessage = "strict billing is unavailable; requests cannot be charged safely"
		result.Error = &core.Error{StatusCode: result.StatusCode, Error: core.ErrorDetail{Message: result.ErrorMessage, Type: "billing_unavailable"}}
		writeError(c, result.StatusCode, result.ErrorMessage, "billing_unavailable")
		return
	}

	// The request lease spans the complete retry loop; each attempt has a
	// nested lease acquired only after selecting an authorized model target.
	if !c.requestAdmitted {
		requestLease, admitErr := g.acquire(ctx, c.output, AdmissionScope{Level: RequestAdmission, Operation: req.Operation(), UserID: result.Identity.UserID, TokenID: result.Identity.TokenID, Model: req.Model()})
		if admitErr != nil {
			result.StatusCode = admitErr.StatusCode
			result.Error = admitErr
			result.ErrorMessage = admitErr.Error.Message
			writeError(c, admitErr.StatusCode, admitErr.Error.Message, admitErr.Error.Type)
			return
		}
		defer requestLease()
	}

	// 1. 筛选出能服务这个模型的渠道
	if failure := g.prepareManagedScope(c, req, allowedModels); failure != nil {
		result.Error, result.StatusCode, result.ErrorMessage = failure, failure.StatusCode, failure.Error.Message
		writeError(c, failure.StatusCode, failure.Error.Message, failure.Error.Type)
		return
	}
	candidates := g.scopeCandidates(c, req, allowedModels)
	if c.responseTarget != nil && len(candidates) == 0 {
		g.recordScopeRouteDecision(c, routeDecision{outcome: "continuation_unavailable", picker: "none"}, 0)
		writeError(c, http.StatusConflict, "the original Responses target is no longer available", "continuation_target_unavailable")
		return
	}
	if len(candidates) == 0 {
		if len(g.pickChannels(req.Model(), allowedModels)) > 0 {
			g.recordScopeRouteDecision(c, routeDecision{outcome: "capability_unavailable", picker: "none"}, 0)
			if g.billingMode == billing.ModeStrict {
				rejected := g.rejectUnpricedAttempt(c, result.reservation)
				result.Result = rejected
				writeError(c, rejected.StatusCode, rejected.Error.Error.Message, rejected.Error.Error.Type)
			} else {
				writeError(c, http.StatusBadRequest, "no authorized target supports the requested operation", "unsupported_operation")
			}
			return
		}
		// 记账先行：这个提前返回的分支最容易漏掉状态码记录，
		// 漏了就会把一次彻底失败的请求记成 status=200，账本失去意义
		g.recordScopeRouteDecision(c, routeDecision{outcome: "model_unavailable", picker: "none"}, 0)
		c.result.StatusCode = http.StatusServiceUnavailable
		c.result.ErrorMessage = fmt.Sprintf("没有可用渠道能服务模型 %q", req.Model())
		writeError(c, http.StatusServiceUnavailable,
			fmt.Sprintf("没有可用渠道能服务模型 %q", req.Model()), "no_channel")
		return
	}

	retryTimes := g.retryTimes
	// 重试预算的上限跟着「重试单元」一起变了：以前单元是渠道，所以拿
	// 渠道数 - 1 当上限；现在单元是 (渠道, 凭据) 对，就该拿「可用凭据
	// 总数 - 1」当上限。否则一个渠道挂 3 把 Key 仍然只允许试 1 次，
	// 多配的 Key 在故障时完全用不上——那是白配。
	if maxRetry := countAvailableCredentials(candidates, time.Now()) - 1; retryTimes > maxRetry {
		retryTimes = maxRetry
	}

	var lastErr *core.Error
	responseCommitted := false
	// 重试单元是「(渠道, 凭据) 对」，不是渠道本身：单渠道多 Key 时，
	// 第一把 Key 失败后必须能在同一把渠道内换一把 Key 重试，否则一个
	// 渠道挂 3 把 Key 也只等于挂 1 把。用**凭据 ID** 记已试而不是渠道
	// ID——凭据 ID 全局唯一，渠道 ID 会把同渠道的其他 Key 一起误判成
	// 「已试过」，那正是这次重构要修掉的毛病。
	triedCred := make(map[uint]bool)
	// 每次尝试重新读取原授权模型的渠道快照；已试凭据仍由本请求排除。

	// 会话 ID 只算一次：整个请求（含重试）必须落在同一个会话上，
	// 否则亲和会把重试打到另一把 Key 上，上游的 prompt 缓存白白作废。
	sessionID := requestSessionID(c, c.result.Identity.UserID, c.result.Identity.TokenID, req)

	for attempt := 0; attempt <= retryTimes; attempt++ {
		if ctx.Err() != nil {
			code := http.StatusGatewayTimeout
			term := core.Deadline
			if ctx.Err() == context.Canceled {
				code = 499
				term = core.Canceled
			}
			lastErr = &core.Error{StatusCode: code, Error: core.ErrorDetail{Message: "request time budget exhausted or canceled", Type: string(term)}}
			result.Termination = term
			break
		}
		ch, cred := g.selectTargetScope(c, g.scopeCandidates(c, req, allowedModels), triedCred, sessionID, time.Now())
		if ch == nil {
			break
		}
		triedCred[cred.ID] = true

		m := buildMeta(ch, cred, req, c.responseID)
		// 记账用：把命中的渠道/凭据写进 context，handler 收尾时取用。
		// 用 context 而不是返回值传递，是因为 Relay 的签名已经很长了，
		// 再加几个返回值会让调用处难读；context 是这次请求的作用域，正合适。
		//
		// 存 uint 而不是 int：RequestLog.ChannelID 是 uint，类型不匹配会让
		// 类型断言静默失败，日志里 channel_id 全是 0，事后查不出走了哪条渠道。
		c.result.ChannelID = uint(ch.id)
		// 指标用的渠道名。channel_id 是数字，作为 Prometheus 标签
		// 可读性差（「哪个渠道在报错」要反查数据库），指标一律用
		// 人可读的名字；logs 表仍然存 ID（要 join）。
		c.result.ChannelName = ch.name
		if credID, ok := m.CredentialID(); ok {
			c.result.CredentialID = credID
		}
		c.result.ActualModel = m.ActualModel
		if g.operations[req.Operation()].Continuity {
			c.result.ResponseTarget = responseTarget(ch, cred, req.Model())
		}
		// 记账用：命中的渠道分组。重试会不断覆盖它，最终值是
		// 「最后一次尝试的渠道」——和 status/channel_id 一样，
		// 账本只该记录实际服务了这次请求的那个渠道。
		// 分组决定计费倍率，必须等选完渠道才有意义。
		c.result.ChannelGroup = ch.groupName

		// 选目标的结果用 debug：正常流量下这是纯噪音（每个请求一条），
		// 但排查「为什么这把 Key 一直不被选中」「亲和有没有生效」
		// 时它是唯一现场——配合 AddSource 还能看到走的是哪条选择
		// 分支（亲和命中 / 加权随机 / 降级）。
		slog.Debug("[调度] 选中转发目标",
			"request_id", c.result.Identity.RequestID,
			"channel", ch.name, "channel_id", ch.id,
			"credential_id", cred.ID, "credential_weight", cred.Weight,
			"model", m.ActualModel,
			"session_id", sessionID,
			"attempt", attempt)

		attemptLease, admitErr := g.acquire(ctx, c.output, AdmissionScope{Level: AttemptAdmission, Operation: req.Operation(), UserID: result.Identity.UserID, TokenID: result.Identity.TokenID, Model: m.ActualModel, ChannelID: uint(ch.id), CredentialID: cred.ID, Attempt: attempt + 1})
		if admitErr != nil {
			lastErr = admitErr
			break
		}
		attemptResult := func() core.Result {
			defer attemptLease()
			return g.doRelay(c, m, req, timeout)
		}()

		result.Attempts = append(result.Attempts, AttemptResult{Operation: req.Operation(), ChannelID: uint(ch.id), CredentialID: cred.ID, Model: m.ActualModel, Result: attemptResult})
		result.Result = attemptResult
		usage, err := attemptResult.Usage, attemptResult.Error
		if err == nil {
			if usage != nil {
				c.result.Usage = usage
				// debug 而不是 info：上游返回的原始用量在 info 级别
				// 会和请求摘要重复（摘要里也有 token 数），而真正需要
				// 它的场景是「usage 数字对不上」的排查——那时会开
				// debug。字段名与统一字段一致（model/channel），
				// 方便按字段检索而不是按 msg 文本检索。
				slog.Debug("[用量] 上游返回 token 用量",
					"channel", ch.name, "model", m.ActualModel,
					"prompt_tokens", usage.PromptTokens,
					"completion_tokens", usage.CompletionTokens,
					"total_tokens", usage.TotalTokens)
			}
			// 健康反馈：成功即清零。让这把 Key 的冷却/拉黑状态立刻回到
			// 全绿，下一次选择就不会再跳过它。
			g.reportCredentialHealth(ch.name, cred, http.StatusOK, "")
			c.result.StatusCode = http.StatusOK
			c.result.ErrorMessage = ""
			return
		}
		lastErr = err
		c.result.StatusCode = err.StatusCode
		c.result.ErrorMessage = err.Error.Message
		if err.Error.Type == "billing_unavailable" || err.Error.Type == "billing_fee_limit" || err.Error.Type == "unsupported_billing_dimension" || strings.HasPrefix(err.Error.Type, "provider_") {
			responseCommitted = attemptResult.Committed
			break
		}
		if c.usageRecording != nil && c.usageRecording.failed {
			responseCommitted = attemptResult.Committed
			break
		}
		if attemptResult.Committed {
			// SSE headers/body are already visible; never append an error JSON or retry
			// after the client may have consumed part of this response.
			responseCommitted = true
			if ctx.Err() == nil && err.StatusCode != 499 && attemptResult.Termination != core.ClientWriteError {
				g.reportCredentialHealth(ch.name, cred, err.StatusCode, err.Error.Message)
			}
			break
		}
		// 健康反馈：按状态码决定是否惩罚这把凭据（400/404/422 不罚，
		// 见 scheduler.ShouldPenalizeCredential）。必须在 shouldRetry
		// 判断之前：400 这类错误本来就不会重试，但反馈还是要做——
		// 「不重试」和「不惩罚」是两件独立的事。
		if ctx.Err() == nil && attemptResult.Termination != core.ClientWriteError {
			g.reportCredentialHealth(ch.name, cred, err.StatusCode, err.Error.Message)
		}

		// 只有「可重试的错误」才换渠道。
		// 比如 400 参数错误，换渠道也没用，反而浪费一次请求。
		if !attemptResult.Replayable || ctx.Err() != nil || attemptResult.Termination == core.ClientWriteError || !shouldRetry(err.StatusCode) {
			// warn：上游返回了错误状态码。这是「本次请求没成」，
			// 属于必须留痕的事件，但网关本身在正确工作（分类、
			// 不重试都是设计行为），所以不是 error。
			slog.Warn("[转发失败] 上游错误，不重试",
				"channel", ch.name, "model", m.ActualModel,
				"credential_id", cred.ID, "status", err.StatusCode,
				"error", err.Error.Message)
			break
		}
		slog.Warn("[转发失败] 上游错误，准备换目标重试",
			"channel", ch.name, "model", m.ActualModel,
			"credential_id", cred.ID, "status", err.StatusCode,
			"error", err.Error.Message, "attempt", attempt)
	}

	if lastErr == nil {
		lastErr = &core.Error{
			StatusCode: http.StatusServiceUnavailable,
			Error:      core.ErrorDetail{Message: "所有可用渠道均不可用", Type: "all_channels_failed"},
		}
	}
	result.Error = lastErr
	c.result.StatusCode = lastErr.StatusCode
	c.result.ErrorMessage = lastErr.Error.Message
	if responseCommitted || result.Termination == core.ClientWriteError {
		return
	}
	writeError(c, lastErr.StatusCode, lastErr.Error.Message, lastErr.Error.Type)
}

// doRelay 向单个渠道发一次请求。
func (g *Gateway) doRelay(c *executionScope, m *execution.Target, req execution.Request, timeout time.Duration) (result core.Result) {
	number := len(c.result.Attempts) + 1
	facts := &protocolFacts{usage: unknownUsageFact()}
	usage := &usageFacts{usage: unknownDetailedUsageFact()}
	dispatched, returned := false, false
	shadowEnabled := g.shadow != nil && c.source == invocationForeground
	var shadowToken shadowToken
	var rawCoreResult core.Result
	shadowBegun, shadowNormal, shadowLocalFault := false, false, false
	if shadowEnabled {
		defer func() {
			recovered := recover()
			if shadowBegun {
				outcome := shadowIgnored
				if recovered == nil && shadowNormal && returned && !shadowLocalFault && !facts.failed && !c.result.nativeFailed &&
					(c.usageRecording == nil || !c.usageRecording.failed) && c.ctx.Err() == nil {
					outcome = classifyShadowHTTP(rawCoreResult)
				}
				if g.shadow.End(&shadowToken, outcome, g.shadowTime()) && g.metrics != nil {
					g.metrics.RecordShadowSample(outcome == shadowHTTPError)
				}
			}
			if recovered != nil {
				panic(recovered)
			}
		}()
	}
	preparedRecording := false
	rejectedRecording := false
	startedWritten := c.output.written
	defer func() {
		dispatch := execution.Phase(result.Phase)
		if dispatched && !returned {
			dispatch = execution.Dispatching
		}
		if dispatch == "" {
			dispatch = execution.NotDispatched
		}
		c.result.protocolFacts = append(c.result.protocolFacts, executionv2.AttemptFacts{Number: number, Dispatch: dispatch,
			ProtocolComplete: result.ProtocolComplete, DeliveryCommitted: result.Committed, BytesWritten: result.BytesWritten,
			Usage: cloneUsageFact(facts.usage), Native: facts.native})
		c.result.usageFacts = append(c.result.usageFacts, executionv3.AttemptUsage{Number: number, Usage: cloneDetailedUsageFact(usage.usage)})
		if preparedRecording {
			observed := result
			observed.Phase = core.Phase(dispatch)
			if !returned {
				observed.BytesWritten = c.output.written - startedWritten
				func() {
					defer func() {
						if recover() != nil {
							c.usageRecording.failed = true
						}
					}()
					state := c.output.State()
					observed.Committed, observed.HTTPStatus = state.Committed, state.HTTPStatus
				}()
			}
			// Until relay returns, its request result may contain defaults or a previous attempt.
			host := recordingHost(observed)
			c.usageRecording.interruptedHost = &host
			g.observeUsageRecording(c, number, observed, usage.usage)
		}
	}()
	if g.operations[req.Operation()].OpaqueErrors {
		defer func() {
			if !rejectedRecording {
				result = sanitizeOperationFailure(result, g.operations[req.Operation()].Errors, c.result.nativeFailed)
			}
		}()
	}
	if c.usageRecording != nil {
		target := usagehistory.Target{ID: c.usageRecording.id, Number: number, ActualModel: m.ActualModel, Group: c.result.ChannelGroup,
			Protocol: m.ChannelType, ChannelID: uint(m.ChannelID), CredentialID: m.CredentialIDValue}
		if !g.prepareUsageRecording(c, target) {
			rejectedRecording = true
			return usageRecordingFailure()
		}
		preparedRecording = true
	}
	a, ok := g.reg.getAdaptor(m.ChannelType)
	if !ok {
		return core.Result{Phase: core.NotDispatched, StatusCode: 500, Error: &core.Error{StatusCode: 500, Error: core.ErrorDetail{Message: "no upstream adaptor", Type: "no_adaptor"}}}
	}
	if g.billingMode == billing.ModeStrict && req.Operation() == execution.Embeddings {
		name := a.Name()
		known := name != ""
		if !known || name != "openai" {
			return g.rejectUnpricedAttempt(c, c.result.reservation)
		}
	}
	prepared, err := a.Prepare(req, *m)
	input := ToCoreInput(prepared)
	hasFacts := false
	if richDecoder, ok := prepared.Decoder.(executionv3.Decoder); ok && !nilInterface(richDecoder) {
		hasFacts = true
		input.Decoder = usageDecoderBridge{decoder: richDecoder, facts: facts, usage: usage}
	} else if richDecoder, ok := prepared.Decoder.(executionv2.Decoder); ok && !nilInterface(richDecoder) {
		hasFacts = true
		input.Decoder = factsDecoderBridge{decoder: richDecoder, facts: facts}
	}
	if err == nil && (prepared.Stream != req.Stream() || nilInterface(prepared.Decoder)) {
		err = &execution.Error{StatusCode: 400, Error: execution.ErrorDetail{Message: "prepared request does not match operation transport", Type: "unsupported_operation"}}
	}
	input.Replayable = input.Replayable && !g.operations[req.Operation()].NonReplayable
	if err != nil {
		if g.billingMode == billing.ModeStrict {
			return g.rejectUnpricedAttempt(c, c.result.reservation)
		}
		return core.Result{Phase: core.NotDispatched, StatusCode: err.StatusCode, Error: toCoreError(err), Termination: core.UpstreamError}
	}
	input.Timeout = timeout
	if failure := g.authorizeManagedInput(c, m, req, &input); failure != nil {
		term := core.UpstreamError
		if c.ctx.Err() != nil {
			failure = admissionFailure(c.ctx, nil)
			term = core.Deadline
			if c.ctx.Err() == context.Canceled {
				term = core.Canceled
			}
		}
		return core.Result{Phase: core.NotDispatched, StatusCode: failure.StatusCode, Error: failure, Termination: term}
	}
	pre := c.result.reservation
	if g.billingMode == billing.ModeStrict {
		name := a.Name()
		known := name != ""
		if !known || billing.ValidatePreparedRequest(input.Body, name, req.Operation() == execution.Embeddings) != nil {
			return g.rejectUnpricedAttempt(c, pre)
		}
		if req.Operation() == execution.Chat {
			limits := c.result.strictRequest
			if limits == nil {
				return core.Result{Phase: core.NotDispatched, StatusCode: 503, Error: &core.Error{StatusCode: 503, Error: core.ErrorDetail{
					Message: "strict request limits are unavailable", Type: "billing_unavailable",
				}}}
			}
			if err := a.CheckOutputLimit(bytes.Clone(input.Body), limits.OutputLimit, limits.OutputLimitField); err != nil {
				return g.rejectUnpricedAttempt(c, pre)
			}
		}
		if !known || c.result.strictRequest == nil {
			return core.Result{Phase: core.NotDispatched, StatusCode: 503, Error: &core.Error{StatusCode: 503, Error: core.ErrorDetail{Message: "provider cost contract is unavailable", Type: "provider_cost_contract_missing"}}}
		}
		err := g.strictBilling.StartPreparedAttempt(c.ctx, pre, dbmodel.BillingAttempt{
			Number: number, ChannelID: uint(m.ChannelID), CredentialID: m.CredentialIDValue,
			ActualModel: m.ActualModel, Group: c.result.ChannelGroup,
		}, billing.ProviderTarget{ChannelID: uint(m.ChannelID), BaseURL: m.BaseURL, Protocol: name, ActualModel: m.ActualModel, Operation: string(req.Operation())}, *c.result.strictRequest, input.URL, bytes.Clone(input.Body), input.Header.Clone())
		if err != nil {
			code, kind, message := 503, "billing_unavailable", "billing attempt could not be recorded"
			switch {
			case errors.Is(err, billing.ErrProviderContract):
				kind, message = "provider_cost_contract_missing", err.Error()
			case errors.Is(err, billing.ErrInputUnproven):
				kind, message = "provider_input_unproven", err.Error()
			case errors.Is(err, billing.ErrTariffUnproven):
				kind, message = "provider_tariff_unproven", err.Error()
			case errors.Is(err, billing.ErrCredentialTariff):
				kind, message = "provider_credential_unproven", err.Error()
			case errors.Is(err, billing.ErrProviderBudget):
				kind, message = "provider_budget_exhausted", err.Error()
			case errors.Is(err, billing.ErrProviderLimit):
				code, kind, message = 400, "provider_cost_limit", err.Error()
			case errors.Is(err, billing.ErrFeeBound):
				code, kind, message = 400, "billing_fee_limit", err.Error()
			}
			return core.Result{Phase: core.NotDispatched, StatusCode: code, Error: &core.Error{StatusCode: code, Error: core.ErrorDetail{Message: message, Type: kind}}}
		}
	}
	// If an injected executor/transport panics, non-execution is not established.
	if !g.markUsageDispatch(c, number) {
		rejectedRecording = true
		return usageRecordingFailure()
	}
	c.result.Phase = core.Dispatching
	dispatched = true
	if shadowEnabled {
		shadowToken = g.shadow.Begin(c.result.selectedEpoch, uint(m.ChannelID), g.shadowTime())
		shadowBegun = true
	}
	wireContext := c.ctx
	var managedObservation managedWireObservation
	if c.managedTarget != nil {
		wireContext = context.WithValue(wireContext, managedWireKey{}, true)
		wireContext = context.WithValue(wireContext, managedObservationKey{}, &managedObservation)
	}
	result = g.executor.ExecuteTo(wireContext, c.output, input)
	g.observeManagedResult(c, &managedObservation, prepared.Decoder, result)
	rawCoreResult = result
	returned = true
	var observation execution.Observation
	if hasFacts {
		observation = execution.Observation{Native: facts.native, Failed: facts.failed}
	} else if decoder, ok := prepared.Decoder.(execution.Observer); ok {
		observation = decoder.Observation()
		facts.native = observation.Native
	}
	if hasFacts || observation.Native.ID != "" || observation.Failed || observation.Usage != nil {
		c.result.nativeFailed = observation.Failed
		c.result.ResponseStatus, c.result.UpstreamResponseID = observation.Native.Status, observation.Native.ID
		if c.managedTarget != nil && result.ProtocolComplete && observation.Native.Status != "completed" {
			observation.Failed = true
			c.result.nativeFailed = true
		}
		if observation.Usage != nil {
			result.Usage = toCoreUsage(observation.Usage)
		}
		if result.Error == nil && observation.Failed {
			result.StatusCode, result.Termination = http.StatusBadGateway, core.UpstreamError
			result.Error = &core.Error{StatusCode: http.StatusBadGateway, Error: core.ErrorDetail{Message: g.operations[req.Operation()].Errors.Generation, Type: "upstream_response_failed"}}
		}
	}
	if g.operations[req.Operation()].NonReplayable {
		result.Replayable = false
	}
	if g.billingMode == billing.ModeStrict {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		err := g.strictBilling.EndAttempt(ctx, pre, number, result.StatusCode, string(result.Termination), fromCoreUsage(result.Usage))
		cancel()
		if err != nil {
			shadowLocalFault = true
			result.StatusCode, result.Termination = 503, core.UpstreamError
			kind, message := "billing_unavailable", "billing attempt result could not be recorded"
			if errors.Is(err, billing.ErrProviderLimit) {
				kind, message = "provider_contract_violation", "upstream usage violates the pinned provider cost contract"
			}
			result.Error = &core.Error{StatusCode: 503, Error: core.ErrorDetail{Message: message, Type: kind}}
		}
	}
	g.metrics.RecordAttempt(c.result.ChannelName, string(result.Termination), result.StatusCode, result.Duration)
	shadowNormal = true
	return result
}

func (g *Gateway) rejectUnpricedAttempt(c *executionScope, pre *billing.PreDeduct) core.Result {
	code, kind, message := 400, "unsupported_billing_dimension", "prepared request has no verified text-only billing shape or output limit"
	if err := g.strictBilling.RecordCostRefusal(c.ctx, pre, billing.ErrUnpricedDimension); !errors.Is(err, billing.ErrUnpricedDimension) {
		code, kind, message = 503, "billing_unavailable", "billing refusal could not be recorded"
	}
	return core.Result{Phase: core.NotDispatched, StatusCode: code, Error: &core.Error{StatusCode: code, Error: core.ErrorDetail{
		Message: message, Type: kind,
	}}, Termination: core.UpstreamError}
}

// buildMeta 把渠道配置和选中的凭据组装成一次转发的上下文。
//
// cred 由调用方选好再传进来（而不是 buildMeta 自己去渠道里挑）：选择
// 时机必须在每次转发尝试时，因为凭据的冷却/拉黑状态随时在变（见
// selectTarget 的注释）。
//
// The operation supplies the legacy adaptor mode; callers cannot override it.
func buildMeta(ch *channel, cred *dbmodel.Credential, req execution.Request, responseID string) *execution.Target {
	actual := req.Model()
	if mapped, ok := ch.mapping[req.Model()]; ok && mapped != "" {
		actual = mapped
	}
	return &execution.Target{
		ResponseID:        responseID,
		ChannelID:         ch.id,
		ChannelName:       ch.name,
		ChannelType:       ch.typ,
		BaseURL:           ch.baseURL,
		APIKey:            cred.APIKey,
		OriginModel:       req.Model(),
		ActualModel:       actual,
		IsStream:          req.Stream(),
		Operation:         req.Operation(),
		CredentialIDValue: cred.ID,
	}
}

// selectTarget 选一个「渠道 + 一把未试过的可用凭据」。
//
// 与旧版 selectWeighted 的两处关键区别：
//  1. 渠道筛选条件从「没试过」变成「还有未试过的可用凭据」——一个渠道
//     被试过一把 Key 不等于整个渠道废了，剩下的 Key 仍然值得试；
//  2. 选中渠道后再在渠道内选凭据，两级选择各自独立。
//
// sessionID 非空时凭据选择走会话亲和（见 pickCredentialWithSession）。
//
// 返回 (nil, nil) 表示没有可试的目标了（所有凭据都试过或都不可用）。
func (g *Gateway) selectTarget(candidates []*channel, triedCred map[uint]bool, sessionID string, now time.Time) (*channel, *dbmodel.Credential) {
	return g.selectTargetContext(context.Background(), candidates, triedCred, sessionID, now)
}

func effectiveChannelWeight(weight int) int {
	if weight <= 0 {
		return 1
	}
	if weight > 1_000_000 {
		return 1_000_000
	}
	return weight
}

func chooseWeightedChannel(pool []*channel, randIntn func(int) int) *channel {
	if len(pool) == 0 {
		return nil
	}
	total := 0
	for _, ch := range pool {
		total += effectiveChannelWeight(ch.weight)
	}
	n := randIntn(total)
	for _, ch := range pool {
		n -= effectiveChannelWeight(ch.weight)
		if n < 0 {
			return ch
		}
	}
	return pool[len(pool)-1]
}

// hasUntriedCredential 判断渠道里是否还有「没试过且当前可用」的凭据。
func hasUntriedCredential(ch *channel, triedCred map[uint]bool, now time.Time) bool {
	for i := range ch.credentials {
		c := &ch.credentials[i]
		if triedCred[c.ID] {
			continue
		}
		if availableCredential(c, now) {
			return true
		}
	}
	return false
}

// countAvailableCredentials 统计候选渠道里一共有多少把可用凭据。
//
// 用作重试预算的上限（见 Relay）。按凭据统计而不是按渠道统计，
// 因为重试单元已经是 (渠道, 凭据) 对。
func countAvailableCredentials(candidates []*channel, now time.Time) int {
	n := 0
	for _, ch := range candidates {
		for i := range ch.credentials {
			c := &ch.credentials[i]
			if availableCredential(c, now) {
				n++
			}
		}
	}
	return n
}

func (g *Gateway) pickCredentialWithSession(ch *channel, triedCred map[uint]bool, sessionID string, now time.Time) *dbmodel.Credential {
	credential, _ := g.pickCredentialDecision(ch, triedCred, sessionID, now)
	return credential
}

// pickChannels 从渠道池里筛出能服务该模型、且令牌有权限的渠道。
//
// P6 批次 3 起走 modelIndex 而不是全表扫描：一次 map 命中拿到候选
// 渠道 ID 列表，再按 ID 取渠道对象。渠道多的时候省掉的是
// 「遍历全部渠道 × 逐个解析 JSON models」这份每请求都付的开销。
//
// 通配渠道（models 里配了 "*"）在索引里存在 "*" 这个 key 下，
// 查询时与精确命中的结果合并——和 abilities 表的语义一致
// （表里也是存 model="*" 一行，不展开）。
func (g *Gateway) pickChannels(modelName string, allowedModels []string) []*channel {
	if !modelAllowed(modelName, allowedModels) {
		return nil
	}
	g.reg.mu.RLock()
	defer g.reg.mu.RUnlock()
	// 两个来源合并：精确命中的渠道 + 通配渠道。
	// 用 seen 去重：一个渠道同时配了具体模型和 "*" 时，
	// 它会在两个 key 下各出现一次，不去重会让它在候选列表里
	// 出现两次——权重随机选中它两次的概率翻倍，等于偷偷改了权重。
	ids := g.reg.modelIndex[modelName]
	wild := g.reg.modelIndex["*"]
	seen := make(map[int]bool, len(ids)+len(wild))
	out := make([]*channel, 0, len(ids)+len(wild))
	appendCh := func(list []int) {
		for _, id := range list {
			if seen[id] {
				continue
			}
			seen[id] = true
			// 索引与渠道池同生命周期重建，正常情况下一定命中；
			// 防御一下：真取不到说明两者不同步，跳过比 panic 好
			// （一次路由失败只是一个请求 503，panic 会打挂进程）。
			if ch, ok := g.reg.channels[id]; ok {
				out = append(out, ch)
			}
		}
	}
	appendCh(ids)
	if modelName != "*" {
		appendCh(wild)
	}
	return out
}

// modelAllowed 判断令牌是否允许访问该模型。allowedModels 为空表示不限制。
func modelAllowed(modelName string, allowedModels []string) bool {
	if len(allowedModels) == 0 {
		return true
	}
	for _, m := range allowedModels {
		if m == "*" || m == modelName {
			return true
		}
	}
	return false
}

// shouldRetry 判断这个状态码是否值得换渠道重试。
//
// 分类依据：
//   - 400/404/422 等客户端错误不重试；上游 401/403 属于所选凭据，可换 Key。
//   - 429 限流：换渠道很可能成功，重试
//   - 5xx 上游故障：换渠道重试
//   - 408 超时：重试
func shouldRetry(statusCode int) bool {
	switch statusCode {
	case http.StatusUnauthorized, http.StatusForbidden,
		http.StatusTooManyRequests,     // 429
		http.StatusRequestTimeout,      // 408
		http.StatusInternalServerError, // 500
		http.StatusBadGateway,          // 502
		http.StatusServiceUnavailable,  // 503
		http.StatusGatewayTimeout:      // 504
		return true
	}
	// 其余 5xx 也重试
	return statusCode >= 500
}

// writeError 按 OpenAI 的错误格式回给客户端。
func writeError(c *executionScope, statusCode int, message, errType string) {
	if errType == "" {
		errType = "gateway_error"
	}
	if c.output.State().Committed {
		return
	}
	c.result.StatusCode = statusCode
	c.result.ErrorMessage = message
	c.result.Error = &core.Error{StatusCode: statusCode, Error: core.ErrorDetail{Message: message, Type: errType}}
	defer c.syncOutput()
	if err := c.output.Commit(c.ctx, core.ResponseMetadata{StatusCode: statusCode, ContentType: "application/json; charset=utf-8"}); err != nil {
		c.result.Termination = core.ClientWriteError
		return
	}
	if !c.output.State().Committed {
		c.result.Termination = core.ClientWriteError
		return
	}
	body, _ := json.Marshal(map[string]any{"error": map[string]string{"message": message, "type": errType}})
	if _, err := c.output.Write(c.ctx, append(body, '\n')); err != nil {
		c.result.Termination = core.ClientWriteError
	}
}

type credentialPicker func(creds []dbmodel.Credential, exclude map[uint]bool, sessionID string, now time.Time) (*dbmodel.Credential, bool)

type credentialHealthReporter func(cred *dbmodel.Credential, statusCode int, errMsg string)

// reportCredentialHealth 把一次转发结果反馈给调度器。
//
// 放在 Relay 里而不是 handler 里：只有 Relay 知道「这次用的是哪把
// 凭据、结果如何」，handler 那层只看到最终状态码（重试后的），会把
// 中间失败的那把 Key 漏掉——而那把恰恰是最该被冷却的。
//
// 同时在这里埋凭据失败指标：与调度器共用同一个「该不该罚」的判定
// （scheduler.ShouldPenalizeCredential），保证「被惩罚的失败」和
// 「被计数的失败」永远是同一批——两处各写一遍判定的话，以后改分类
// 规则一定会漏改一处，指标和冷却行为就会对不上。
func (g *Gateway) reportCredentialHealth(channel string, cred *dbmodel.Credential, statusCode int, errMsg string) {
	if cred == nil || isManaged(cred) {
		return
	}
	if statusCode != http.StatusOK && scheduler.ShouldPenalizeCredential(statusCode) {
		g.metrics.RecordCredentialFailure(channel, cred.ID)
	}
	observed := g.observeHealth(cred, statusCode, errMsg)
	if observed == nil {
		return
	}
	if g.healthReporter == nil {
		return
	}
	g.healthReporter(observed, statusCode, errMsg)
}

// SessionIDHeader 是客户端声明会话 ID 的请求头。
//
// 与 CORS 中间件允许的头保持一致（见 middleware.CORS）：浏览器直连
// 网关时这个头要能过预检，否则亲和在前端场景下永远不生效。
const SessionIDHeader = "X-Session-Id"

// requestSessionID scopes only the neutral affinity projection; codecs own wire parsing.
func requestSessionID(c *executionScope, userID, tokenID uint, req execution.Request) string {
	return scheduler.SessionKey(c.sessionID, userID, tokenID, req.AffinityText())
}

func (g *Gateway) capableChannels(req execution.Request, allowed []string) []*channel {
	candidates := g.pickChannels(req.Model(), allowed)
	filtered := make([]*channel, 0, len(candidates))
	for _, ch := range candidates {
		a, ok := g.reg.getAdaptor(ch.typ)
		if ok && a.Supports(req) {
			filtered = append(filtered, ch)
		}
	}
	return filtered
}
