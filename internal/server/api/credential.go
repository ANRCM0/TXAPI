package api

import (
	"net/http"

	"github.com/gin-gonic/gin"

	cfg "minigateway/internal/foundation/contracts/configuration/v1"
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
	creds := h.configuration.ListCredentials(id)
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
	var in cfg.CredentialInput
	if !bindJSON(c, &in) {
		return
	}
	cred, change, err := h.configuration.CreateCredential(id, &in)
	if !configurationResult(c, change, err) {
		return
	}
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
	var in cfg.CredentialInput
	if !bindJSON(c, &in) {
		return
	}
	cred, change, err := h.configuration.UpdateCredential(id, &in)
	if !configurationResult(c, change, err) {
		return
	}
	cred.APIKey = maskKey(cred.APIKey)
	c.JSON(http.StatusOK, gin.H{"data": cred})
}

// DeleteCredential 处理 DELETE /api/credentials/:id。
func (h *Handler) DeleteCredential(c *gin.Context) {
	id, ok := parseID(c, "id")
	if !ok {
		return
	}
	change, err := h.configuration.DeleteCredential(id)
	if !configurationResult(c, change, err) {
		return
	}
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
	cred, change, err := h.configuration.SetCredentialStatus(id, in.Status)
	if !configurationResult(c, change, err) {
		return
	}
	cred.APIKey = maskKey(cred.APIKey)
	c.JSON(http.StatusOK, gin.H{"data": cred})
}
