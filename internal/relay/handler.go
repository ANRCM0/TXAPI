package relay

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

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

	Relay(c, &req, body, allowedModels, relayTimeout)
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
