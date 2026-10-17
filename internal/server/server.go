// Package server 负责 HTTP 层：中间件链、路由注册、请求入口。
//
// 分层意图：server 只做「HTTP 相关的事」——解析请求、鉴权、拿 token 信息，
// 然后把干净的数据交给 relay 包去转发。server 不碰上游，relay 不碰 gin 的路由细节。
package server

import (
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"

	"minigateway/internal/billing"
	"minigateway/internal/config"
	"minigateway/internal/gateway"
	"minigateway/internal/metrics"
	"minigateway/internal/server/api"
	"minigateway/internal/server/middleware"
	"minigateway/internal/store"
	"minigateway/web"
)

// Deps 是组装引擎所需的全部依赖。
//
// 为什么用结构体而不是一长串参数：参数已经多到容易传错位（cfg 和 store 都是指针），
// 具名字段让调用处自解释。
type Deps struct {
	Gateway *gateway.Gateway
	Cfg     *config.Config
	Store   *store.Store
	// AdminAuth 是管理 API 的鉴权中间件，**必填**。
	// 管理接口能改渠道、看上游 Key 明文，nil 意味着整个 /api 裸奔，
	// 所以漏传时 NewEngine 直接 panic——装配错误要在启动时炸出来，
	// 而不是上线一个「以为有鉴权其实没有」的网关。
	// 密钥来源见 config.EnvAdminKey（环境变量优先于配置文件）。
	AdminAuth gin.HandlerFunc
	// Billing 是计费引擎，nil 表示不启用计费（测试场景）。
	// main 同时注入 gateway 和管理价格刷新路径。
	Billing *billing.Engine
	// RateLimiter 是限流器，nil 表示不启用限流（测试场景）。
	// 令牌级和 IP 级共用同一个实例：同一个内存表、同一份配置，
	// 两边的 key 前缀不同（tok:/ip:）所以不会互相串账。
	RateLimiter middleware.Limiter
	// IPRPMLimit 是 IP 级限流的每分钟请求数上限，0 表示不限
	// （与令牌级阈值的 0=不限 语义一致）。
	IPRPMLimit int
}

// NewEngine 组装中间件链和路由。
//
// 中间件顺序很重要，从上到下依次执行：
//
//	Recovery   → 最外层兜底，防止 panic 打挂整个进程
//	RequestID  → 给每个请求一个 ID，后面所有日志都带上它
//	IPRateLimit → IP 级兜底闸门（防无令牌的暴力探测）
//	CORS       → 预检请求不带令牌，必须在鉴权之前
//	Auth       → 校验令牌，把令牌信息（含限流阈值）放进上下文
//	TokenRateLimit → 令牌级限流，必须在 Auth 之后（要按令牌 ID 分桶）
//	Relay      → 真正的转发
func NewEngine(deps Deps) *gin.Engine {
	// 管理 API 鉴权是安全底线，没有「忘了传也凑合能跑」这个选项：
	// nil 时裸奔的 /api 等于把凭据池敞开，所以这里硬失败
	if deps.AdminAuth == nil {
		panic("server.NewEngine: Deps.AdminAuth 不能为 nil（管理 API 必须鉴权）")
	}
	if deps.Gateway == nil {
		panic("server.NewEngine: Deps.Gateway must be explicitly supplied")
	}

	gin.SetMode(gin.ReleaseMode)
	engine := gin.New()

	engine.Use(middleware.Recovery())
	engine.Use(middleware.RequestID())

	// 健康检查不需要鉴权，方便 Docker healthcheck 和负载均衡探活。
	//
	// 特意注册在 IP 级限流（下面的 engine.Use）**之前**：gin 的 Use
	// 只对之后注册的路由生效，先注册就天然绕过了限流。这是故意的——
	// 探活被限流打挂会让负载均衡把健康的网关摘掉，那是自伤；
	// 探活的调用方是基础设施，本来也不该被算进业务配额。
	// 注意 Recovery/RequestID 在上面已经挂上了，探活仍然享受这两层。
	engine.GET("/healthz", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})

	// Prometheus 抓取端点，与 /healthz 同样免鉴权、免限流。
	//
	// 为什么不鉴权：抓取方是 Prometheus（或临时 curl 的运维），
	// 它没有、也不该有客户端令牌；给监控系统单独发一把管理密钥
	// 意味着「密钥进了监控系统的配置」，比不鉴权的暴露面更大。
	// 指标本身只有聚合计数和标签（模型名/渠道名），不含请求内容、
	// 不含任何 Key——泄漏它的后果是「竞争对手看到你的调用量」，
	// 而不是「凭据被盗」。真要在公网暴露时，正确做法是在反向代理
	// 层按来源 IP 放行 /metrics（和 /healthz 一样），而不是在应用层
	// 加一套监控专用鉴权。
	//
	// 为什么也绕过限流：抓取通常 15~30s 一次，但误配成 1s 一次时
	// 限流会把抓取打挂，表现为「监控图上突然全是断点」——监控自己
	// 被打挂是最难排查的一类故障，而它本来就不该占业务配额。
	//
	// 凭据状态 provider 在这里注入而不是 main：只有 server 层同时
	// 看得见 relay 的渠道池，且注入点紧挨着使用处。
	deps.Gateway.Metrics().SetCredentialStatsProvider(deps.Gateway.CredentialStats)
	deps.Gateway.Metrics().SetLogStatsProvider(func() metrics.LogQueueStats {
		stats := deps.Store.LogQueueStats()
		return metrics.LogQueueStats{Depth: stats.Depth, Capacity: stats.Capacity, Dropped: stats.Dropped, Failed: stats.Failed}
	})
	engine.GET("/metrics", func(c *gin.Context) {
		// Content-Type 必须带 version=0.0.4：Prometheus 按它判断
		// 解析器，缺了会走文本兜底且告警（多数情况下仍能解析，
		// 但那是未定义行为）。
		c.Header("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		// 直接写给 ResponseWriter 而不是先拼字符串：抓取时渠道池
		// 的读锁会被 provider 持有一次， Render 内部不再加锁，
		// 所以这里没有「大字符串占内存」的问题。
		if err := deps.Gateway.Metrics().Render(c.Writer); err != nil {
			// 写一半失败（客户端断开）时 gin 已经不能改状态码了，
			// 只能留痕。抓取失败的对岸是 Prometheus 的 target down，
			// 服务端日志是唯一线索。
			slog.Error("[指标] 写出 /metrics 失败", "err", err)
		}
	})

	engine.Use(middleware.RequestBodyLimit(middleware.MaxRequestBodyBytes))

	// IP 级限流：兜底防「拿随机 key 试」的探测请求。它不依赖令牌信息，
	// 所以挂在最前面，越早拦住越省（详见 middleware.IPRateLimit）。
	engine.Use(middleware.IPRateLimit(deps.RateLimiter, deps.IPRPMLimit))
	engine.Use(middleware.CORS())
	for _, path := range []string{"/", "/ui/:asset"} {
		engine.GET(path, func(c *gin.Context) { web.ServeHTTP(c.Writer, c.Request) })
		engine.HEAD(path, func(c *gin.Context) { web.ServeHTTP(c.Writer, c.Request) })
	}

	// 令牌鉴权：查 store 的内存缓存，改令牌不用重启
	auth := middleware.NewAuth(deps.Store)

	// 计费引擎注入转发层：额度检查/预扣/结算都发生在请求路径上

	// 限流器注入转发层：TPM 是「请求前预估、响应后按真实用量修正」的
	// 两段式，真实 usage 只有 relay 的收尾逻辑拿得到（见 handler.go）。

	// 模型列表：让客户端知道这个令牌能用哪些模型
	engine.GET("/v1/models", auth.Handle(), deps.Gateway.ListModels)

	// 转发主入口：对话补全。
	// 鉴权 → 令牌限流 → 转发：限流必须挂在 Auth **之后**，
	// 它要按令牌 ID 分桶、读该令牌的 RPM/TPM 阈值；挂在前面拿不到
	// 令牌，只能退化成 IP 级，「每个客户端一把钥匙」的粒度就丢了。
	// 它也必须是**请求前**的闸门——P5 的额度是响应后结算，
	// 防不住 1000 个并发在结算前全打到上游（见 ratelimit.go 头注）。
	engine.POST("/v1/chat/completions", auth.Handle(),
		middleware.TokenRateLimit(deps.RateLimiter),
		deps.Gateway.HandleChatCompletions)

	// embeddings：与对话共用整条转发链路，区别只有 Mode 一个值。
	// 限流同样按令牌生效——embedding 请求同样在烧上游 Key。
	engine.POST("/v1/embeddings", auth.Handle(),
		middleware.TokenRateLimit(deps.RateLimiter),
		deps.Gateway.HandleEmbeddings)

	// 管理 API：渠道/令牌/日志/统计/价格 CRUD。
	// 挂载在这里而不是 engine 上，是为了让 AdminAuth 能统一作用到整个 /api 前缀。
	apiHandler := api.NewWithBilling(deps.Store, func() error {
		channels, err := deps.Store.ChannelSnapshot()
		if err != nil {
			return err
		}
		return deps.Gateway.Publish(channels)
	}, func() error {
		// 价格/倍率变更后让计费引擎重读价格表
		if deps.Billing != nil && deps.Billing.Prices() != nil {
			return deps.Billing.Prices().ReloadPrices()
		}
		return nil
	}, deps.Billing)
	apiHandler.Register(engine.Group(""), deps.AdminAuth)
	apiHandler.RegisterAccess(engine.Group(""))

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
