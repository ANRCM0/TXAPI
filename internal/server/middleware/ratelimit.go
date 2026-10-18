package middleware

import (
	"bytes"
	"fmt"
	"github.com/gin-gonic/gin"
	"io"
	"log/slog"
	admission "minigateway/internal/foundation/contracts/admission/v1"
	textvalue "minigateway/internal/foundation/contracts/text/v1"
	"minigateway/internal/protocol"
	"net/http"
	"strconv"
	"time"
)

type Limiter = admission.Limiter

const ctxTPMEstimate = "tpm_estimate"
const estimateBodyLimit = 64 * 1024

func TokenRateLimit(gate admission.RequestGate) gin.HandlerFunc {
	return func(c *gin.Context) {
		if gate == nil {
			c.Next()
			return
		}
		ctx, decision := gate.Admit(c.Request.Context(), func() int { return estimatePromptTokens(c) })
		if decision.Rejected {
			abortRateLimited(c, decision.RetryAfterSeconds, decision.Message)
			return
		}
		c.Request = c.Request.WithContext(ctx)
		if decision.HasEstimate {
			c.Set(ctxTPMEstimate, decision.Estimate)
		}
		c.Next()
	}
}
func IPRateLimit(limiter Limiter, limit int) gin.HandlerFunc {
	return func(c *gin.Context) {
		if limiter == nil || limit <= 0 {
			c.Next()
			return
		}
		allowed, retry := limiter.AllowRequest(c.Request.Context(), "ip:"+c.ClientIP(), limit, time.Now())
		if !allowed {
			abortRateLimited(c, retry, fmt.Sprintf("IP %s 每分钟请求数超过上限 %d", c.ClientIP(), limit))
			return
		}
		c.Next()
	}
}

type replayedBody struct {
	io.Reader
	io.Closer
}

func estimatePromptTokens(c *gin.Context) int {
	if c.Request.Body == nil {
		return 0
	}
	original := c.Request.Body
	body, err := io.ReadAll(io.LimitReader(original, estimateBodyLimit))
	c.Request.Body = &replayedBody{Reader: io.MultiReader(bytes.NewReader(body), original), Closer: original}
	if err != nil {
		return 0
	}
	return textvalue.EstimateTokens(protocol.PromptTextFromBody(body))
}
func abortRateLimited(c *gin.Context, retryAfterSec int, msg string) {
	if retryAfterSec < 1 {
		retryAfterSec = 1
	}
	c.Header("Retry-After", strconv.Itoa(retryAfterSec))
	c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{
		"error": gin.H{"message": msg, "type": "rate_limit_error"},
	})
	// warn 而不是 info：限流意味着有客户端在超配额使用，属于
	// 需要人看一眼的事件；但它不是故障（网关在正常工作），
	// 所以不到 error。
	slog.Warn("[限流] 请求被限流",
		"method", c.Request.Method, "path", c.Request.URL.Path,
		"request_id", c.GetString(CtxRequestID), "reason", msg)
}
