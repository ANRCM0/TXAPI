package api

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"minigateway/internal/store"
)

// ListChannels 处理 GET /api/channels。
func (h *Handler) ListChannels(c *gin.Context) {
	channels := h.store.ListChannels()
	// 不在列表接口里回显凭据 key：列表是一次性拉全量数据的，
	// 把每个渠道的 key 都吐出来等于把整个网关的凭据池明文打印。
	// 需要看 key 的场合走 GET /api/channels/:id，并且后续阶段会加管理员鉴权。
	for i := range channels {
		for j := range channels[i].Credentials {
			channels[i].Credentials[j].APIKey = maskKey(channels[i].Credentials[j].APIKey)
		}
	}
	c.JSON(http.StatusOK, gin.H{"data": channels, "total": len(channels)})
}

// maskKey 把 key 中间打码，只留前 3 位和后 4 位（形如 sk-...abcd）。
//
// 为什么要打码：日志、列表接口、截图排查时 key 经常被无意带出去。
// 留头尾足够人工辨认是哪一个 key，又不足以完整泄露——Key 的核心
// 熵在中段，截掉中段后即使拿到掩码值也拼不回真身。
//
// 短 key（≤8 字符）整体打码：前后一共只留 7 位的话，短 key 的
// 掩码版几乎等于原文，打码失去意义。
func maskKey(key string) string {
	if len(key) <= 8 {
		return "****"
	}
	return key[:3] + "..." + key[len(key)-4:]
}

// GetChannel 处理 GET /api/channels/:id。
//
// api_key 始终返回掩码；已有凭据不提供明文回显接口。
func (h *Handler) GetChannel(c *gin.Context) {
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	ch, err := h.store.GetChannel(id)
	if err != nil {
		fail(c, err)
		return
	}
	for i := range ch.Credentials {
		ch.Credentials[i].APIKey = maskKey(ch.Credentials[i].APIKey)
	}
	c.JSON(http.StatusOK, gin.H{"data": ch})
}

// CreateChannel 处理 POST /api/channels。
func (h *Handler) CreateChannel(c *gin.Context) {
	var in store.ChannelInput
	if !bindJSON(c, &in) {
		return
	}
	ch, err := h.store.CreateChannel(&in)
	if err != nil {
		fail(c, err)
		return
	}
	// 建渠道的同时刷新转发层渠道池——这一步就是「不重启就生效」的实现处
	h.notifyChanged()
	for i := range ch.Credentials {
		ch.Credentials[i].APIKey = maskKey(ch.Credentials[i].APIKey)
	}
	c.JSON(http.StatusCreated, gin.H{"data": ch})
}

// UpdateChannel 处理 PUT /api/channels/:id。
func (h *Handler) UpdateChannel(c *gin.Context) {
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	var in store.ChannelInput
	if !bindJSON(c, &in) {
		return
	}
	ch, err := h.store.UpdateChannel(id, &in)
	if err != nil {
		fail(c, err)
		return
	}
	// 渠道改了 models/status/priority 都会影响路由，必须热刷新
	h.notifyChanged()
	for i := range ch.Credentials {
		ch.Credentials[i].APIKey = maskKey(ch.Credentials[i].APIKey)
	}
	c.JSON(http.StatusOK, gin.H{"data": ch})
}

// DeleteChannel 处理 DELETE /api/channels/:id。
func (h *Handler) DeleteChannel(c *gin.Context) {
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	if err := h.store.DeleteChannel(id); err != nil {
		fail(c, err)
		return
	}
	h.notifyChanged()
	c.JSON(http.StatusOK, gin.H{"deleted": true, "id": id})
}

// notifyChanged 触发转发层渠道池刷新。
//
// 判空是为了让 api.Handler 在单元测试里可以不带 reload 依赖直接用。
func (h *Handler) notifyChanged() {
	if h.onChannelsChanged != nil {
		h.onChannelsChanged()
	}
}

// notifyPricesChanged 让计费引擎重读价格表和分组倍率。
//
// 判空同理：计费未启用（billingEngine 为 nil）时管理接口照常工作。
func (h *Handler) notifyPricesChanged() {
	if h.onPricesChanged != nil {
		h.onPricesChanged()
	}
}

// parseID 从路径参数解析 uint ID。
//
// 单独抽出来是因为每个 handler 都要做同一件事，抄五遍容易漏掉错误分支。
func parseID(c *gin.Context, name string) (uint, bool) {
	raw := c.Param(name)
	n, err := strconv.ParseUint(raw, 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": gin.H{"message": "非法的 ID: " + raw, "type": "invalid_request"},
		})
		return 0, false
	}
	return uint(n), true
}
