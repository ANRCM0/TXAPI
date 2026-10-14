package server

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"minigateway/internal/config"
	"minigateway/internal/gateway"
	"minigateway/internal/server/middleware"
	"minigateway/internal/store"
)

// 本文件验证 NewEngine 的装配约定（P6 批次 1 引入的两条）：
//  1. AdminAuth 必填——nil 直接 panic，不留裸奔的管理面
//  2. 鉴权只作用在 /api 分组——/healthz 和 /v1 转发入口不受影响
//
// 为什么值得单独测：api 包的测试测的是「分组挂了中间件」，
// 这里的测试测的是「server 把中间件接对了地方」。两层接错任何
// 一层，结果都是 /api 裸奔，但 build 和 vet 都看不出来。

const testSecret = "sk-server-test-0123456789"

// newEngine 用真 store 搭一个完整引擎。
//
// relay 的初始化不在这里做：本测试只碰 /healthz 和 /api，
// 不产生任何转发请求，不需要渠道池。
func newEngine(t *testing.T, adminAuth gin.HandlerFunc) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)

	st, err := store.Open(config.DatabaseConfig{
		Driver: "sqlite",
		Path:   filepath.Join(t.TempDir(), "server.db"),
	})
	if err != nil {
		t.Fatalf("开库失败: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	gw, err := gateway.New(gateway.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(gw.Close)
	return NewEngine(Deps{
		Gateway:   gw,
		Cfg:       &config.Config{Server: config.ServerConfig{Addr: "127.0.0.1:0"}},
		Store:     st,
		AdminAuth: adminAuth,
	})
}

// TestNewEnginePanicsWithoutAdminAuth 验证漏传鉴权中间件会硬失败。
//
// 用 recover 而不是让测试 panic：panic 是这里的**预期行为**，
// 断言的是「确实 panic 且消息说清了原因」，而不是让进程挂掉。
func TestNewEnginePanicsWithoutAdminAuth(t *testing.T) {
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("AdminAuth 为 nil 时应 panic")
		}
		if msg, ok := r.(string); !ok || msg == "" {
			t.Fatalf("panic 消息应说明原因, 实际 %v", r)
		}
	}()
	newEngine(t, nil)
	t.Fatal("不应执行到这里")
}

// TestHealthzAndAdminAuthSplit 验证鉴权的边界：/healthz 免鉴权，
// /api 全分组鉴权。
//
// /healthz 必须免鉴权：Docker healthcheck 和负载均衡探活不会带
// 管理密钥，挡在这里会让健康检查一直失败，进程被反复重启——
// 一个「为了安全」的设计把服务搞成不可用，比不安全更糟。
func TestHealthzAndAdminAuthSplit(t *testing.T) {
	e := newEngine(t, middleware.AdminAuth(testSecret))

	do := func(path string, withKey bool) int {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		if withKey {
			req.Header.Set("X-Admin-Key", testSecret)
		}
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		return rec.Code
	}

	// /healthz：免鉴权
	if code := do("/healthz", false); code != http.StatusOK {
		t.Fatalf("/healthz 应免鉴权 200, 实际 %d", code)
	}
	// /api：无头 401、带头 200
	if code := do("/api/channels", false); code != http.StatusUnauthorized {
		t.Fatalf("/api 无头应 401, 实际 %d", code)
	}
	if code := do("/api/channels", true); code != http.StatusOK {
		t.Fatalf("/api 带头应 200, 实际 %d", code)
	}
	// 凭据路由也在鉴权范围内（新加的端点不能成为缺口）
	if code := do("/api/channels/1/credentials", false); code != http.StatusUnauthorized {
		t.Fatalf("凭据路由无头应 401, 实际 %d", code)
	}
	// /v1/models 走令牌鉴权而不是管理密钥：只带管理密钥（没有客户端
	// 令牌）应被令牌中间件拒掉——两个密钥体系互不通用，
	// 管理密钥不能顺手当转发令牌用
	if code := do("/v1/models", true); code != http.StatusUnauthorized {
		t.Fatalf("/v1/models 只带管理密钥应 401（需客户端令牌）, 实际 %d", code)
	}

	oversized := httptest.NewRequest(http.MethodPost, "/api/channels", strings.NewReader("{}"))
	oversized.ContentLength = middleware.MaxRequestBodyBytes + 1
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, oversized)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("超限管理请求应 413，实际 %d", rec.Code)
	}
}

// TestHealthzNotRateLimited：探活不能被限流打挂（P6 批次 3 验收项）。
//
// 回归保护。这条坑过一次：gin 的 Use 只对**之后**注册的路由生效，
// 所以 /healthz 必须注册在 IP 级限流之前。写反了的表现很隐蔽——
// 业务测试全绿（它们都走 /v1），只有探活连续打才会 429，
// 而那时负载均衡已经把健康的网关摘掉了。
func TestHealthzNotRateLimited(t *testing.T) {
	gin.SetMode(gin.TestMode)

	st, err := store.Open(config.DatabaseConfig{
		Driver: "sqlite",
		Path:   filepath.Join(t.TempDir(), "rl.db"),
	})
	if err != nil {
		t.Fatalf("开库失败: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	gw, err := gateway.New(gateway.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(gw.Close)
	e := NewEngine(Deps{
		Gateway:   gw,
		Cfg:       &config.Config{Server: config.ServerConfig{Addr: "127.0.0.1:0"}},
		Store:     st,
		AdminAuth: middleware.AdminAuth(testSecret),
		// IP 级限流故意设成极小（1 次/分钟）：/v1 请求会立刻打满它。
		// 此时如果 /healthz 也被限流，就会 429——这正是要防的。
		RateLimiter: middleware.NewMemoryLimiter(),
		IPRPMLimit:  1,
	})

	hit := func(path string) int {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		if path == "/healthz" {
			// 探活不带任何凭据
		} else {
			req.Header.Set("Authorization", "Bearer nope")
		}
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		return rec.Code
	}

	// 把 IP 配额打满：前两次过 IP 限流后撞上令牌鉴权（401）
	if code := hit("/v1/models"); code != http.StatusUnauthorized {
		t.Fatalf("第 1 次应被令牌鉴权拒（401），实际 %d", code)
	}
	if code := hit("/v1/models"); code != http.StatusTooManyRequests {
		t.Fatalf("IP 配额应已打满（期望 429），实际 %d——限流没生效？", code)
	}

	// 关键断言：同一个 IP、配额已满，探活必须仍然 200
	for i := 0; i < 5; i++ {
		if code := hit("/healthz"); code != http.StatusOK {
			t.Fatalf("IP 限流打满后 /healthz 仍应为 200，实际 %d（第 %d 次）", code, i+1)
		}
	}
}
