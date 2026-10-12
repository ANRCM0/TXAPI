package api

import (
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"minigateway/internal/store"
)

// 本文件是日志查询与用量统计的 API（SPEC 三交付物）。
//
// 从 token.go 拆出来：日志/统计和令牌 CRUD 是两个关注点，
// 混在一个文件里改一个动另一个，拆开后各自的演进互不打扰。

// parseUintQuery 解析一个可选的 uint query 参数。
// 空串返回 0（不过滤）；非法值回 400——query 写错了必须让调用方
// 知道，静默按 0 处理会把「查不到数据」伪装成「没有匹配的数据」。
func parseUintQuery(c *gin.Context, name string) (uint, bool) {
	raw := c.Query(name)
	if raw == "" {
		return 0, true
	}
	n, err := strconv.ParseUint(raw, 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": gin.H{"message": "非法的 " + name + ": " + raw, "type": "invalid_request"},
		})
		return 0, false
	}
	return uint(n), true
}

// ListLogs 处理 GET /api/logs?token_id=&channel_id=&model=&limit=&offset=
//
// 过滤参数全部用 query：GET 带 body 不符合语义，部分客户端和代理
// 会直接丢掉。limit 超过上限由 store 截断而不是报错——前端写错
// limit 不该让整个查询失败，返回 1000 条比返回一个错误有用得多。
func (h *Handler) ListLogs(c *gin.Context) {
	q := store.LogQuery{Model: c.Query("model")}
	if raw := c.Query("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 {
			c.JSON(http.StatusBadRequest, gin.H{
				"error": gin.H{"message": "limit 必须是正整数，收到: " + raw, "type": "invalid_request"},
			})
			return
		}
		q.Limit = n
	}
	if raw := c.Query("offset"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 0 {
			c.JSON(http.StatusBadRequest, gin.H{
				"error": gin.H{"message": "offset 必须是非负整数，收到: " + raw, "type": "invalid_request"},
			})
			return
		}
		q.Offset = n
	}
	channelID, ok := parseUintQuery(c, "channel_id")
	if !ok {
		return
	}
	q.ChannelID = channelID
	tokenID, ok := parseUintQuery(c, "token_id")
	if !ok {
		return
	}
	q.TokenID = tokenID

	logs, err := h.store.ListLogs(q)
	if err != nil {
		fail(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": logs, "total": len(logs)})
}

// parseRange 解析 range 参数并转成起始时间。非法值回 400。
//
// 默认 7d：大多数「最近用得怎么样」的问题关心的就是一周。
func parseRange(c *gin.Context) (time.Time, bool) {
	since, err := store.ParseSince(c.Query("range"), time.Now())
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": gin.H{"message": err.Error(), "type": "invalid_request"},
		})
		return time.Time{}, false
	}
	return since, true
}

// StatsSummary 处理 GET /api/stats/summary?range=7d，全局用量汇总。
func (h *Handler) StatsSummary(c *gin.Context) {
	since, ok := parseRange(c)
	if !ok {
		return
	}
	summary, err := h.store.LogSummary(since)
	if err != nil {
		fail(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": summary})
}

// StatsByModel 处理 GET /api/stats/by-model?range=7d，按模型统计。
//
// 回答「哪个模型最贵」——SPEC 一里「用量分析」的核心问题。
func (h *Handler) StatsByModel(c *gin.Context) {
	since, ok := parseRange(c)
	if !ok {
		return
	}
	rows, err := h.store.StatsByModel(since)
	if err != nil {
		fail(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": rows, "total": len(rows)})
}

// StatsByChannel 处理 GET /api/stats/by-channel?range=7d，按渠道统计。
//
// 回答「哪个渠道最贵/最慢/最常失败」——渠道调优的依据。
func (h *Handler) StatsByChannel(c *gin.Context) {
	since, ok := parseRange(c)
	if !ok {
		return
	}
	rows, err := h.store.StatsByChannel(since)
	if err != nil {
		fail(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": rows, "total": len(rows)})
}
