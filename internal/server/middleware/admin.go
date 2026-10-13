package middleware

import (
	"crypto/subtle"
	"strings"

	"github.com/gin-gonic/gin"
)

// 本文件是管理 API（/api/*）的鉴权中间件。
//
// 与 Auth（客户端令牌）的分工：Auth 校验「这个令牌能用哪些模型」，
// AdminAuth 校验「你是不是这台网关的管理员」。管理接口能改渠道、
// 看上游 Key 明文，泄露的代价远大于转发接口，所以它有自己的密钥，
// 不复用客户端令牌体系。

// AdminAuth 用固定密钥保护管理 API。
//
// 密钥来自配置（环境变量 GATEWAY_ADMIN_KEY 优先，其次 config.yaml 的
// admin_key），由调用方在启动时解析好传进来——中间件自己不读环境变量，
// 保持「一个中间件只干一件事」，也方便测试时注入任意密钥。
func AdminAuth(secret string) gin.HandlerFunc {
	return func(c *gin.Context) {
		// 空密钥说明装配出错：正常路径下 config 校验已经拒绝启动，
		// 这里是最后一道兜底。此时必须拒绝一切请求而不是放行——
		// 「没配密钥所以谁都能进」正是这个中间件要消灭的事故形态。
		if secret == "" {
			abortUnauthorized(c, "管理密钥未配置，拒绝一切管理请求")
			return
		}
		key := extractAdminKey(c)
		if key == "" {
			abortUnauthorized(c, "缺少管理密钥。请在 X-Admin-Key 头或 Authorization: Bearer <key> 里带上")
			return
		}
		// 用常量时间比较防时序攻击：普通 == 会在第一个不同字节就返回，
		// 攻击者能据此逐字节猜出密钥。ConstantTimeCompare 长度不等时
		// 直接返回 0，这不会泄露内容（响应一律 401，与对错无关）。
		if subtle.ConstantTimeCompare([]byte(key), []byte(secret)) != 1 {
			abortUnauthorized(c, "管理密钥错误")
			return
		}
		c.Next()
	}
}

// extractAdminKey 取管理密钥：优先 X-Admin-Key 头，
// 没有时回落到 Authorization（兼容 curl -H 'Authorization: Bearer <key>'
// 的写法，和 /v1 的令牌鉴权保持一致的手感）。
func extractAdminKey(c *gin.Context) string {
	if k := strings.TrimSpace(c.GetHeader("X-Admin-Key")); k != "" {
		return k
	}
	return extractToken(c)
}
