// Package api 提供管理 REST API：渠道 / 令牌 / 日志 / 统计 / 价格的 CRUD。
//
// 设计约定：
//   - 所有路由挂在 /api 前缀下，与客户端转发入口 /v1 完全分开，
//     这样将来给 /api 加管理员鉴权时不会影响转发链路
//   - 目前不做鉴权（PLAN 的 P2 阶段明确「先做无鉴权或简单鉴权」），
//     但挂载点已经留好：见 Register 的 adminAuth 参数，P3 传入真正的中间件即可
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
	store *store.Store
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
// adminAuth 是管理员鉴权中间件的挂载点。传 nil 表示暂不鉴权（P2 现状）；
// P3 要加鉴权时，在这里传一个 gin.HandlerFunc 即可，路由代码不用动。
func (h *Handler) Register(r *gin.RouterGroup, adminAuth gin.HandlerFunc) {
	group := r.Group("/api")
	if adminAuth != nil {
		group.Use(adminAuth)
	}

	// 渠道 CRUD
	group.GET("/channels", h.ListChannels)
	group.POST("/channels", h.CreateChannel)
	group.GET("/channels/:id", h.GetChannel)
	group.PUT("/channels/:id", h.UpdateChannel)
	group.DELETE("/channels/:id", h.DeleteChannel)

	// 令牌（增删查 + 单独的额度设置端点）
	group.GET("/tokens", h.ListTokens)
	group.POST("/tokens", h.CreateToken)
	group.PUT("/tokens/:id", h.UpdateToken)
	group.DELETE("/tokens/:id", h.DeleteToken)
	group.POST("/tokens/:id/quota", h.SetTokenQuota)

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
