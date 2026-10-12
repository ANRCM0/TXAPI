package relay

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"math/rand"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

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
	// 候选渠道数 - 1 是重试次数的上限：不能把同一个渠道试两遍
	if maxRetry := len(candidates) - 1; retryTimes > maxRetry {
		retryTimes = maxRetry
	}

	var lastErr *model.ErrorWithStatusCode
	// 记录已失败的渠道 ID，重试时跳过。
	// 不跳过的后果：一个坏渠道会被反复选中，重试全部浪费在它身上。
	tried := make(map[int]bool, len(candidates))
	// 已用过但还有备用的渠道，留到最后兜底（避免「所有渠道都试过一遍就放弃」）
	remaining := candidates

	for attempt := 0; attempt <= retryTimes; attempt++ {
		ch := selectWeighted(remaining, tried)
		if ch == nil {
			break
		}
		tried[ch.id] = true

		m := buildMeta(ch, req)
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

	// 协议转换：把客户端请求体变成上游能懂的格式
	converted, err := a.ConvertRequest(body, m)
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

	return a.DoResponse(c, resp, m)
}

// buildMeta 把渠道配置和请求特征组装成一次转发的上下文。
func buildMeta(ch *channel, req *model.ModelRequest) *meta.Meta {
	actual := req.Model
	if mapped, ok := ch.mapping[req.Model]; ok && mapped != "" {
		actual = mapped
	}
	return &meta.Meta{
		ChannelID:          ch.id,
		ChannelName:        ch.name,
		ChannelType:        ch.typ,
		BaseURL:            ch.baseURL,
		APIKey:             ch.key,
		OriginModel:        req.Model,
		ActualModel:        actual,
		IsStream:           req.Stream,
		Mode:               meta.ModeChatCompletions,
		CredentialIDValue:  ch.credentialID,
	}
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

// selectWeighted 在候选渠道里挑一个，跳过已试过的。
//
// 两级选择（对应 PLAN 的 priority + weight 语义）：
//  1. 先按 priority 分层，只在高优先级层里选——「主备渠道」就是这个语义，
//     备份渠道不该抢主力渠道的流量
//  2. 同一层内按 weight 加权随机，让运维可以把流量按比例压到不同渠道
//     （比如新渠道先给 10% 流量试水），轮询做不到这一点
//
// 为什么用加权随机而不是纯轮询：权重让「试水比例」可表达；轮询是权重全相等的特例。
func selectWeighted(candidates []*channel, tried map[int]bool) *channel {
	pool := make([]*channel, 0, len(candidates))
	total := 0
	// 未试过的候选里的最高优先级。只在这一层里选，低优先级渠道留作兜底。
	maxPriority := 0
	first := true
	for _, ch := range candidates {
		if tried[ch.id] {
			continue
		}
		if first || ch.priority > maxPriority {
			maxPriority = ch.priority
			first = false
		}
	}
	for _, ch := range candidates {
		if tried[ch.id] || ch.priority != maxPriority {
			continue
		}
		pool = append(pool, ch)
		total += ch.weight
	}
	if len(pool) == 0 {
		return nil
	}
	// 加权随机：在 [0, total) 里掷一个点，落在谁的区间就是谁
	n := rand.Intn(total)
	for _, ch := range pool {
		n -= ch.weight
		if n < 0 {
			return ch
		}
	}
	return pool[len(pool)-1]
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
		http.StatusRequestTimeout,                  // 408
		http.StatusInternalServerError,             // 500
		http.StatusBadGateway,                      // 502
		http.StatusServiceUnavailable,              // 503
		http.StatusGatewayTimeout:                  // 504
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
	relayStatusKey      = "relay_status"
	relayChannelKey     = "relay_channel_id"
	relayCredentialKey  = "relay_credential_id"
	relayActualModelKey = "relay_actual_model"
	relayUsageKey       = "relay_usage"
	relayErrorKey       = "relay_error"
)

// cfgRetryTimes 由 Init 从配置写入。
var cfgRetryTimes = 2

// SetRetryTimes 让 main 把配置里的重试次数注入进来。
func SetRetryTimes(n int) { cfgRetryTimes = n }
