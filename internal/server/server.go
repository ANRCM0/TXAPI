// Package server 负责 HTTP 层：中间件链、路由注册、请求入口。
//
// 分层意图：server 只做「HTTP 相关的事」——解析请求、鉴权、拿 token 信息，
// 然后把干净的数据交给 relay 包去转发。server 不碰上游，relay 不碰 gin 的路由细节。
package server

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"minigateway/internal/config"
	"minigateway/internal/relay"
	"minigateway/internal/server/middleware"
)

// NewEngine 组装中间件链和路由。
//
// 中间件顺序很重要，从上到下依次执行：
//
//	Recovery  → 最外层兜底，防止 panic 打挂整个进程
//	RequestID → 给每个请求一个 ID，后面所有日志都带上它
//	Auth      → 校验令牌，把令牌信息放进上下文
//	Relay     → 真正的转发
func NewEngine(cfg *config.Config) *gin.Engine {
	gin.SetMode(gin.ReleaseMode)
	engine := gin.New()

	engine.Use(middleware.Recovery())
	engine.Use(middleware.RequestID())
	engine.Use(middleware.CORS())

	// 令牌鉴权：把配置里的令牌表交给中间件
	auth := middleware.NewAuth(cfg.Tokens)

	// 健康检查不需要鉴权，方便 Docker healthcheck 和负载均衡探活
	engine.GET("/healthz", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})

	// 模型列表：让客户端知道这个令牌能用哪些模型
	engine.GET("/v1/models", auth.Handle(), relay.ListModels)

	// 转发主入口：对话补全
	engine.POST("/v1/chat/completions", auth.Handle(), relay.HandleChatCompletions)

	// 兜底：其他路径统一返回 404，格式保持 OpenAI 风格，
	// 避免客户端因为拿到 HTML 404 页面而解析失败
	engine.NoRoute(func(c *gin.Context) {
		c.JSON(http.StatusNotFound, gin.H{
			"error": gin.H{
				"message": "网关不支持该路径: " + c.Request.URL.Path,
				"type":    "not_found",
			},
		})
	})

	return engine
}
