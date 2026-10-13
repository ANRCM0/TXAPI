package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

// 本文件覆盖管理 API 鉴权中间件的四条路径：无头、错密钥、正确密钥，
// 以及空密钥兜底（装配出错时拒绝一切请求而不是放行）。
//
// 为什么这些用例值得单独测：鉴权是安全边界，「忘了校验某个分支」
// 和「校验逻辑写反」都不会让 build 失败，只有测试能发现。
// ConstantTimeCompare 的时序特性本身无法在单测里断言（毫秒级
// 抖动远大于比较耗时），这里断言的是它的**功能**路径确实走了——
// 对的过、错的拦，且长度不等也不 panic。

const testAdminSecret = "sk-admin-secret-0123456789"

// newAdminEngine 搭一个只挂 AdminAuth 的最小引擎。
//
// 用真 gin 引擎而不是直接调中间件函数：中间件的行为依赖 gin.Context
// （GetHeader / AbortWithStatusJSON），绕过框架测等于没测。
func newAdminEngine(secret string) *gin.Engine {
	gin.SetMode(gin.TestMode)
	e := gin.New()
	e.GET("/api/channels", AdminAuth(secret), func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})
	return e
}

// doAdmin 发一次请求，返回状态码。headers 逐条塞进请求头。
func doAdmin(t *testing.T, e *gin.Engine, headers map[string]string) int {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/channels", nil)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	return rec.Code
}

// TestAdminAuthRejectsMissingKey 对应验收：无头 → 401。
func TestAdminAuthRejectsMissingKey(t *testing.T) {
	e := newAdminEngine(testAdminSecret)
	if code := doAdmin(t, e, nil); code != http.StatusUnauthorized {
		t.Fatalf("无密钥请求应 401, 实际 %d", code)
	}
	// 空字符串头等同于没带头（HTTP 头空值会被 curl/浏览器发出来），
	// 必须同样拒绝——否则攻击者只要发一个空头就能绕过
	if code := doAdmin(t, e, map[string]string{"X-Admin-Key": ""}); code != http.StatusUnauthorized {
		t.Fatalf("空 X-Admin-Key 应 401, 实际 %d", code)
	}
}

// TestAdminAuthRejectsWrongKey 对应验收：错密钥 → 401。
//
// 覆盖三种「错」的形态：完全不同的值、正确密钥的前缀、正确密钥的
// 超集。前缀/超集用例专门防「用 HasPrefix 之类的近似比较冒充
// 常量时间比较」这种实现错误——近似比较会让前缀永远通过。
func TestAdminAuthRejectsWrongKey(t *testing.T) {
	e := newAdminEngine(testAdminSecret)
	cases := map[string]string{
		"完全不同的值":  "sk-wrong-key-999",
		"正确密钥的前缀": testAdminSecret[:10],
		"正确密钥加后缀": testAdminSecret + "x",
	}
	for name, key := range cases {
		if code := doAdmin(t, e, map[string]string{"X-Admin-Key": key}); code != http.StatusUnauthorized {
			t.Fatalf("%s 应 401, 实际 %d", name, code)
		}
	}
}

// TestAdminAuthAcceptsCorrectKey 对应验收：正确密钥 → 200，
// 同时验证 Authorization: Bearer 的兼容写法。
func TestAdminAuthAcceptsCorrectKey(t *testing.T) {
	e := newAdminEngine(testAdminSecret)
	if code := doAdmin(t, e, map[string]string{"X-Admin-Key": testAdminSecret}); code != http.StatusOK {
		t.Fatalf("正确的 X-Admin-Key 应 200, 实际 %d", code)
	}
	if code := doAdmin(t, e, map[string]string{"Authorization": "Bearer " + testAdminSecret}); code != http.StatusOK {
		t.Fatalf("Bearer 写法应 200, 实际 %d", code)
	}
	// X-Admin-Key 优先于 Authorization：两个头同时存在且 Authorization
	// 里是错值时，不能因为 Bearer 写法的存在而被带偏
	if code := doAdmin(t, e, map[string]string{
		"X-Admin-Key":   testAdminSecret,
		"Authorization": "Bearer wrong",
	}); code != http.StatusOK {
		t.Fatalf("X-Admin-Key 优先, 应 200, 实际 %d", code)
	}
}

// TestAdminAuthRefusesWithoutSecret 验证空密钥兜底。
//
// 正常路径下 config 校验会在启动前拒绝空密钥，这个用例测的是
// 「万一装配漏了」的第二道防线：空密钥时一切请求 401，
// 绝不能退化成「没配密钥 = 不校验」。
func TestAdminAuthRefusesWithoutSecret(t *testing.T) {
	e := newAdminEngine("")
	if code := doAdmin(t, e, nil); code != http.StatusUnauthorized {
		t.Fatalf("空密钥 + 无头应 401, 实际 %d", code)
	}
	// 连「猜中空密钥」的请求也必须拒：空密钥本身就是非法状态
	if code := doAdmin(t, e, map[string]string{"X-Admin-Key": ""}); code != http.StatusUnauthorized {
		t.Fatalf("空密钥应拒绝一切请求, 实际 %d", code)
	}
}
