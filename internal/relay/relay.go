package relay

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math/rand"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"minigateway/internal/metrics"
	dbmodel "minigateway/internal/model"
	"minigateway/internal/relay/adaptor"
	"minigateway/internal/relay/meta"
	"minigateway/internal/relay/model"
	"minigateway/internal/scheduler"
	"minigateway/internal/server/middleware"
)

// httpClient 是所有上游请求共用的客户端。
//
// 为什么全局复用一个 client：每次请求新建 client 会丢掉连接池，
// 每次都要重新 TCP 握手 + TLS 握手，在高频转发下开销很大。
// 超时在单次请求上用 context 控制，不设 client.Timeout，
// 否则流式响应会在固定时长后被掐断。
var httpClient = &http.Client{
	Transport: &http.Transport{
		MaxIdleConns:        200,
		MaxIdleConnsPerHost: 50,
		IdleConnTimeout:     90 * time.Second,
	},
}

// Relay 执行一次完整的转发，失败时换渠道重试。
//
// 这是网关的主流程，对应 one-api 的 controller/relay.go。
// 流程：选渠道 → 转换请求 → 发请求 → 解析响应 → 失败换渠道重试。
//
// mode 是本次请求的转发模式（对话 / 向量）。由 handler 按路由传入并
// 一路透传到 buildMeta：适配器要靠它决定上游路径（openai 适配器的
// /v1/embeddings 就是靠 Mode 分的支）。以前这里写死对话模式，
// 新增端点时每个调用方都得记得改，透传让「加端点」只动 handler。
func Relay(c *gin.Context, req *model.ModelRequest, body []byte, allowedModels []string, mode int, timeout time.Duration) {
	// 1. 筛选出能服务这个模型的渠道
	candidates := pickChannels(req.Model, allowedModels)
	if len(candidates) == 0 {
		// 记账先行：这个提前返回的分支最容易漏掉状态码记录，
		// 漏了就会把一次彻底失败的请求记成 status=200，账本失去意义
		c.Set(relayStatusKey, http.StatusServiceUnavailable)
		c.Set(relayErrorKey, fmt.Sprintf("没有可用渠道能服务模型 %q", req.Model))
		writeError(c, http.StatusServiceUnavailable,
			fmt.Sprintf("没有可用渠道能服务模型 %q", req.Model), "no_channel")
		return
	}

	retryTimes := cfgRetryTimes
	// 重试预算的上限跟着「重试单元」一起变了：以前单元是渠道，所以拿
	// 渠道数 - 1 当上限；现在单元是 (渠道, 凭据) 对，就该拿「可用凭据
	// 总数 - 1」当上限。否则一个渠道挂 3 把 Key 仍然只允许试 1 次，
	// 多配的 Key 在故障时完全用不上——那是白配。
	if maxRetry := countAvailableCredentials(candidates, time.Now()) - 1; retryTimes > maxRetry {
		retryTimes = maxRetry
	}

	var lastErr *model.ErrorWithStatusCode
	responseCommitted := false
	// 重试单元是「(渠道, 凭据) 对」，不是渠道本身：单渠道多 Key 时，
	// 第一把 Key 失败后必须能在同一把渠道内换一把 Key 重试，否则一个
	// 渠道挂 3 把 Key 也只等于挂 1 把。用**凭据 ID** 记已试而不是渠道
	// ID——凭据 ID 全局唯一，渠道 ID 会把同渠道的其他 Key 一起误判成
	// 「已试过」，那正是这次重构要修掉的毛病。
	triedCred := make(map[uint]bool)
	// 已用过但还有备用的渠道，留到最后兜底（避免「所有渠道都试过一遍就放弃」）
	remaining := candidates
	// 会话 ID 只算一次：整个请求（含重试）必须落在同一个会话上，
	// 否则亲和会把重试打到另一把 Key 上，上游的 prompt 缓存白白作废。
	sessionID := requestSessionID(c, ctxUint(c, middleware.CtxTokenID), body)

	for attempt := 0; attempt <= retryTimes; attempt++ {
		ch, cred := selectTarget(remaining, triedCred, sessionID, time.Now())
		if ch == nil {
			break
		}
		triedCred[cred.ID] = true

		m := buildMeta(ch, cred, req, mode)
		// 记账用：把命中的渠道/凭据写进 context，handler 收尾时取用。
		// 用 context 而不是返回值传递，是因为 Relay 的签名已经很长了，
		// 再加几个返回值会让调用处难读；context 是这次请求的作用域，正合适。
		//
		// 存 uint 而不是 int：RequestLog.ChannelID 是 uint，类型不匹配会让
		// 类型断言静默失败，日志里 channel_id 全是 0，事后查不出走了哪条渠道。
		c.Set(relayChannelKey, uint(ch.id))
		// 指标用的渠道名。channel_id 是数字，作为 Prometheus 标签
		// 可读性差（「哪个渠道在报错」要反查数据库），指标一律用
		// 人可读的名字；logs 表仍然存 ID（要 join）。
		c.Set(relayChannelNameKey, ch.name)
		if credID, ok := m.CredentialID(); ok {
			c.Set(relayCredentialKey, credID)
		}
		c.Set(relayActualModelKey, m.ActualModel)
		// 记账用：命中的渠道分组。重试会不断覆盖它，最终值是
		// 「最后一次尝试的渠道」——和 status/channel_id 一样，
		// 账本只该记录实际服务了这次请求的那个渠道。
		// 分组决定计费倍率，必须等选完渠道才有意义。
		c.Set(relayChannelGroupKey, ch.groupName)

		// 选目标的结果用 debug：正常流量下这是纯噪音（每个请求一条），
		// 但排查「为什么这把 Key 一直不被选中」「亲和有没有生效」
		// 时它是唯一现场——配合 AddSource 还能看到走的是哪条选择
		// 分支（亲和命中 / 加权随机 / 降级）。
		slog.Debug("[调度] 选中转发目标",
			"request_id", c.GetString(middleware.CtxRequestID),
			"channel", ch.name, "channel_id", ch.id,
			"credential_id", cred.ID, "credential_weight", cred.Weight,
			"model", m.ActualModel,
			"session_id", sessionID,
			"attempt", attempt)

		usage, err := doRelay(c, m, body, timeout)
		if err == nil {
			if usage != nil {
				c.Set(relayUsageKey, usage)
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
			reportCredentialHealth(ch.name, cred, http.StatusOK, "")
			c.Set(relayStatusKey, http.StatusOK)
			return
		}
		lastErr = err
		c.Set(relayStatusKey, err.StatusCode)
		c.Set(relayErrorKey, err.Error.Message)
		if c.Writer.Written() {
			// SSE headers/body are already visible; never append an error JSON or retry
			// after the client may have consumed part of this response.
			responseCommitted = true
			if err.StatusCode != 499 {
				reportCredentialHealth(ch.name, cred, err.StatusCode, err.Error.Message)
			}
			break
		}
		// 健康反馈：按状态码决定是否惩罚这把凭据（400/404/422 不罚，
		// 见 scheduler.ShouldPenalizeCredential）。必须在 shouldRetry
		// 判断**之后**：400 这类错误本来就不会重试，但反馈还是要做——
		// 「不重试」和「不惩罚」是两件独立的事。
		reportCredentialHealth(ch.name, cred, err.StatusCode, err.Error.Message)

		// 只有「可重试的错误」才换渠道。
		// 比如 400 参数错误，换渠道也没用，反而浪费一次请求。
		if !shouldRetry(err.StatusCode) {
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
		lastErr = &model.ErrorWithStatusCode{
			StatusCode: http.StatusServiceUnavailable,
			Error:      model.ErrorDetail{Message: "所有可用渠道均不可用", Type: "all_channels_failed"},
		}
	}
	c.Set(relayStatusKey, lastErr.StatusCode)
	c.Set(relayErrorKey, lastErr.Error.Message)
	if responseCommitted {
		return
	}
	writeError(c, lastErr.StatusCode, lastErr.Error.Message, lastErr.Error.Type)
}

// doRelay 向单个渠道发一次请求。
func doRelay(c *gin.Context, m *meta.Meta, body []byte, timeout time.Duration) (*model.Usage, *model.ErrorWithStatusCode) {
	a, ok := reg.getAdaptor(m.ChannelType)
	if !ok {
		return nil, &model.ErrorWithStatusCode{
			StatusCode: http.StatusInternalServerError,
			Error:      model.ErrorDetail{Message: "未找到渠道类型 " + m.ChannelType + " 的适配器", Type: "no_adaptor"},
		}
	}

	url, err := a.GetRequestURL(m)
	if err != nil {
		return nil, &model.ErrorWithStatusCode{
			StatusCode: http.StatusInternalServerError,
			Error:      model.ErrorDetail{Message: "构造上游地址失败: " + err.Error(), Type: "bad_request"},
		}
	}

	// 协议转换：客户端协议 → 规范格式 → 上游协议
	//
	// 客户端协议当前固定是 OpenAI（网关只暴露 /v1/chat/completions 入口）。
	// 如果渠道类型也是 openai，走适配器自己的 ConvertRequest（纯模型名映射，
	// 不碰 body，保持 P1 的透明代理行为）；否则走规范格式两段转换。
	converted, err := convertRequestBody(a, body, m)
	if err != nil {
		return nil, &model.ErrorWithStatusCode{
			StatusCode: http.StatusBadRequest,
			Error:      model.ErrorDetail{Message: "请求转换失败: " + err.Error(), Type: "convert_error"},
		}
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(converted))
	if err != nil {
		return nil, &model.ErrorWithStatusCode{
			StatusCode: http.StatusInternalServerError,
			Error:      model.ErrorDetail{Message: "构造请求失败: " + err.Error(), Type: "internal_error"},
		}
	}
	req.Header.Set("Content-Type", "application/json")
	// 客户端声明的流式偏好要透传给上游，否则上游不会返回 SSE
	if m.IsStream {
		req.Header.Set("Accept", "text/event-stream")
	}
	if err := a.SetupRequestHeader(req, m); err != nil {
		return nil, &model.ErrorWithStatusCode{
			StatusCode: http.StatusInternalServerError,
			Error:      model.ErrorDetail{Message: "设置鉴权头失败: " + err.Error(), Type: "internal_error"},
		}
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		// 网络层错误（连不上、超时、TLS 失败）统一视为 502，可重试
		return nil, &model.ErrorWithStatusCode{
			StatusCode: http.StatusBadGateway,
			Error:      model.ErrorDetail{Message: "请求上游失败: " + err.Error(), Type: "upstream_unreachable"},
		}
	}
	defer resp.Body.Close()

	// 响应方向：上游协议 → 规范 → 客户端协议(OpenAI)
	//
	// openai 渠道走适配器原生的 DoResponse（P1 行为，非流式直接透传）；
	// 其他渠道类型在这里分流：
	//   - 非流式：整体读 body → 转换 → 回写
	//   - 流式：逐行翻译（relayStream），保持打字机效果
	if resp.StatusCode != http.StatusOK {
		return handleUpstreamError(a, resp)
	}
	if needsProtocolConversion(a) {
		if m.IsStream {
			usage, err := relayStream(c, a, resp, m)
			if err != nil {
				status := http.StatusBadGateway
				if c.Request.Context().Err() != nil || errors.Is(err, context.Canceled) {
					status = 499
				}
				return usage, &model.ErrorWithStatusCode{
					StatusCode: status,
					Error:      model.ErrorDetail{Message: "流式转发未正常结束: " + err.Error(), Type: "stream_error"},
				}
			}
			return usage, nil
		}
		return relayNormal(c, a, resp, m)
	}
	return a.DoResponse(c, resp, m)
}

// needsProtocolConversion 判断这个渠道是否需要走协议枢纽。
// openai 渠道不需要（客户端也是 openai，透传即可）。
func needsProtocolConversion(a adaptor.Adaptor) bool {
	pa, ok := a.(adaptor.ProtocolAdaptor)
	if !ok {
		return false
	}
	return pa.Name() != "openai"
}

// relayNormal 处理非流式的协议转换响应。
func relayNormal(c *gin.Context, a adaptor.Adaptor, resp *http.Response, m *meta.Meta) (*model.Usage, *model.ErrorWithStatusCode) {
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, &model.ErrorWithStatusCode{
			StatusCode: http.StatusBadGateway,
			Error:      model.ErrorDetail{Message: "读取上游响应失败: " + err.Error(), Type: "upstream_error"},
		}
	}
	out, usage, err := convertResponseBody(a, body, m)
	if err != nil {
		return nil, &model.ErrorWithStatusCode{
			StatusCode: http.StatusBadGateway,
			Error:      model.ErrorDetail{Message: "响应转换失败: " + err.Error(), Type: "convert_error"},
		}
	}
	c.Data(http.StatusOK, "application/json", out)
	return usage, nil
}

// handleUpstreamError 把上游的错误响应转成网关统一的错误体。
//
// 为什么要统一：上游错误格式各家不同（OpenAI/Claude/Gemini 结构都不一样），
// 而且可能泄露渠道信息（比如 key 的提示）。网关对外只暴露 OpenAI 风格错误。
func handleUpstreamError(a adaptor.Adaptor, resp *http.Response) (*model.Usage, *model.ErrorWithStatusCode) {
	body, _ := io.ReadAll(resp.Body)

	detail := model.ErrorDetail{
		Message: string(body),
		Type:    "upstream_error",
	}
	// 各协议错误体都有 error.message 字段，结构接近，先按通用形状解析
	var parsed struct {
		Error struct {
			Message string `json:"message"`
			Type    string `json:"type"`
			Code    any    `json:"code"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &parsed) == nil && parsed.Error.Message != "" {
		detail.Message = parsed.Error.Message
		detail.Type = parsed.Error.Type
		detail.Code = parsed.Error.Code
	}
	return nil, &model.ErrorWithStatusCode{StatusCode: resp.StatusCode, Error: detail}
}

// buildMeta 把渠道配置和选中的凭据组装成一次转发的上下文。
//
// cred 由调用方选好再传进来（而不是 buildMeta 自己去渠道里挑）：选择
// 时机必须在每次转发尝试时，因为凭据的冷却/拉黑状态随时在变（见
// selectTarget 的注释）。
//
// mode 由 Relay 透传进来，不再在这里写死对话模式：embeddings 端点
// 要的是 /v1/embeddings 路径和只按输入计费，适配器靠 Mode 区分。
func buildMeta(ch *channel, cred *dbmodel.Credential, req *model.ModelRequest, mode int) *meta.Meta {
	actual := req.Model
	if mapped, ok := ch.mapping[req.Model]; ok && mapped != "" {
		actual = mapped
	}
	return &meta.Meta{
		ChannelID:         ch.id,
		ChannelName:       ch.name,
		ChannelType:       ch.typ,
		BaseURL:           ch.baseURL,
		APIKey:            cred.APIKey,
		OriginModel:       req.Model,
		ActualModel:       actual,
		IsStream:          req.Stream,
		Mode:              mode,
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
func selectTarget(candidates []*channel, triedCred map[uint]bool, sessionID string, now time.Time) (*channel, *dbmodel.Credential) {
	pool := make([]*channel, 0, len(candidates))
	total := 0
	// 未试过的候选里的最高优先级。只在这一层里选，低优先级渠道留作兜底。
	maxPriority := 0
	first := true
	for _, ch := range candidates {
		if !hasUntriedCredential(ch, triedCred, now) {
			continue
		}
		if first || ch.priority > maxPriority {
			maxPriority = ch.priority
			first = false
		}
	}
	for _, ch := range candidates {
		if !hasUntriedCredential(ch, triedCred, now) || ch.priority != maxPriority {
			continue
		}
		pool = append(pool, ch)
		total += ch.weight
	}
	if len(pool) == 0 {
		return nil, nil
	}
	// 加权随机：在 [0, total) 里掷一个点，落在谁的区间就是谁
	n := rand.Intn(total)
	ch := pool[len(pool)-1]
	for _, c := range pool {
		n -= c.weight
		if n < 0 {
			ch = c
			break
		}
	}
	cred := pickCredentialWithSession(ch, triedCred, sessionID, now)
	if cred == nil {
		// hasUntriedCredential 已经保证有，防御性返回：真出现说明两个
		// 判断条件不一致，宁可放弃这次尝试也不要发出一个没有 Key 的请求
		return nil, nil
	}
	return ch, cred
}

// hasUntriedCredential 判断渠道里是否还有「没试过且当前可用」的凭据。
func hasUntriedCredential(ch *channel, triedCred map[uint]bool, now time.Time) bool {
	for i := range ch.credentials {
		c := &ch.credentials[i]
		if triedCred[c.ID] {
			continue
		}
		if c.IsUsable(now) && c.APIKey != "" {
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
			if c.IsUsable(now) && c.APIKey != "" {
				n++
			}
		}
	}
	return n
}

// pickCredentialWithSession 在渠道的凭据池里挑一把本次请求要用的 Key。
//
// 默认实现（未注入调度器时）与旧 pickKey **行为等价**：按权重降序、
// 同权重按 ID 升序取第一个可用的。
//
// 注入调度器后（SetCredentialPicker）换成加权随机 + 会话亲和：
// 旧实现会让权重低的凭据永远拿不到流量，权重退化成优先级——运维配
// weight=5 的意图是「这把 Key 分 5 份流量」而不是「这把 Key 优先」。
//
// 亲和只在「首次选择」时起作用：重试时 exclude 里已经有了失败的那把，
// 亲和目标若正是它，调度器会在剩余可用凭据里重新定位——行为上等于降级
// 到加权随机，不会把请求卡死（见 scheduler.PickWithAffinity 的说明）。
func pickCredentialWithSession(ch *channel, triedCred map[uint]bool, sessionID string, now time.Time) *dbmodel.Credential {
	if pickCredentialFn != nil {
		if cred, ok := pickCredentialFn(ch.credentials, triedCred, sessionID, now); ok {
			return cred
		}
		return nil
	}
	var best *dbmodel.Credential
	for i := range ch.credentials {
		c := &ch.credentials[i]
		if triedCred[c.ID] || !c.IsUsable(now) || c.APIKey == "" {
			continue
		}
		if best == nil || c.Weight > best.Weight || (c.Weight == best.Weight && c.ID < best.ID) {
			best = c
		}
	}
	return best
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
func pickChannels(modelName string, allowedModels []string) []*channel {
	if !modelAllowed(modelName, allowedModels) {
		return nil
	}
	reg.mu.RLock()
	defer reg.mu.RUnlock()
	// 两个来源合并：精确命中的渠道 + 通配渠道。
	// 用 seen 去重：一个渠道同时配了具体模型和 "*" 时，
	// 它会在两个 key 下各出现一次，不去重会让它在候选列表里
	// 出现两次——权重随机选中它两次的概率翻倍，等于偷偷改了权重。
	ids := reg.modelIndex[modelName]
	wild := reg.modelIndex["*"]
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
			if ch, ok := reg.channels[id]; ok {
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
//   - 4xx 客户端错误（400/401/403/404）：请求本身有问题，换渠道也一样错，不重试
//   - 429 限流：换渠道很可能成功，重试
//   - 5xx 上游故障：换渠道重试
//   - 408 超时：重试
func shouldRetry(statusCode int) bool {
	switch statusCode {
	case http.StatusTooManyRequests, // 429
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
func writeError(c *gin.Context, statusCode int, message, errType string) {
	if errType == "" {
		errType = "gateway_error"
	}
	c.JSON(statusCode, gin.H{"error": gin.H{"message": message, "type": errType}})
}

// 记账用的 context key。
//
// 为什么 Relay 把结果写进 context 而不是返回：记账信息（命中的渠道、
// 实际模型名、用量、状态码）在重试过程中会不断被覆盖，只有「最后一次
// 尝试的结果」才对账本有意义。写在 context 上，handler 收尾时读到的
// 天然就是最终值，不需要 Relay 再包一个结果结构体往外传。
const (
	relayStatusKey  = "relay_status"
	relayChannelKey = "relay_channel_id"
	// relayChannelNameKey 是命中的渠道名，给 Prometheus 指标当标签
	// （见 Relay 里设置处的注释）。
	relayChannelNameKey  = "relay_channel_name"
	relayCredentialKey   = "relay_credential_id"
	relayActualModelKey  = "relay_actual_model"
	relayUsageKey        = "relay_usage"
	relayErrorKey        = "relay_error"
	relayChannelGroupKey = "relay_channel_group"
)

// cfgRetryTimes 由 Init 从配置写入。
var cfgRetryTimes = 2

// SetRetryTimes 让 main 把配置里的重试次数注入进来。
func SetRetryTimes(n int) { cfgRetryTimes = n }

// credentialPicker 是凭据选择函数，由 main 注入调度器的实现。
//
// 签名与 relay 内部旧逻辑保持一致（返回渠道快照里的元素指针），
// 这样注入与否对调用点完全透明。默认（不注入）走 pickCredentialSort：
// 按权重降序取第一个可用的，与 P6 批次 2 之前行为一致。
//
// 为什么用函数变量而不是让 relay 直接 import scheduler：scheduler 是叶子
// 包，它不能反过来依赖 relay；而 relay 依赖 scheduler 会让「调度器可替换」
// 变成空话（换个实现要改 relay 代码）。函数变量 + SetCredentialPicker 是
// 本项目既有的解耦套路（见 handler.go 的 SetLogRecorder），保持一致。
type credentialPicker func(creds []dbmodel.Credential, exclude map[uint]bool, sessionID string, now time.Time) (*dbmodel.Credential, bool)

var pickCredentialFn credentialPicker

// SetCredentialPicker 注入凭据选择实现。传 nil 恢复默认（按权重降序取第一个）。
func SetCredentialPicker(fn credentialPicker) { pickCredentialFn = fn }

// credentialHealthReporter 是凭据健康状态的反馈函数，由 main 注入。
//
// 与 SetCredentialPicker 同一套路：relay 只负责「把结果告诉调度器」，
// 具体怎么算状态、怎么落库是 scheduler 的事。不注入时（单元测试）
// 反馈是空操作，转发行为与 P6 批次 2 之前完全一致。
type credentialHealthReporter func(cred *dbmodel.Credential, statusCode int, errMsg string)

var reportHealthFn credentialHealthReporter

// SetHealthReporter 注入凭据健康反馈实现。传 nil 关闭反馈。
func SetHealthReporter(fn credentialHealthReporter) { reportHealthFn = fn }

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
func reportCredentialHealth(channel string, cred *dbmodel.Credential, statusCode int, errMsg string) {
	if cred == nil {
		return
	}
	if statusCode != http.StatusOK && scheduler.ShouldPenalizeCredential(statusCode) {
		metrics.Default.RecordCredentialFailure(channel, cred.ID)
	}
	if reportHealthFn == nil {
		return
	}
	reportHealthFn(cred, statusCode, errMsg)
}

// SessionIDHeader 是客户端声明会话 ID 的请求头。
//
// 与 CORS 中间件允许的头保持一致（见 middleware.CORS）：浏览器直连
// 网关时这个头要能过预检，否则亲和在前端场景下永远不生效。
const SessionIDHeader = "X-Session-Id"

// requestSessionID 算出本次请求的会话标识，供凭据亲和用。
//
// 在 relay 里算而不是在 handler 里算：请求体在这里已经是解析好的
// []byte，而首条 user 消息的抽取要看 messages 数组。handler 为了保持
// 「透明代理」只解析了 model/stream/max_tokens 三个字段，再为会话亲和
// 多解析一个 messages 会让那个刻意的边界变模糊（见 handler.go 的说明）。
//
// 取值优先级：X-Session-Id 请求头 > 令牌 ID + 首条 user 消息哈希。
// 都取不到时返回空串，选择逻辑据此降级到纯加权随机。
func requestSessionID(c *gin.Context, tokenID uint, body []byte) string {
	if h := c.GetHeader(SessionIDHeader); h != "" {
		return scheduler.SessionID(h, tokenID, nil)
	}
	var envelope struct {
		Messages []scheduler.Message `json:"messages"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil || len(envelope.Messages) == 0 {
		return ""
	}
	return scheduler.SessionID("", tokenID, envelope.Messages)
}
