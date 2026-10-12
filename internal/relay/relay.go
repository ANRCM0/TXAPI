package relay

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	dbmodel "minigateway/internal/model"
	"minigateway/internal/relay/adaptor"
	"minigateway/internal/relay/meta"
	"minigateway/internal/relay/model"
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
func Relay(c *gin.Context, req *model.ModelRequest, body []byte, allowedModels []string, timeout time.Duration) {
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
	// 重试单元是「(渠道, 凭据) 对」，不是渠道本身：单渠道多 Key 时，
	// 第一把 Key 失败后必须能在同一把渠道内换一把 Key 重试，否则一个
	// 渠道挂 3 把 Key 也只等于挂 1 把。用**凭据 ID** 记已试而不是渠道
	// ID——凭据 ID 全局唯一，渠道 ID 会把同渠道的其他 Key 一起误判成
	// 「已试过」，那正是这次重构要修掉的毛病。
	triedCred := make(map[uint]bool)
	// 已用过但还有备用的渠道，留到最后兜底（避免「所有渠道都试过一遍就放弃」）
	remaining := candidates

	for attempt := 0; attempt <= retryTimes; attempt++ {
		ch, cred := selectTarget(remaining, triedCred, time.Now())
		if ch == nil {
			break
		}
		triedCred[cred.ID] = true

		m := buildMeta(ch, cred, req)
		// 记账用：把命中的渠道/凭据写进 context，handler 收尾时取用。
		// 用 context 而不是返回值传递，是因为 Relay 的签名已经很长了，
		// 再加几个返回值会让调用处难读；context 是这次请求的作用域，正合适。
		//
		// 存 uint 而不是 int：RequestLog.ChannelID 是 uint，类型不匹配会让
		// 类型断言静默失败，日志里 channel_id 全是 0，事后查不出走了哪条渠道。
		c.Set(relayChannelKey, uint(ch.id))
		if credID, ok := m.CredentialID(); ok {
			c.Set(relayCredentialKey, credID)
		}
		c.Set(relayActualModelKey, m.ActualModel)
		// 记账用：命中的渠道分组。重试会不断覆盖它，最终值是
		// 「最后一次尝试的渠道」——和 status/channel_id 一样，
		// 账本只该记录实际服务了这次请求的那个渠道。
		// 分组决定计费倍率，必须等选完渠道才有意义。
		c.Set(relayChannelGroupKey, ch.groupName)

		usage, err := doRelay(c, m, body, timeout)
		if err == nil {
			if usage != nil {
				c.Set(relayUsageKey, usage)
				log.Printf("[用量] 渠道=%s 模型=%s 输入=%d 输出=%d 合计=%d",
					ch.name, m.ActualModel, usage.PromptTokens, usage.CompletionTokens, usage.TotalTokens)
			}
			c.Set(relayStatusKey, http.StatusOK)
			return
		}
		lastErr = err
		c.Set(relayStatusKey, err.StatusCode)
		c.Set(relayErrorKey, err.Error.Message)

		// 只有「可重试的错误」才换渠道。
		// 比如 400 参数错误，换渠道也没用，反而浪费一次请求。
		if !shouldRetry(err.StatusCode) {
			log.Printf("[转发失败] 渠道=%s 状态码=%d 不重试: %s", ch.name, err.StatusCode, err.Error.Message)
			break
		}
		log.Printf("[转发失败] 渠道=%s 状态码=%d 准备重试: %s", ch.name, err.StatusCode, err.Error.Message)
	}

	if lastErr == nil {
		lastErr = &model.ErrorWithStatusCode{
			StatusCode: http.StatusServiceUnavailable,
			Error:      model.ErrorDetail{Message: "所有可用渠道均不可用", Type: "all_channels_failed"},
		}
	}
	c.Set(relayStatusKey, lastErr.StatusCode)
	c.Set(relayErrorKey, lastErr.Error.Message)
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
				// 流已开始，无法改状态码；记日志后按「已发出的流」处理
				log.Printf("[流式转发] 渠道=%s 流结束异常: %v", m.ChannelName, err)
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
func buildMeta(ch *channel, cred *dbmodel.Credential, req *model.ModelRequest) *meta.Meta {
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
		Mode:              meta.ModeChatCompletions,
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
// 返回 (nil, nil) 表示没有可试的目标了（所有凭据都试过或都不可用）。
func selectTarget(candidates []*channel, triedCred map[uint]bool, now time.Time) (*channel, *dbmodel.Credential) {
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
	cred := pickCredential(ch, triedCred, now)
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

// pickCredential 在渠道的凭据池里挑一把本次请求要用的 Key。
//
// 本步骤（结构性重构）刻意保持与旧 pickKey **行为等价**：按权重降序、
// 同权重按 ID 升序取第一个可用的，只是执行时机从装载时搬到了每请求时。
// 下一步才换成加权随机 + 冷却 + 拉黑 + 亲和的完整调度器。
//
// 为什么先做等价重构再换策略：一次性改「选择时机 + 选择算法」两件事，
// 出问题时无法判断是结构坏了还是算法错了。分开两步，每步都有独立的
// 回归网（本步骤的回归网就是 P5 的 6 个验收场景）。
func pickCredential(ch *channel, triedCred map[uint]bool, now time.Time) *dbmodel.Credential {
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
func pickChannels(modelName string, allowedModels []string) []*channel {
	if !modelAllowed(modelName, allowedModels) {
		return nil
	}
	all := reg.allChannels()
	out := make([]*channel, 0, len(all))
	for _, ch := range all {
		if ch.supports(modelName) {
			out = append(out, ch)
		}
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
	relayStatusKey       = "relay_status"
	relayChannelKey      = "relay_channel_id"
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
