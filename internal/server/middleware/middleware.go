// Package middleware 存放 HTTP 中间件。
package middleware

import (
	"crypto/rand"
	"encoding/hex"
	"log"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"minigateway/internal/config"
)

// 上下文 key。用常量而不是裸字符串，避免拼错导致取值拿到 nil。
const (
	CtxTokenName   = "token_name"
	CtxAllowedModel = "allowed_models"
	CtxRequestID   = "request_id"
)

// Recovery 捕获 panic，避免一个请求的异常打挂整个网关进程。
//
// 网关是长驻服务，一旦进程退出，所有在途请求都会失败。
// 所以必须把 panic 限制在单个请求内。
func Recovery() gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			if err := recover(); err != nil {
				log.Printf("[panic] %s %s: %v", c.Request.Method, c.Request.URL.Path, err)
				c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{
					"error": gin.H{
						"message": "网关内部错误，请稍后重试",
						"type":    "internal_error",
					},
				})
			}
		}()
		c.Next()
	}
}

// RequestID 给每个请求生成唯一 ID，写进响应头。
//
// 作用：用户报障时给出这个 ID，就能在日志里捞出完整链路。
// 这是可观测性最便宜也最有效的一步。
func RequestID() gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.GetHeader("X-Request-Id")
		if id == "" {
			buf := make([]byte, 8)
			if _, err := rand.Read(buf); err != nil {
				id = "req-unknown"
			} else {
				id = hex.EncodeToString(buf)
			}
		}
		c.Set(CtxRequestID, id)
		c.Writer.Header().Set("X-Request-Id", id)
		c.Next()
	}
}

// CORS 允许浏览器直接调用网关。
//
// 网关常被前端页面直连，没有这个头浏览器会拦掉请求。
func CORS() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Writer.Header().Set("Access-Control-Allow-Origin", "*")
		c.Writer.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		c.Writer.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, X-Request-Id")
		if c.Request.Method == http.MethodOptions {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}
		c.Next()
	}
}

// Auth 校验客户端令牌。
//
// 令牌是「客户端访问网关」的凭证，和「网关访问上游」的渠道 key 是两回事。
// 令牌表直接放内存：读多写极少，map 查找是 O(1)，
// 不需要为了这个引入数据库查询。
type Auth struct {
	tokens map[string]tokenInfo
}

type tokenInfo struct {
	name   string
	models []string
}

// NewAuth 用配置里的令牌列表构建鉴权器。
func NewAuth(tokens []config.TokenConfig) *Auth {
	m := make(map[string]tokenInfo, len(tokens))
	for _, t := range tokens {
		m[t.Key] = tokenInfo{name: t.Name, models: t.Models}
	}
	return &Auth{tokens: m}
}

// Handle 返回鉴权中间件。
func (a *Auth) Handle() gin.HandlerFunc {
	return func(c *gin.Context) {
		key := extractToken(c)
		if key == "" {
			abortUnauthorized(c, "缺少 API Key。请在 Authorization 头里带上 Bearer <key>")
			return
		}
		info, ok := a.tokens[key]
		if !ok {
			abortUnauthorized(c, "无效的 API Key")
			return
		}
		// 把令牌信息放进上下文，后续 handler 用来做模型权限校验和日志
		c.Set(CtxTokenName, info.name)
		c.Set(CtxAllowedModel, info.models)
		c.Next()
	}
}

// extractToken 从 Authorization 头里取出 key。
// 兼容 "Bearer xxx" 和直接给 "xxx" 两种写法。
func extractToken(c *gin.Context) string {
	h := c.GetHeader("Authorization")
	if h == "" {
		return ""
	}
	if strings.HasPrefix(strings.ToLower(h), "bearer ") {
		return strings.TrimSpace(h[7:])
	}
	return strings.TrimSpace(h)
}

func abortUnauthorized(c *gin.Context, msg string) {
	c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
		"error": gin.H{"message": msg, "type": "invalid_request_error"},
	})
}
