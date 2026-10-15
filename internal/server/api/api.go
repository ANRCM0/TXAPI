// Package api 提供管理 REST API：渠道 / 凭据 / 令牌 / 日志 / 统计 / 价格的 CRUD。
//
// 设计约定：
//   - 所有路由挂在 /api 前缀下，与客户端转发入口 /v1 完全分开，
//     两个面用不同的密钥体系（/api 用管理密钥，/v1 用客户端令牌），
//     互不影响
//   - 整个 /api 分组强制鉴权（见 Register 的 adminAuth 参数，必填）：
//     管理接口能改渠道、看上游 Key，裸奔的代价是凭据池整体泄露
//   - 错误格式统一成 OpenAI 风格 {"error": {...}}，和 /v1 保持一致
package api

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"minigateway/internal/store"
)

// Handler 持有管理 API 需要的一切依赖。
//
// 为什么把 store 和回调函数都塞进来：渠道变更后必须同步刷新转发层的
// 渠道池，价格变更后必须让计费引擎失效价格缓存——否则会出现
// 「API 显示改成功了但转发/计费还用旧数据」的静默故障。
// 把回调作为依赖注入，api 包就不需要反向 import relay/billing
// （那会形成 server → api → relay → server 的环）。
type Handler struct {
	loginGate loginGate
	store     *store.Store
	// onChannelsChanged 在渠道发生任何写操作后调用，用于刷新 relay 渠道池。
	onChannelsChanged func()
	// onPricesChanged 在价格/倍率发生任何写操作后调用，用于让计费引擎
	// 重读价格表。nil 表示计费未启用。
	onPricesChanged func()
}

// New 创建管理 API handler。
func New(st *store.Store, onChannelsChanged, onPricesChanged func()) *Handler {
	return &Handler{store: st, onChannelsChanged: onChannelsChanged, onPricesChanged: onPricesChanged}
}

// Register 把管理路由挂到引擎上。
//
// 用 *gin.RouterGroup 而不是 gin.IRoutes：IRoutes 没有 Group 方法，
// 而这里需要建一个 /api 分组来统一挂中间件。
//
// adminAuth 从 P6 批次 1 起是**必填**。管理接口能改渠道、看 Key 明文，
// 漏挂鉴权的后果是「连上 localhost 就能拿走全部凭据」。所以这里传 nil
// 直接 panic：装配错误要在启动时炸出来，而不是留一个裸奔的管理面。
// （client token 的 /v1 路由不走这个分组，不受影响。）
func (h *Handler) Register(r *gin.RouterGroup, adminAuth gin.HandlerFunc) {
	if adminAuth == nil {
		panic("api.Register: adminAuth 不能为 nil，管理 API 必须鉴权")
	}
	group := r.Group("/api")
	group.Use(adminAuth)

	h.registerResources(group)
	// 令牌（增删查 + 单独的额度设置端点）
	group.GET("/tokens", h.ListTokens)
	group.POST("/tokens", h.CreateToken)
	group.PUT("/tokens/:id", h.UpdateToken)
	group.DELETE("/tokens/:id", h.DeleteToken)
	group.POST("/tokens/:id/quota", h.SetTokenQuota)

}

// fail 统一错误出口。
//
// store.ErrNotFound 映射成 404 而不是 500：调用方要知道的是「你要改的东西不存在」，
// 这是客户端问题，不是服务端故障。
func fail(c *gin.Context, err error) {
	if err == nil {
		return
	}
	if errors.Is(err, store.ErrNotFound) {
		c.JSON(http.StatusNotFound, gin.H{
			"error": gin.H{"message": err.Error(), "type": "not_found"},
		})
		return
	}
	c.JSON(http.StatusBadRequest, gin.H{
		"error": gin.H{"message": err.Error(), "type": "invalid_request"},
	})
}

func bindJSON(c *gin.Context, dst any) bool {
	if err := c.ShouldBindJSON(dst); err != nil {
		status := http.StatusBadRequest
		typ := "invalid_request"
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			status = http.StatusRequestEntityTooLarge
			typ = "request_too_large"
		}
		c.JSON(status, gin.H{"error": gin.H{"message": "请求体无效或超过大小限制: " + err.Error(), "type": typ}})
		return false
	}
	return true
}

func (h *Handler) registerResources(group *gin.RouterGroup) {
	// 渠道 CRUD
	group.GET("/channels", h.ListChannels)
	group.POST("/channels", h.CreateChannel)
	group.GET("/channels/:id", h.GetChannel)
	group.PUT("/channels/:id", h.UpdateChannel)
	group.DELETE("/channels/:id", h.DeleteChannel)

	// 凭据 CRUD（渠道下的上游 Key）
	// 路由分两种形态：挂在 /channels/:id 下的走「渠道视角」（列、增），
	// 凭据自身的改删状态走 /credentials/:id——凭据 ID 全局唯一，
	// 不需要再带一层渠道 ID
	group.GET("/channels/:id/credentials", h.ListCredentials)
	group.POST("/channels/:id/credentials", h.CreateCredential)
	group.PUT("/credentials/:id", h.UpdateCredential)
	group.DELETE("/credentials/:id", h.DeleteCredential)
	group.POST("/credentials/:id/status", h.SetCredentialStatus)

	// 日志查询
	group.GET("/logs", h.ListLogs)

	// 用量统计
	group.GET("/stats/summary", h.StatsSummary)
	group.GET("/stats/by-model", h.StatsByModel)
	group.GET("/stats/by-channel", h.StatsByChannel)

	// 价格表与分组倍率
	group.GET("/prices", h.ListPrices)
	group.PUT("/prices/:model", h.UpsertPrice)
	group.DELETE("/prices/:model", h.DeletePrice)
	group.PUT("/group-rates", h.SetGroupRates)

	// 能力表：路由加速器的可视化，排查「为什么这个模型路由不到某渠道」时看它
	group.GET("/abilities", h.ListAbilities)
}
