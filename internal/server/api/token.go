package api

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"minigateway/internal/store"
)

// ListTokens 处理 GET /api/tokens。
func (h *Handler) ListTokens(c *gin.Context) {
	tokens := h.store.ListTokens()
	for i := range tokens {
		// 与渠道列表同理：key 打码，只留头尾辨认
		tokens[i].Key = maskKey(tokens[i].Key)
	}
	c.JSON(http.StatusOK, gin.H{"data": tokens, "total": len(tokens)})
}

// CreateToken 处理 POST /api/tokens。
func (h *Handler) CreateToken(c *gin.Context) {
	var in store.TokenInput
	if err := c.ShouldBindJSON(&in); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": gin.H{"message": "请求体不是合法 JSON: " + err.Error(), "type": "invalid_request"},
		})
		return
	}
	tk, err := h.store.CreateToken(&in)
	if err != nil {
		fail(c, err)
		return
	}
	// 创建响应**不**打码：这把 key 是调用方第一次也是唯一一次看到完整值的机会
	// （列表接口只给打码值），打码在这里等于让用户没法用自己刚创建的令牌
	c.JSON(http.StatusCreated, gin.H{"data": tk})
}

// UpdateToken 处理 PUT /api/tokens/:id。
func (h *Handler) UpdateToken(c *gin.Context) {
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	var in store.TokenInput
	if err := c.ShouldBindJSON(&in); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": gin.H{"message": "请求体不是合法 JSON: " + err.Error(), "type": "invalid_request"},
		})
		return
	}
	tk, err := h.store.UpdateToken(id, &in)
	if err != nil {
		fail(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": tk})
}

// DeleteToken 处理 DELETE /api/tokens/:id（吊销令牌）。
func (h *Handler) DeleteToken(c *gin.Context) {
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	if err := h.store.DeleteToken(id); err != nil {
		fail(c, err)
		return
	}
	// 令牌不需要 notifyChanged：鉴权中间件直接查 store 的内存缓存，
	// store 的写操作已经刷过缓存，下一次请求自动生效
	c.JSON(http.StatusOK, gin.H{"deleted": true, "id": id})
}

// ListAbilities 处理 GET /api/abilities，暴露路由加速表。
func (h *Handler) ListAbilities(c *gin.Context) {
	rows, err := h.store.Abilities()
	if err != nil {
		fail(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": rows, "total": len(rows)})
}
