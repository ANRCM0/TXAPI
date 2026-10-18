package api

import (
	"net/http"

	"github.com/gin-gonic/gin"

	cfg "minigateway/internal/foundation/contracts/configuration/v1"
)

// 本文件是价格表 / 分组倍率 / 令牌额度的管理 API（SPEC 三交付物）。
//
// 和 relay 的计费引擎的联动：任何写操作成功后都要通知引擎失效价格
// 缓存（由 configuration 用例协调），否则会出现「API 显示改成功
// 了、计费还用旧价格」的静默故障。

// ListPrices 处理 GET /api/prices，返回价格表全量。
//
// 价格表是个位到百行的小表，全量返回即可，不做分页。
func (h *Handler) ListPrices(c *gin.Context) {
	prices, rates, err := h.configuration.Prices()
	if err != nil {
		fail(c, err)
		return
	}
	// 倍率和价格一起返回：它们共同决定一笔费用，拆成两个接口
	// 会让前端为了展示一行价格多发一次请求
	c.JSON(http.StatusOK, gin.H{
		"data":        prices,
		"total":       len(prices),
		"group_rates": rates,
	})
}

// UpsertPrice 处理 PUT /api/prices/:model，写入或更新一个模型的单价。
//
// 用 PUT 而不是 POST：语义是「把这个模型的价格设成这个值」，
// 重复执行结果相同（幂等），调用方不需要关心条目是否已存在。
func (h *Handler) UpsertPrice(c *gin.Context) {
	modelName := c.Param("model")
	if modelName == "" {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": gin.H{"message": "缺少模型名", "type": "invalid_request"},
		})
		return
	}
	var in cfg.PriceInput
	if !bindJSON(c, &in) {
		return
	}
	price, change, err := h.configuration.UpsertPrice(modelName, &in)
	if !configurationResult(c, change, err) {
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": price})
}

// DeletePrice 处理 DELETE /api/prices/:model。
//
// 删除后该模型按 0 计费并打 price_missing 标记（不是报错）：
// SPEC 2.2 的选择是「宁可漏计费，不要因为价格表缺一项就拒绝服务」。
func (h *Handler) DeletePrice(c *gin.Context) {
	modelName := c.Param("model")
	change, err := h.configuration.DeletePrice(modelName)
	if !configurationResult(c, change, err) {
		return
	}
	c.JSON(http.StatusOK, gin.H{"deleted": true, "model": modelName})
}

// SetGroupRates 处理 PUT /api/group-rates，整体替换分组倍率表。
//
// body 形如 {"default": 1.0, "relay": 0.8}。
func (h *Handler) SetGroupRates(c *gin.Context) {
	var rates map[string]float64
	if !bindJSON(c, &rates) {
		return
	}
	change, err := h.configuration.SetGroupRates(rates)
	if !configurationResult(c, change, err) {
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": rates})
}

// SetTokenQuota 处理 POST /api/tokens/:id/quota，设置令牌总额度。
//
// body: {"quota": 1.0}。只动 quota 不动 used_quota——「加额度」和
// 「重置用量」是两个操作，后者需要单独的 reset 语义，不能顺手
// 塞进同一个端点。
func (h *Handler) SetTokenQuota(c *gin.Context) {
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	var in struct {
		Quota float64 `json:"quota"`
	}
	if !bindJSON(c, &in) {
		return
	}
	tk, err := h.store.SetTokenQuota(id, in.Quota)
	if err != nil {
		fail(c, err)
		return
	}
	tk.Key = ""
	c.JSON(http.StatusOK, gin.H{"data": tk})
}
