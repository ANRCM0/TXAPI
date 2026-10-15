package api

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"minigateway/internal/store"
)

// 本文件是凭据（渠道下的上游 Key）的管理 API。
//
// 与渠道 CRUD 的关系：渠道回答「上游在哪」，凭据回答「用哪把 Key」。
// 一个渠道下挂多把 Key 是凭据池调度的前提（PLAN 2.1），P3 的加权随机、
// 失败冷却都建立在这批接口之上。

// ListCredentials 处理 GET /api/channels/:id/credentials。
func (h *Handler) ListCredentials(c *gin.Context) {
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	creds := h.store.ListCredentials(id)
	// 列表一律打码：列表接口常被整体拉取、截图、贴进 issue，
	// 把每把 Key 都吐出来等于把整个凭据池明文打印出去。
	// 已有凭据的明文不通过列表接口回显。
	for i := range creds {
		creds[i].APIKey = maskKey(creds[i].APIKey)
	}
	c.JSON(http.StatusOK, gin.H{"data": creds, "total": len(creds)})
}

// CreateCredential 处理 POST /api/channels/:id/credentials。
//
// body: {"name":"backup","api_key":"sk-...","weight":5}
// 三个字段都可选，api_key 除外——没有 Key 的凭据没有存在意义。
func (h *Handler) CreateCredential(c *gin.Context) {
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	var in store.CredentialInput
	if !bindJSON(c, &in) {
		return
	}
	cred, err := h.store.CreateCredential(id, &in)
	if err != nil {
		h.failConfiguration(c, err)
		return
	}
	// 凭据进池子就是转发层的事：必须刷渠道池，否则新 Key 要等下次
	// 渠道变更才生效——「加了 Key 但流量不打过去」是最容易误判成
	// 「Key 无效」的故障
	h.notifyChanged(c)
	// 创建响应**不**打码：这是调用方唯一一次能拿到完整 Key 的机会，
	// 和 CreateToken 同理。打码在这里等于让用户没法用刚建的凭据。
	c.JSON(http.StatusCreated, gin.H{"data": cred})
}

// UpdateCredential 处理 PUT /api/credentials/:id，更新凭据的
// name / api_key / weight。
//
// 为什么不开放改 status：状态由调度器驱动（P3），管理接口顺手改会
// 和调度器的拉黑/冷却逻辑打架。人工干预走单独的 status 端点。
func (h *Handler) UpdateCredential(c *gin.Context) {
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	var in store.CredentialInput
	if !bindJSON(c, &in) {
		return
	}
	cred, err := h.store.UpdateCredential(id, &in)
	if err != nil {
		h.failConfiguration(c, err)
		return
	}
	// 换了 Key 就必须刷池子：转发层持有的是缓存里的 Key 快照，
	// 不刷的话旧 Key 会一直用到下一次无关的渠道变更
	h.notifyChanged(c)
	cred.APIKey = maskKey(cred.APIKey)
	c.JSON(http.StatusOK, gin.H{"data": cred})
}

// DeleteCredential 处理 DELETE /api/credentials/:id。
func (h *Handler) DeleteCredential(c *gin.Context) {
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	if err := h.store.DeleteCredential(id); err != nil {
		h.failConfiguration(c, err)
		return
	}
	h.notifyChanged(c)
	c.JSON(http.StatusOK, gin.H{"deleted": true, "id": id})
}

// SetCredentialStatus 处理 POST /api/credentials/:id/status。
//
// body: {"status":"active"}
// 用于「手动重置为 active」和「解除拉黑」——调度器自动拉黑的 Key
// 不该永远回不来，运维确认 Key 修好了之后靠这个端点放行。
// 设回 active 会连带清掉 cooldown_until 和 fail_count（见 store 层）。
func (h *Handler) SetCredentialStatus(c *gin.Context) {
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	var in struct {
		Status string `json:"status"`
	}
	if !bindJSON(c, &in) {
		return
	}
	cred, err := h.store.SetCredentialStatus(id, in.Status)
	if err != nil {
		h.failConfiguration(c, err)
		return
	}
	h.notifyChanged(c)
	cred.APIKey = maskKey(cred.APIKey)
	c.JSON(http.StatusOK, gin.H{"data": cred})
}
