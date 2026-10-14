package gateway

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"minigateway/internal/billing"
	"minigateway/internal/config"
	dbmodel "minigateway/internal/model"
	"minigateway/internal/server/middleware"
	"minigateway/internal/store"
)

// 本文件验证 P6 批次 3 的 B3-2（embeddings 端点 + Mode 透传）。
//
// 与 relay_billing_test.go 同一套思路：真库 + 真 gin + 假上游 + 真计费，
// 从头到尾走完整链路，不 mock 任何一环。差别只在假上游要按路径分流：
// /v1/embeddings 返回向量，/v1/chat/completions 返回对话——这也顺带
// 验证了 Mode 透传真的改变了上游路径（否则两个端点会打到同一个地方，
// 假上游分不清，测试直接失败）。

// embeddingsGateway 是一次 embeddings 验收要用的组件。
type embeddingsGateway struct {
	engine           *gin.Engine
	store            *store.Store
	upstreamEmbedHit *int64 // 上游 /v1/embeddings 收到的请求数
	upstreamChatHit  *int64 // 上游 /v1/chat/completions 收到的请求数
	tokenKey         string
}

// newEmbeddingsGateway 搭一套支持 embeddings 的最小网关。
func newEmbeddingsGateway(t *testing.T) *embeddingsGateway {
	gw := testCoordinator(t)
	t.Helper()
	gin.SetMode(gin.TestMode)

	var embedHits, chatHits int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/embeddings":
			// 真 OpenAI embeddings 响应的形状：data[].embedding 是向量，
			// usage 只有 prompt_tokens/total_tokens，**没有**
			// completion_tokens——这正是「embeddings 只按输入计价」
			// 的数据来源。
			atomic.AddInt64(&embedHits, 1)
			var req struct {
				Model string `json:"model"`
				Input any    `json:"input"`
			}
			_ = json.NewDecoder(r.Body).Decode(&req)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"object": "list",
				"model":  req.Model,
				"data": []map[string]any{{
					"object":    "embedding",
					"index":     0,
					"embedding": []float64{0.0123, -0.0456, 0.0789},
				}},
				"usage": map[string]any{"prompt_tokens": 11, "total_tokens": 11},
			})
		case "/v1/chat/completions":
			atomic.AddInt64(&chatHits, 1)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"object":  "chat.completion",
				"choices": []map[string]any{{"index": 0, "message": map[string]any{"role": "assistant", "content": "hi"}}},
				"usage":   map[string]any{"prompt_tokens": 11, "completion_tokens": 7, "total_tokens": 18},
			})
		default:
			// 打到了意料之外的路径：直接 404 让测试失败得明显。
			// 这正是「Mode 没透传」会出现的症状——embeddings 请求
			// 被当成对话请求打到了 /v1/chat/completions。
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":"unexpected path"}`))
		}
	}))
	t.Cleanup(upstream.Close)

	st, err := store.Open(config.DatabaseConfig{
		Driver: "sqlite",
		Path:   filepath.Join(t.TempDir(), "gateway.db"),
	})
	if err != nil {
		t.Fatalf("开库失败: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	// 渠道同时配两个模型：对话模型和 embedding 模型。
	// 只配对话模型的话，embeddings 请求会因为「没有可用渠道」直接 503，
	// 那就测不到下游的 Mode 透传了。
	if _, err := st.CreateChannel(&store.ChannelInput{
		Name:      "fake-openai",
		Type:      "openai",
		BaseURL:   upstream.URL,
		Models:    []string{"gpt-4o-mini", "text-embedding-3-small"},
		APIKey:    "sk-test-upstream",
		GroupName: "default",
	}); err != nil {
		t.Fatalf("建渠道失败: %v", err)
	}
	const tokenKey = "sk-test-token"
	if _, err := st.CreateToken(&store.TokenInput{
		Key:  tokenKey,
		Name: "测试令牌",
	}); err != nil {
		t.Fatalf("建令牌失败: %v", err)
	}

	if err := gw.initRegistry(st.Channels()); err != nil {
		t.Fatalf("初始化转发层失败: %v", err)
	}
	gw.SetTimeout(10 * time.Second)

	// 两个模型都配价，输出价故意给 embedding 模型配一个非 0 值：
	// 这样「cost 只按输入计价」才能被真正验证到——如果计费错误地
	// 把 completion_tokens 算进去（或用了错误的模型价格），金额就会对不上。
	if err := st.UpsertModelPrice(&dbmodel.ModelPrice{
		Model: "gpt-4o-mini", InputPricePer1K: 0.15, OutputPricePer1K: 0.6,
	}); err != nil {
		t.Fatalf("写价格失败: %v", err)
	}
	if err := st.UpsertModelPrice(&dbmodel.ModelPrice{
		Model: "text-embedding-3-small", InputPricePer1K: 0.02, OutputPricePer1K: 99.0,
	}); err != nil {
		t.Fatalf("写价格失败: %v", err)
	}
	priceTable := billing.NewPriceTable(func() ([]dbmodel.ModelPrice, map[string]float64, error) {
		prices, err := st.ModelPrices()
		if err != nil {
			return nil, nil, err
		}
		rates, err := st.GroupRates()
		if err != nil {
			return nil, nil, err
		}
		return prices, rates, nil
	}, billing.DefaultPriceTTL)
	eng := billing.New(st.DB(), priceTable)

	gw.SetBilling(eng)
	gw.SetLogRecorder(st.WriteLog)

	e := gin.New()
	e.Use(middleware.RequestID())
	auth := middleware.NewAuth(st)
	// 两个入口都挂上，验证它们走同一条转发链路但打不同上游路径
	e.POST("/v1/chat/completions", auth.Handle(), gw.HandleChatCompletions)
	e.POST("/v1/embeddings", auth.Handle(), gw.HandleEmbeddings)

	return &embeddingsGateway{
		engine:           e,
		store:            st,
		upstreamEmbedHit: &embedHits,
		upstreamChatHit:  &chatHits,
		tokenKey:         tokenKey,
	}
}

// sendEmbeddings 发一次 embeddings 请求。
func (g *embeddingsGateway) sendEmbeddings(t *testing.T, body string) (int, string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/embeddings", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+g.tokenKey)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	g.engine.ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

// waitForLogs 等日志落库（异步写入）。
func (g *embeddingsGateway) waitForLogs(t *testing.T, n int64) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		got, err := g.store.CountLogs()
		if err != nil {
			t.Fatalf("数日志失败: %v", err)
		}
		if got >= n {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	got, _ := g.store.CountLogs()
	t.Fatalf("等日志落库超时: 期望 %d, 实际 %d", n, got)
}

// TestEmbeddingsForwardsToEmbeddingsPath：embeddings 请求必须打到
// 上游的 /v1/embeddings，而不是 /v1/chat/completions。
//
// 这条测的是 Mode 透传（B3-2 的核心改动）：buildMeta 以前把 Mode
// 写死成对话模式，适配器据此拼出的永远是 /v1/chat/completions。
// 透传之后 Mode 由 handler 按路由传入，适配器才分得出该打哪。
func TestEmbeddingsForwardsToEmbeddingsPath(t *testing.T) {
	g := newEmbeddingsGateway(t)

	code, body := g.sendEmbeddings(t,
		`{"model":"text-embedding-3-small","input":"hello world"}`)
	if code != http.StatusOK {
		t.Fatalf("embeddings 请求应 200，实际 %d: %s", code, body)
	}
	if got := atomic.LoadInt64(g.upstreamEmbedHit); got != 1 {
		t.Fatalf("上游 /v1/embeddings 应收到 1 次，实际 %d（Mode 没透传的话会打到 chat/completions）", got)
	}
	if got := atomic.LoadInt64(g.upstreamChatHit); got != 0 {
		t.Fatalf("上游 /v1/chat/completions 不该被 embeddings 请求命中，实际 %d", got)
	}

	// 响应里要有向量
	var resp struct {
		Data []struct {
			Embedding []float64 `json:"embedding"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		t.Fatalf("响应不是合法 JSON: %v (%s)", err, body)
	}
	if len(resp.Data) == 0 || len(resp.Data[0].Embedding) == 0 {
		t.Fatalf("响应里应包含向量，实际 %s", body)
	}
}

// TestChatStillGoesToChatPath：对话入口没被 embeddings 的改动带偏。
//
// 回归保护：Mode 改成透传之后，对话请求必须还能正确拼出
// /v1/chat/completions。少这一条，透传改错了对话入口是测不出来的。
func TestChatStillGoesToChatPath(t *testing.T) {
	g := newEmbeddingsGateway(t)

	code, body := g.sendChat(t,
		`{"model":"gpt-4o-mini","messages":[{"role":"user","content":"hi"}]}`)
	if code != http.StatusOK {
		t.Fatalf("对话请求应 200，实际 %d: %s", code, body)
	}
	if got := atomic.LoadInt64(g.upstreamChatHit); got != 1 {
		t.Fatalf("上游 /v1/chat/completions 应收到 1 次，实际 %d", got)
	}
	if got := atomic.LoadInt64(g.upstreamEmbedHit); got != 0 {
		t.Fatalf("上游 /v1/embeddings 不该被对话请求命中，实际 %d", got)
	}
}

// TestEmbeddingsBillingInputOnly：embeddings 只按输入计价。
//
// 手算对照：上游返回 usage {prompt_tokens: 11, total_tokens: 11}，
// 没有 completion_tokens。text-embedding-3-small 输入价 0.02/1K：
//
//	cost = 11/1000 × 0.02 = 0.00022
//
// 输出价故意配成 99.0：如果计费错误地把 completion_tokens 算进去
// （哪怕算成 0 也会因为用到错误的模型价格而对不上），金额会明显偏大。
// 这个「离谱的输出价」是专门为了让错误藏不住。
func TestEmbeddingsBillingInputOnly(t *testing.T) {
	g := newEmbeddingsGateway(t)

	code, body := g.sendEmbeddings(t,
		`{"model":"text-embedding-3-small","input":"hello world"}`)
	if code != http.StatusOK {
		t.Fatalf("embeddings 请求应 200，实际 %d: %s", code, body)
	}
	g.waitForLogs(t, 1)

	logs, err := g.store.ListLogs(store.LogQuery{Limit: 1})
	if err != nil {
		t.Fatalf("查日志失败: %v", err)
	}
	if len(logs) == 0 {
		t.Fatal("一条日志都没有")
	}
	entry := logs[0]

	// prompt_tokens 有值：说明 usage 抽取对 embeddings 响应生效了
	if entry.PromptTokens != 11 {
		t.Fatalf("prompt_tokens 应为 11，实际 %d", entry.PromptTokens)
	}
	// completion_tokens 必须是 0：embeddings 响应里没这个字段，
	// 反序列化后天然是 0。不为 0 说明计费把输出算进去了。
	if entry.CompletionTokens != 0 {
		t.Fatalf("embeddings 的 completion_tokens 应为 0，实际 %d（embeddings 没有输出 token）", entry.CompletionTokens)
	}
	if entry.TotalTokens != 11 {
		t.Fatalf("total_tokens 应为 11，实际 %d", entry.TotalTokens)
	}
	if entry.IsEstimated {
		t.Fatal("上游给了 usage，不该标记为估算")
	}

	want := 11.0 / 1000 * 0.02
	if diff := entry.Cost - want; diff > 1e-9 || diff < -1e-9 {
		t.Fatalf("cost 应为 %.6f（只按输入计价），实际 %.6f", want, entry.Cost)
	}
}

// TestEmbeddingsUnknownModelNoChannel：模型没配进渠道时返回 503。
//
// 这条防的是「索引查不到就 panic」这类静默故障：pickChannels 走
// 内存索引后，模型名不在索引里必须走「没有可用渠道」的正常错误路径。
func TestEmbeddingsUnknownModelNoChannel(t *testing.T) {
	g := newEmbeddingsGateway(t)

	code, body := g.sendEmbeddings(t,
		`{"model":"text-embedding-does-not-exist","input":"hi"}`)
	if code != http.StatusServiceUnavailable {
		t.Fatalf("未知模型应 503，实际 %d: %s", code, body)
	}
	if got := atomic.LoadInt64(g.upstreamEmbedHit); got != 0 {
		t.Fatalf("选不到渠道时不该打上游，实际打了 %d 次", got)
	}
}

// TestEmbeddingsArrayInput：批量输入（字符串数组）也能正常转发。
func TestEmbeddingsArrayInput(t *testing.T) {
	g := newEmbeddingsGateway(t)

	code, body := g.sendEmbeddings(t,
		`{"model":"text-embedding-3-small","input":["第一段","second chunk"]}`)
	if code != http.StatusOK {
		t.Fatalf("批量输入应 200，实际 %d: %s", code, body)
	}
	if got := atomic.LoadInt64(g.upstreamEmbedHit); got != 1 {
		t.Fatalf("上游 /v1/embeddings 应收到 1 次，实际 %d", got)
	}
}

// TestEmbeddingsRequiresAuth：embeddings 同样要鉴权。
func TestEmbeddingsRequiresAuth(t *testing.T) {
	g := newEmbeddingsGateway(t)

	req := httptest.NewRequest(http.MethodPost, "/v1/embeddings",
		strings.NewReader(`{"model":"text-embedding-3-small","input":"hi"}`))
	req.Header.Set("Authorization", "Bearer sk-wrong-key")
	rec := httptest.NewRecorder()
	g.engine.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("错误令牌应 401，实际 %d", rec.Code)
	}
	if got := atomic.LoadInt64(g.upstreamEmbedHit); got != 0 {
		t.Fatalf("鉴权失败不该打上游，实际打了 %d 次", got)
	}
}

// sendChat 供对话回归用例复用（与 relay_billing_test.go 的同名方法
// 各自独立，因为两边的 engine 不是同一个）。
func (g *embeddingsGateway) sendChat(t *testing.T, body string) (int, string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+g.tokenKey)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	g.engine.ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

// TestEmbeddingsWithoutUsageFallsBackToEstimate：上游没给 usage 时
// 降级为字符估算，且能正确从 embeddings 的 input 字段抽文本。
//
// 这条专门保护 counter.go 里「input 也要认」的改动：embeddings 请求体
// 没有 messages 字段，不认 input 的话估算恒为 0，日志里 prompt_tokens
// 会是 0——比估算不准更糟，那是彻底没记账。
func TestEmbeddingsWithoutUsageFallsBackToEstimate(t *testing.T) {
	gw := testCoordinator(t)
	// 单独搭一个「不给 usage」的上游：走 newEmbeddingsGateway 的话
	// usage 恒有，这条用例就没意义了。
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"object": "list",
			"model":  "text-embedding-3-small",
			"data":   []map[string]any{{"object": "embedding", "index": 0, "embedding": []float64{0.1}}},
			// 故意不带 usage
		})
	}))
	t.Cleanup(upstream.Close)

	st, err := store.Open(config.DatabaseConfig{
		Driver: "sqlite",
		Path:   filepath.Join(t.TempDir(), "gateway.db"),
	})
	if err != nil {
		t.Fatalf("开库失败: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	if _, err := st.CreateChannel(&store.ChannelInput{
		Name: "fake", Type: "openai", BaseURL: upstream.URL,
		Models: []string{"text-embedding-3-small"}, APIKey: "sk-up", GroupName: "default",
	}); err != nil {
		t.Fatalf("建渠道失败: %v", err)
	}
	const tokenKey = "sk-tok"
	if _, err := st.CreateToken(&store.TokenInput{Key: tokenKey, Name: "t"}); err != nil {
		t.Fatalf("建令牌失败: %v", err)
	}
	if err := gw.initRegistry(st.Channels()); err != nil {
		t.Fatalf("初始化转发层失败: %v", err)
	}
	gw.SetTimeout(10 * time.Second)
	if err := st.UpsertModelPrice(&dbmodel.ModelPrice{
		Model: "text-embedding-3-small", InputPricePer1K: 0.02, OutputPricePer1K: 0,
	}); err != nil {
		t.Fatalf("写价格失败: %v", err)
	}
	priceTable := billing.NewPriceTable(func() ([]dbmodel.ModelPrice, map[string]float64, error) {
		prices, err := st.ModelPrices()
		if err != nil {
			return nil, nil, err
		}
		rates, err := st.GroupRates()
		if err != nil {
			return nil, nil, err
		}
		return prices, rates, nil
	}, billing.DefaultPriceTTL)
	gw.SetBilling(billing.New(st.DB(), priceTable))
	gw.SetLogRecorder(st.WriteLog)

	e := gin.New()
	e.Use(middleware.RequestID())
	e.POST("/v1/embeddings", middleware.NewAuth(st).Handle(), gw.HandleEmbeddings)

	// 一段足够长的中文输入，估算值应该明显大于 0
	const inputText = "这是一段用于测试 embeddings 估算降级路径的输入文本，需要足够长才能让估算值明显大于零"
	req := httptest.NewRequest(http.MethodPost, "/v1/embeddings",
		strings.NewReader(fmt.Sprintf(`{"model":"text-embedding-3-small","input":%q}`, inputText)))
	req.Header.Set("Authorization", "Bearer "+tokenKey)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("应 200，实际 %d: %s", rec.Code, rec.Body.String())
	}

	deadline := time.Now().Add(5 * time.Second)
	var entry dbmodel.RequestLog
	for time.Now().Before(deadline) {
		logs, err := st.ListLogs(store.LogQuery{Limit: 1})
		if err != nil {
			t.Fatalf("查日志失败: %v", err)
		}
		if len(logs) > 0 {
			entry = logs[0]
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !entry.IsEstimated {
		t.Fatal("上游没给 usage，日志应标记为估算")
	}
	if entry.PromptTokens == 0 {
		t.Fatal("降级估算的 prompt_tokens 不该为 0（embeddings 的 input 字段必须被识别，否则等于没记账）")
	}
	if entry.CompletionTokens != 0 {
		t.Fatalf("embeddings 的 completion_tokens 应为 0，实际 %d", entry.CompletionTokens)
	}
}
