package relay

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	dbmodel "minigateway/internal/model"
	"minigateway/internal/relay/model"
	"minigateway/internal/server/middleware"
)

// relayTimeout 由 main 注入，控制单次上游请求的最长等待时间。
var relayTimeout = 120 * time.Second

// SetTimeout 让 main 把配置里的超时注入进来。
func SetTimeout(d time.Duration) { relayTimeout = d }

// HandleChatCompletions 处理 POST /v1/chat/completions。
//
// 关键设计：请求体只解析 model 和 stream 两个字段，然后**原样透传**给上游。
//
// 为什么不完整建模请求体：LLM 请求的参数在快速演进（新出的 reasoning、
// tools、response_format…）。如果网关用结构体反序列化再序列化，
// 任何未声明的字段都会被静默丢掉，用户会遇到「某个参数在网关后面失效了」
// 这种极难排查的问题。透明代理是网关的基本素养。
func HandleChatCompletions(c *gin.Context) {
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		writeError(c, http.StatusBadRequest, "读取请求体失败: "+err.Error(), "invalid_request_error")
		return
	}
	if len(body) == 0 {
		writeError(c, http.StatusBadRequest, "请求体为空", "invalid_request_error")
		return
	}

	// 只解析路由必需的字段
	var req model.ModelRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeError(c, http.StatusBadRequest, "请求体不是合法 JSON: "+err.Error(), "invalid_request_error")
		return
	}
	if req.Model == "" {
		writeError(c, http.StatusBadRequest, "缺少 model 字段", "invalid_request_error")
		return
	}

	// 从上下文取令牌允许的模型（由鉴权中间件写入）
	allowed, _ := c.Get(middleware.CtxAllowedModel)
	allowedModels, _ := allowed.([]string)
	tokenName, _ := c.Get(middleware.CtxTokenName)
	requestID, _ := c.Get(middleware.CtxRequestID)

	log.Printf("[请求] id=%s 令牌=%v 模型=%s 流式=%v", requestID, tokenName, req.Model, req.Stream)

	// 记录开始时间，用于算整条链路耗时（含重试）
	startedAt := time.Now()

	Relay(c, &req, body, allowedModels, relayTimeout)

	// 记账：转发结束后（无论成败）落一条请求日志。
	// 放在 Relay 之后而不是用 defer：流式响应是在 Relay 内部边收边发的，
	// 函数返回时响应早已写完，此时记账不会影响客户端拿到的任何字节。
	writeRequestLog(c, &req, startedAt)
}

// logRecorder 是日志落库的注入点。
//
// 用函数变量而不是直接依赖 store：relay 是纯转发层，不该知道存储层的存在。
// main 在启动时把 store.WriteLog 注入进来，测试时不注入就是空操作。
// 这也是将来把日志改成异步队列或外发到日志系统的唯一改动点。
var logRecorder func(entry *dbmodel.RequestLog)

// SetLogRecorder 注入日志落库函数。
func SetLogRecorder(fn func(entry *dbmodel.RequestLog)) { logRecorder = fn }

// writeRequestLog 组装并写一条请求日志。
//
// 刻意不阻塞主流程：记账失败只打日志，不影响已经发给客户端的响应。
// 用户请求已经成功了，网关因为自己的账本坏了就报错是不讲道理的。
func writeRequestLog(c *gin.Context, req *model.ModelRequest, startedAt time.Time) {
	if logRecorder == nil {
		return
	}
	// 从 context 取 Relay 写入的结果；没拿到说明中途 panic 被 Recovery 吞了
	status := http.StatusOK
	if v, ok := c.Get(relayStatusKey); ok {
		if code, ok := v.(int); ok {
			status = code
		}
	}
	channelID := uint(0)
	if v, ok := c.Get(relayChannelKey); ok {
		if id, ok := v.(uint); ok {
			channelID = id
		}
	}
	credentialID := uint(0)
	if v, ok := c.Get(relayCredentialKey); ok {
		if id, ok := v.(uint); ok {
			credentialID = id
		}
	}
	actualModel := req.Model
	if v, ok := c.Get(relayActualModelKey); ok {
		if m, ok := v.(string); ok && m != "" {
			actualModel = m
		}
	}
	var usage *model.Usage
	if v, ok := c.Get(relayUsageKey); ok {
		if u, ok := v.(*model.Usage); ok {
			usage = u
		}
	}
	tokenID := uint(0)
	if v, ok := c.Get(middleware.CtxTokenID); ok {
		if id, ok := v.(uint); ok {
			tokenID = id
		}
	}
	requestID, _ := c.Get(middleware.CtxRequestID)
	rid, _ := requestID.(string)
	errMsg := ""
	if v, ok := c.Get(relayErrorKey); ok {
		if m, ok := v.(string); ok {
			errMsg = m
		}
	}

	entry := &dbmodel.RequestLog{
		RequestID:    rid,
		TokenID:      tokenID,
		ChannelID:    channelID,
		CredentialID: credentialID,
		OriginModel:  req.Model,
		ActualModel:  actualModel,
		IsStream:     req.Stream,
		Status:       status,
		ErrorMessage: errMsg,
		LatencyMs:    time.Since(startedAt).Milliseconds(),
	}
	if usage != nil {
		entry.PromptTokens = usage.PromptTokens
		entry.CompletionTokens = usage.CompletionTokens
		entry.TotalTokens = usage.TotalTokens
	}
	logRecorder(entry)
}

// ListModels 处理 GET /v1/models，返回该令牌可访问的模型清单。
//
// 客户端（Cherry Studio、NextChat 这类）靠这个接口自动填充模型下拉框。
func ListModels(c *gin.Context) {
	allowed, _ := c.Get(middleware.CtxAllowedModel)
	allowedModels, _ := allowed.([]string)

	seen := map[string]bool{}
	data := make([]gin.H, 0)

	for _, ch := range reg.allChannels() {
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
