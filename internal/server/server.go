// Package server 负责 HTTP 层：中间件链、路由注册、请求入口。
//
// 分层意图：server 只做「HTTP 相关的事」——解析请求、鉴权、拿 token 信息，
// 然后把干净的数据交给 relay 包去转发。server 不碰上游，relay 不碰 gin 的路由细节。
package server

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"minigateway/internal/billing"
	"minigateway/internal/config"
	"minigateway/internal/relay"
	"minigateway/internal/server/api"
	"minigateway/internal/server/middleware"
	"minigateway/internal/store"
)

// Deps 是组装引擎所需的全部依赖。
//
// 为什么用结构体而不是一长串参数：参数已经多到容易传错位（cfg 和 store 都是指针），
// 具名字段让调用处自解释。
type Deps struct {
	Cfg   *config.Config
	Store *store.Store
	// AdminAuth 是管理 API 的鉴权中间件，nil 表示暂不鉴权（P2 现状）。
	// 留这个字段就是留挂载点：P3 实现管理员鉴权后，在 main 里塞进来即可。
	AdminAuth gin.HandlerFunc
	// Billing 是计费引擎，nil 表示不启用计费（测试场景）。
	// main 在 store 打开后创建它，这里注入给 relay 用。
	Billing *billing.Engine
}

// NewEngine 组装中间件链和路由。
//
// 中间件顺序很重要，从上到下依次执行：
//
//	Recovery  → 最外层兜底，防止 panic 打挂整个进程
//	RequestID → 给每个请求一个 ID，后面所有日志都带上它
//	Auth      → 校验令牌，把令牌信息放进上下文
//	Relay     → 真正的转发
func NewEngine(deps Deps) *gin.Engine {
	gin.SetMode(gin.ReleaseMode)
	engine := gin.New()

	engine.Use(middleware.Recovery())
	engine.Use(middleware.RequestID())
	engine.Use(middleware.CORS())

	// 令牌鉴权：查 store 的内存缓存，改令牌不用重启
	auth := middleware.NewAuth(deps.Store)

	// 计费引擎注入转发层：额度检查/预扣/结算都发生在请求路径上
	relay.SetBilling(deps.Billing)

	// 健康检查不需要鉴权，方便 Docker healthcheck 和负载均衡探活
	engine.GET("/healthz", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})

	// 模型列表：让客户端知道这个令牌能用哪些模型
	engine.GET("/v1/models", auth.Handle(), relay.ListModels)

	// 转发主入口：对话补全
	engine.POST("/v1/chat/completions", auth.Handle(), relay.HandleChatCompletions)

	// 管理 API：渠道/令牌/日志/统计/价格 CRUD。
	// 挂载在这里而不是 engine 上，是为了让 AdminAuth 能统一作用到整个 /api 前缀。
	apiHandler := api.New(deps.Store, func() {
		// 渠道变更后热刷新转发层渠道池：这就是「不重启网关就生效」的那一环
		if err := relay.Reload(deps.Store.Channels()); err != nil {
			// 刷新失败不能影响 API 的返回值（数据已经落库了），
			// 但必须响亮的告警——否则会出现「库里改了、转发还用旧的」的静默故障
			gin.DefaultErrorWriter.Write([]byte("[热更新] 刷新渠道池失败: " + err.Error() + "\n"))
		}
	}, func() {
		// 价格/倍率变更后让计费引擎重读价格表
		if deps.Billing != nil && deps.Billing.Prices() != nil {
			if err := deps.Billing.Prices().ReloadPrices(); err != nil {
				// 重读失败保留旧缓存（见 PriceTable.ReloadPrices），
				// 但同样要告警：价格表静默用旧值等于悄悄算错账
				gin.DefaultErrorWriter.Write([]byte("[热更新] 重读价格表失败: " + err.Error() + "\n"))
			}
		}
	})
	apiHandler.Register(engine.Group(""), deps.AdminAuth)

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
