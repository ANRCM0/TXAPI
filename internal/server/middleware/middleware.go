// Package middleware 存放 HTTP 中间件。
package middleware

import (
	"crypto/rand"
	"encoding/hex"
	"log"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"minigateway/internal/model"
)

// 上下文 key。用常量而不是裸字符串，避免拼错导致取值拿到 nil。
const (
	CtxTokenName    = "token_name"
	CtxAllowedModel = "allowed_models"
	CtxRequestID    = "request_id"
	// CtxTokenID 是令牌在数据库里的 ID，写日志时要带上它，
	// 否则「哪个令牌打的」只能靠 key 反查
	CtxTokenID = "token_id"
	// CtxUserID 是令牌所属用户的 ID。P5 的额度是双层结构
	// （先扣令牌、再扣用户），结算时要扣用户额度；
	// 日志的 user_id 字段也用它填。
	CtxUserID = "user_id"
	// CtxTokenView 是完整的令牌投影（含限流阈值）。
	// 单独存一份而不是让下游拼：限流中间件要按令牌 ID 分桶、
	// 读 RPM/TPM 阈值，逐个取 key 会漏字段（加一个字段就要记得
	// 在这里再加一行）。投影是「鉴权结果的完整表达」，
	// 一个 key 存一个结构体，以后加字段不用动这里。
	CtxTokenView = "token_view"
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
		c.Writer.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		c.Writer.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, X-Request-Id")
		if c.Request.Method == http.MethodOptions {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}
		c.Next()
	}
}

// TokenLookup 是令牌查询能力的最小接口。
//
// 为什么用接口而不是直接传 *store.Store：middleware 是 HTTP 层的基础设施，
// 不该依赖存储层的具体类型。这样以后令牌挪到 Redis 或多级缓存，
// 只要实现同样的方法签名，中间件一行不改。
//
// 实现方（store.Store）负责过滤掉「已吊销/已过期」的令牌，
// 中间件只关心「查得到就是合法的」。
type TokenLookup interface {
	LookupToken(key string) (model.TokenView, bool)
}

// Auth 校验客户端令牌。
//
// 令牌是「客户端访问网关」的凭证，和「网关访问上游」的渠道 key 是两回事。
// P2 起令牌来自数据库（经 store 的内存缓存），map 查找仍是 O(1)，
// 不产生任何 SQLite 读——管理 API 改了令牌，下一次请求就能看到。
type Auth struct {
	lookup TokenLookup
}

// NewAuth 用令牌查询源构建鉴权器。
func NewAuth(lookup TokenLookup) *Auth {
	return &Auth{lookup: lookup}
}

// Handle 返回鉴权中间件。
func (a *Auth) Handle() gin.HandlerFunc {
	return func(c *gin.Context) {
		key := extractToken(c)
		if key == "" {
			abortUnauthorized(c, "缺少 API Key。请在 Authorization 头里带上 Bearer <key>")
			return
		}
		view, ok := a.lookup.LookupToken(key)
		if !ok {
			// 不区分「key 不存在」和「key 已过期」：把区别说出去等于
			// 告诉攻击者哪些 key 是真实存在的
			abortUnauthorized(c, "无效的 API Key")
			return
		}
		// 把令牌信息放进上下文，后续 handler 用来做模型权限校验、
		// 额度扣减、限流分桶和日志
		c.Set(CtxTokenName, view.Name)
		c.Set(CtxAllowedModel, view.Models)
		c.Set(CtxTokenID, view.ID)
		c.Set(CtxUserID, view.UserID)
		// 整个投影也存一份：限流中间件要按令牌 ID 分桶并读 RPM/TPM
		// 阈值，只给它散落的字段会让「加一个令牌字段」变成三处改动。
		c.Set(CtxTokenView, view)
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
