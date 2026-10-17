package gateway

import (
	"context"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"minigateway/internal/protocol"
	"minigateway/internal/protocol/meta"
	"minigateway/internal/protocol/model"
)

func (g *Gateway) HandleChatCompletions(c *gin.Context) { g.handleRelay(c, meta.ModeChatCompletions) }
func (g *Gateway) HandleEmbeddings(c *gin.Context)      { g.handleRelay(c, meta.ModeEmbeddings) }

func (g *Gateway) handleRelay(c *gin.Context, mode int) {
	result := state(c)
	scope := newExecutionScope(c.Request.Context(), c.Writer, result, c.GetHeader(SessionIDHeader))
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		var oversized *http.MaxBytesError
		if errors.As(err, &oversized) {
			writeError(scope, 413, "request body exceeds the size limit", "request_too_large")
		} else {
			writeError(scope, 400, "could not read request body", "invalid_request_error")
		}
		return
	}
	operation := protocol.Chat
	if mode == meta.ModeEmbeddings {
		operation = protocol.Embeddings
	}
	g.execute(scope, Execution{Operation: operation, Body: body})
}

// Relay is retained for existing in-package integrations. New consumers use Execute.
func (g *Gateway) Relay(c *gin.Context, req *model.ModelRequest, body []byte, allowed []string, mode int, timeout time.Duration) {
	scope := newExecutionScope(c.Request.Context(), c.Writer, state(c), c.GetHeader(SessionIDHeader))
	g.relay(scope, req, body, allowed, mode, timeout)
}

func state(c *gin.Context) *RequestResult {
	if c.Request == nil {
		c.Request = &http.Request{Header: make(http.Header)}
	}
	if result := ResultFromContext(c.Request.Context()); result != nil {
		return result
	}
	result := newRequestResult(c.Request.Context())
	c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), resultContextKey{}, result))
	return result
}

func relayStatus(c *gin.Context) int { return state(c).StatusCode }

// ListModels 处理 GET /v1/models，返回该令牌可访问的模型清单。
//
// 客户端（Cherry Studio、NextChat 这类）靠这个接口自动填充模型下拉框。
func (g *Gateway) ListModels(c *gin.Context) {
	allowedModels := state(c).Identity.AllowedModels

	seen := map[string]bool{}
	data := make([]gin.H, 0)

	for _, ch := range g.reg.allChannels() {
		models := append([]string(nil), ch.models...)
		if ch.supports("*") && state(c).Identity.Policy != nil {
			models = append(models, state(c).Identity.Policy.Models...)
		}
		for _, m := range models {
			if m == "*" {
				continue
			}
			if seen[m] || !modelAllowed(m, allowedModels) || (state(c).Identity.Policy != nil && !state(c).Identity.Policy.Allows(m)) {
				continue
			}
			seen[m] = true
			data = append(data, gin.H{
				"id":       m,
				"object":   "model",
				"owned_by": ch.name,
			})
		}
	}

	c.JSON(http.StatusOK, gin.H{"object": "list", "data": data})
}
