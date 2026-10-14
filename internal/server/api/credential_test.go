package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"minigateway/internal/config"
	"minigateway/internal/server/middleware"
	"minigateway/internal/store"
)

// 本文件覆盖管理 API 的 HTTP 面：渠道详情脱敏（B1-3）、凭据路由
// （B1-2 的接线）、以及 /api 分组的鉴权强制（B1-1 的装配验证）。
//
// 为什么在这里补一层：store 测试保证规则对，api 测试保证「对的规则
// 被正确地接到了 HTTP 上」——掩码漏挂、reveal 参数拼错、路由少注册
// 一类的问题只有打真请求才能发现。

const testSecret = "sk-admin-test-0123456789"

// newAPITestEngine 搭一套带真 store、真鉴权的管理 API 引擎。
func newAPITestEngine(t *testing.T) (*gin.Engine, *store.Store) {
	t.Helper()
	gin.SetMode(gin.TestMode)

	st, err := store.Open(config.DatabaseConfig{
		Driver: "sqlite",
		Path:   filepath.Join(t.TempDir(), "api.db"),
	})
	if err != nil {
		t.Fatalf("开库失败: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	// 渠道带 3 把凭据，方便验证掩码/列表/reveal 在多凭据下的行为
	if _, err := st.CreateChannel(&store.ChannelInput{
		Name:    "mask-test",
		Type:    "openai",
		BaseURL: "http://127.0.0.1:9911",
		Models:  []string{"gpt-4o-mini"},
		APIKey:  "sk-aaaa1111bbbb2222cccc",
	}); err != nil {
		t.Fatalf("建渠道失败: %v", err)
	}
	for _, k := range []string{"sk-dddd3333eeee4444ffff", "sk-gggg5555hhhh6666iiii"} {
		if _, err := st.CreateCredential(1, &store.CredentialInput{Name: "extra", APIKey: k}); err != nil {
			t.Fatalf("加凭据失败: %v", err)
		}
	}

	h := New(st, nil, nil)
	e := gin.New()
	h.Register(e.Group(""), middleware.AdminAuth(testSecret))
	return e, st
}

// doAPI 发一次管理请求。withAuth=false 时刻意不带头，用于验证鉴权。
func doAPI(t *testing.T, e *gin.Engine, method, path, body string, withAuth bool) (int, string) {
	t.Helper()
	var r *strings.Reader
	if body != "" {
		r = strings.NewReader(body)
	} else {
		r = strings.NewReader("")
	}
	req := httptest.NewRequest(method, path, r)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if withAuth {
		req.Header.Set("X-Admin-Key", testSecret)
	}
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

// decodeData 解出 {"data": ...} 里的 data 字段。
func decodeData(t *testing.T, body string) map[string]any {
	t.Helper()
	var resp struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		t.Fatalf("解析响应失败: %v (%s)", err, body)
	}
	return resp.Data
}

// TestGetChannelMasksKeyByDefault 对应验收 B1-3：默认掩码。
//
// 断言的是「完整 Key 不出现在响应体的任何位置」，而不是「某个字段
// 等于某个掩码值」——前者才是安全属性，后者只是格式约定，
// 格式对但 Key 漏在别处（比如某个调试字段）时后者照样通过。
func TestGetChannelMasksKeyByDefault(t *testing.T) {
	e, st := newAPITestEngine(t)

	code, body := doAPI(t, e, http.MethodGet, "/api/channels/1", "", true)
	if code != http.StatusOK {
		t.Fatalf("详情请求失败: %d %s", code, body)
	}
	if strings.Contains(body, "sk-aaaa1111bbbb2222cccc") {
		t.Fatal("默认响应泄露了完整 api_key")
	}
	// 掩码形态：前 3 位 + "..." + 后 4 位
	data := decodeData(t, body)
	creds, ok := data["credentials"].([]any)
	if !ok || len(creds) != 3 {
		t.Fatalf("应有 3 个凭据, 实际 %v", data["credentials"])
	}
	first := creds[0].(map[string]any)["api_key"].(string)
	if first != "sk-...cccc" {
		t.Fatalf("掩码形态错误: %s (期望 sk-...cccc)", first)
	}
	if got := st.Channels()[0].Credentials[0].APIKey; got != "sk-aaaa1111bbbb2222cccc" {
		t.Fatalf("读取 API 不应修改共享凭据缓存，got %q", got)
	}
}

// TestGetChannelNeverRevealsKey verifies existing upstream keys stay masked.
func TestGetChannelNeverRevealsKey(t *testing.T) {
	e, _ := newAPITestEngine(t)

	for _, query := range []string{"", "?reveal=1", "?reveal=true"} {
		code, body := doAPI(t, e, http.MethodGet, "/api/channels/1"+query, "", true)
		if code != http.StatusOK {
			t.Fatalf("详情请求失败: %d %s", code, body)
		}
		if strings.Contains(body, "sk-aaaa1111bbbb2222cccc") {
			t.Fatalf("详情接口不应回显明文 key，query=%q", query)
		}
	}
}

func TestCreateChannelDoesNotEchoUpstreamKey(t *testing.T) {
	e, _ := newAPITestEngine(t)
	const key = "sk-channel-create-secret-123456"
	code, body := doAPI(t, e, http.MethodPost, "/api/channels",
		`{"name":"created","type":"openai","base_url":"http://127.0.0.1:9912","models":["gpt-4o-mini"],"api_key":"`+key+`"}`, true)
	if code != http.StatusCreated {
		t.Fatalf("创建渠道失败: status=%d body=%s", code, body)
	}
	if strings.Contains(body, key) {
		t.Fatal("创建渠道响应不应回显上游明文 key")
	}
}

// TestAdminAPIRequiresAuth 验证 /api 分组真的挂了鉴权。
//
// 这一条测的是「装配」而不是「中间件」（中间件有自己的单测）：
// 路由注册时漏了 group.Use(adminAuth) 的话，中间件写得再对也没用。
// 顺带确认 /healthz 不受影响（健康检查不该被管理密钥挡住，
// 否则 Docker healthcheck 会误判进程死亡）。
func TestAdminAPIRequiresAuth(t *testing.T) {
	e, _ := newAPITestEngine(t)

	// 无头 → 401
	if code, _ := doAPI(t, e, http.MethodGet, "/api/channels", "", false); code != http.StatusUnauthorized {
		t.Fatalf("无头调管理 API 应 401, 实际 %d", code)
	}
	// 错密钥 → 401
	req := httptest.NewRequest(http.MethodGet, "/api/channels", nil)
	req.Header.Set("X-Admin-Key", "wrong-key")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("错密钥应 401, 实际 %d", rec.Code)
	}
	for _, target := range []struct{ method, path string }{
		{http.MethodGet, "/api/channels/1"},
		{http.MethodPost, "/api/channels"},
		{http.MethodPut, "/api/credentials/1"},
		{http.MethodDelete, "/api/tokens/1"},
		{http.MethodGet, "/api/logs"},
		{http.MethodGet, "/api/prices"},
	} {
		req := httptest.NewRequest(target.method, target.path, strings.NewReader(`{}`))
		req.Header.Set("Authorization", "Bearer client-token")
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("普通客户端令牌不应访问 %s %s: status=%d body=%s", target.method, target.path, rec.Code, rec.Body.String())
		}
	}
	// 带头 → 200
	if code, _ := doAPI(t, e, http.MethodGet, "/api/channels", "", true); code != http.StatusOK {
		t.Fatalf("正确密钥应 200, 实际 %d", code)
	}
}

// TestCredentialRoutes 验证凭据的五个路由端到端可用。
//
// 只测「路由接对了 + 状态码语义对」，CRUD 规则本身由 store 层测试
// 覆盖——两层分开，避免同一份断言抄两遍还漂移。
func TestCredentialRoutes(t *testing.T) {
	e, st := newAPITestEngine(t)

	// 列：3 把，key 打码
	code, body := doAPI(t, e, http.MethodGet, "/api/channels/1/credentials", "", true)
	if code != http.StatusOK {
		t.Fatalf("列凭据失败: %d %s", code, body)
	}
	if strings.Contains(body, "sk-dddd3333eeee4444ffff") {
		t.Fatal("凭据列表泄露了完整 api_key")
	}

	// 增：创建响应回显完整 key（用户唯一一次看到它的机会）
	code, body = doAPI(t, e, http.MethodPost, "/api/channels/1/credentials",
		`{"name":"new","api_key":"sk-new-key-9999"}`, true)
	if code != http.StatusCreated {
		t.Fatalf("增凭据应 201, 实际 %d %s", code, body)
	}
	if !strings.Contains(body, "sk-new-key-9999") {
		t.Fatal("创建响应应回显完整 api_key")
	}
	if len(st.ListCredentials(1)) != 4 {
		t.Fatalf("库里应有 4 把, 实际 %d", len(st.ListCredentials(1)))
	}

	// 重复 key → 4xx
	code, body = doAPI(t, e, http.MethodPost, "/api/channels/1/credentials",
		`{"api_key":"sk-new-key-9999"}`, true)
	if code < 400 || code >= 500 {
		t.Fatalf("重复 key 应 4xx, 实际 %d %s", code, body)
	}

	// 改权重
	creds := st.ListCredentials(1)
	target := creds[0].ID
	code, body = doAPI(t, e, http.MethodPut, "/api/credentials/"+itoa(target),
		`{"api_key":"sk-aaaa1111bbbb2222cccc","weight":7}`, true)
	if code != http.StatusOK {
		t.Fatalf("改凭据应 200, 实际 %d", code)
	}
	if strings.Contains(body, "sk-aaaa1111bbbb2222cccc") {
		t.Fatal("更新凭据响应不应回显已有明文 key")
	}

	// 设状态：banned
	code, body = doAPI(t, e, http.MethodPost, "/api/credentials/"+itoa(target)+"/status",
		`{"status":"banned"}`, true)
	if code != http.StatusOK {
		t.Fatalf("设状态应 200, 实际 %d", code)
	}
	if strings.Contains(body, "sk-aaaa1111bbbb2222cccc") {
		t.Fatal("状态更新响应不应回显已有明文 key")
	}
	if got, _ := st.GetCredential(target); got.Status != "banned" {
		t.Fatalf("状态没写进库: %s", got.Status)
	}

	// 删：剩 3 把时删一把 OK；删到剩 1 把时再删 → 4xx
	for _, c := range st.ListCredentials(1) {
		if len(st.ListCredentials(1)) <= 1 {
			break
		}
		code, body := doAPI(t, e, http.MethodDelete, "/api/credentials/"+itoa(c.ID), "", true)
		if code != http.StatusOK {
			t.Fatalf("删凭据应 200, 实际 %d %s", code, body)
		}
	}
	rest := st.ListCredentials(1)
	if len(rest) != 1 {
		t.Fatalf("应剩 1 把, 实际 %d", len(rest))
	}
	code, _ = doAPI(t, e, http.MethodDelete, "/api/credentials/"+itoa(rest[0].ID), "", true)
	if code < 400 || code >= 500 {
		t.Fatalf("删最后一个凭据应 4xx, 实际 %d", code)
	}

	// 不存在的凭据 → 404
	code, _ = doAPI(t, e, http.MethodPut, "/api/credentials/9999", `{"api_key":"sk-x"}`, true)
	if code != http.StatusNotFound {
		t.Fatalf("不存在的凭据应 404, 实际 %d", code)
	}
}

func TestTokenMutationDoesNotEchoExistingKey(t *testing.T) {
	e, st := newAPITestEngine(t)
	const key = "sk-existing-token-secret-123456"
	token, err := st.CreateToken(&store.TokenInput{Key: key, Name: "existing"})
	if err != nil {
		t.Fatalf("创建测试令牌失败: %v", err)
	}

	code, body := doAPI(t, e, http.MethodPut, "/api/tokens/"+itoa(token.ID),
		`{"key":"sk-existing-token-secret-123456","name":"renamed"}`, true)
	if code != http.StatusOK || strings.Contains(body, key) {
		t.Fatalf("更新令牌应成功且不回显已有 key: status=%d body=%s", code, body)
	}

	code, body = doAPI(t, e, http.MethodPost, "/api/tokens/"+itoa(token.ID)+"/quota", `{"quota":5}`, true)
	if code != http.StatusOK || strings.Contains(body, key) {
		t.Fatalf("设置额度应成功且不回显已有 key: status=%d body=%s", code, body)
	}
}

// itoa 避免在测试里引 strconv 只为一个转换。
func itoa(n uint) string {
	if n == 0 {
		return "0"
	}
	var buf []byte
	for n > 0 {
		buf = append([]byte{byte('0' + n%10)}, buf...)
		n /= 10
	}
	return string(buf)
}
