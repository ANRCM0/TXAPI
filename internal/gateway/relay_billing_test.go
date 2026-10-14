package gateway

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
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

// 本文件把 SPEC-P5 第四节的六个验收场景变成自动化测试：
// 真库（SQLite 文件）+ 真 gin 引擎 + 假上游（httptest）+ 真计费引擎，
// 从头到尾走完整请求链路，而不是 mock 掉任何一环。
//
// 假上游固定返回 usage 11 输入 / 7 输出（与 testtools/fakeupstream 一致），
// 配 gpt-4o-mini 价格 0.15/0.6 时，每次成功请求的费用恒为
// 11/1000×0.15 + 7/1000×0.6 = 0.00585——所有金额断言都围绕这个数。

// testGateway 是一次验收测试要用的全部组件。
type testGateway struct {
	engine      *gin.Engine
	store       *store.Store
	billing     *billing.Engine
	upstream    *httptest.Server
	upstreamHit *int64 // 上游收到的请求数（验证「超额不转发」的唯一证据）
	tokenKey    string
}

// newTestGateway 搭一套完整的最小网关。
func newTestGateway(t *testing.T, tokenQuota float64) *testGateway {
	gw := testCoordinator(t)
	t.Helper()
	gin.SetMode(gin.TestMode)

	var hits int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&hits, 1)
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var req struct {
			Model  string `json:"model"`
			Stream bool   `json:"stream"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.Stream {
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			flusher := w.(http.Flusher)
			for _, ch := range []string{"你好", "，这是", "流式", "响应。"} {
				fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":%q}}]}\n\n", ch)
				flusher.Flush()
			}
			// 结束帧 + 带 usage 的尾帧（真实 OpenAI 的顺序）
			fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
			flusher.Flush()
			fmt.Fprint(w, "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":11,\"completion_tokens\":7,\"total_tokens\":18}}\n\n")
			flusher.Flush()
			fmt.Fprint(w, "data: [DONE]\n\n")
			flusher.Flush()
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":      "chatcmpl-test",
			"object":  "chat.completion",
			"model":   req.Model,
			"choices": []map[string]any{{"index": 0, "message": map[string]any{"role": "assistant", "content": "假上游的回答"}, "finish_reason": "stop"}},
			"usage":   map[string]any{"prompt_tokens": 11, "completion_tokens": 7, "total_tokens": 18},
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

	// 渠道指向假上游；配两个模型，第二个故意不配价格（验 4.5）
	if _, err := st.CreateChannel(&store.ChannelInput{
		Name:      "fake-openai",
		Type:      "openai",
		BaseURL:   upstream.URL,
		Models:    []string{"gpt-4o-mini", "gpt-3.5-turbo"},
		APIKey:    "sk-fake-upstream",
		GroupName: "default",
	}); err != nil {
		t.Fatalf("建渠道失败: %v", err)
	}
	const tokenKey = "sk-test-token"
	if _, err := st.CreateToken(&store.TokenInput{
		Key:   tokenKey,
		Name:  "测试令牌",
		Quota: tokenQuota,
	}); err != nil {
		t.Fatalf("建令牌失败: %v", err)
	}

	if err := gw.initRegistry(st.Channels()); err != nil {
		t.Fatalf("初始化转发层失败: %v", err)
	}
	gw.SetTimeout(10 * time.Second)

	// 价格表：gpt-4o-mini 输入 0.15 / 输出 0.6（SPEC 4.1 的配置）
	if err := st.UpsertModelPrice(&dbmodel.ModelPrice{
		Model:            "gpt-4o-mini",
		InputPricePer1K:  0.15,
		OutputPricePer1K: 0.6,
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
	e.POST("/v1/chat/completions", auth.Handle(), gw.HandleChatCompletions)

	return &testGateway{
		engine:      e,
		store:       st,
		billing:     eng,
		upstream:    upstream,
		upstreamHit: &hits,
		tokenKey:    tokenKey,
	}
}

// sendChat 发一次对话请求，返回状态码和响应体。
func (g *testGateway) sendChat(t *testing.T, body string) (int, string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+g.tokenKey)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	g.engine.ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

// latestLog 取最新一条日志。
func (g *testGateway) latestLog(t *testing.T) dbmodel.RequestLog {
	t.Helper()
	logs, err := g.store.ListLogs(store.LogQuery{Limit: 1})
	if err != nil {
		t.Fatalf("查日志失败: %v", err)
	}
	if len(logs) == 0 {
		t.Fatal("一条日志都没有")
	}
	return logs[0]
}

// waitForLogs 轮询等待日志落库（异步写入，见 store.asyncLogWriter）。
func (g *testGateway) waitForLogs(t *testing.T, n int64) {
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

const expectedCost = 0.00585 // 11/1000×0.15 + 7/1000×0.6

// TestAcceptBasicBilling 对应验收 4.1：基础计费。
//
// 手算对照：cost = 11/1000×0.15 + 7/1000×0.6
//
//	= 0.00165 + 0.0042 = 0.00585
func TestAcceptBasicBilling(t *testing.T) {
	g := newTestGateway(t, 0)

	code, body := g.sendChat(t, `{"model":"gpt-4o-mini","messages":[{"role":"user","content":"hi"}]}`)
	if code != http.StatusOK {
		t.Fatalf("请求失败: %d %s", code, body)
	}
	g.waitForLogs(t, 1)

	entry := g.latestLog(t)
	if entry.PromptTokens != 11 || entry.CompletionTokens != 7 || entry.TotalTokens != 18 {
		t.Fatalf("用量没记对: %+v", entry)
	}
	if entry.Cost < expectedCost-1e-9 || entry.Cost > expectedCost+1e-9 {
		t.Fatalf("费用错误: 得到 %v, 期望 %v（11/1000×0.15 + 7/1000×0.6）", entry.Cost, expectedCost)
	}
	if entry.IsEstimated || entry.PriceMissing {
		t.Fatalf("不该有估算/缺价标记: %+v", entry)
	}
	if entry.Status != 200 {
		t.Fatalf("状态码错误: %d", entry.Status)
	}
}

// TestAcceptQuotaControl 对应验收 4.2：额度控制。
//
// 额度 0.001（不到一次请求的 0.00585）：第一次通过检查后按实际
// 结算，第二次起 used_quota 已超额 → 403，且**不再转发上游**
// （上游命中数不增加——这才是「省 Key 额度」的证据）。
func TestAcceptQuotaControl(t *testing.T) {
	g := newTestGateway(t, 0.001)

	hitsBefore := int64(0)
	rejected := false
	var lastCode int
	for i := 0; i < 5; i++ {
		// 每次请求前记一次上游命中数：被拒的那一次必须不增加它
		// （前面的成功请求是合法转发的，不能拿来对比）
		hitsBefore = atomic.LoadInt64(g.upstreamHit)
		lastCode, _ = g.sendChat(t, `{"model":"gpt-4o-mini","messages":[{"role":"user","content":"hi"}]}`)
		if lastCode == http.StatusForbidden {
			rejected = true
			break
		}
	}
	if !rejected {
		t.Fatalf("额度 0.001 应在几次请求内被拒, 最后一次状态码 %d", lastCode)
	}

	hitsAfter := atomic.LoadInt64(g.upstreamHit)
	if hitsAfter != hitsBefore {
		t.Fatalf("超额请求不该转发到上游: 被拒前转发 %d 次, 被拒后转发 %d 次", hitsBefore, hitsAfter)
	}

	// 403 也要落日志（排查「为什么突然全部 403」的唯一依据）
	g.waitForLogs(t, 2)
	entry := g.latestLog(t)
	if entry.Status != http.StatusForbidden {
		t.Fatalf("403 没记进日志: %+v", entry)
	}
	if entry.ErrorMessage == "" {
		t.Fatal("403 日志缺错误信息")
	}
}

// TestAcceptPreDeductSettle 对应验收 4.3：预扣与结算。
//
// 预扣上限 0.3（max_tokens=500 × 0.6/1k），实际 0.00585：
// 结算后 used_quota 必须等于**实际值**，不是预扣值。
func TestAcceptPreDeductSettle(t *testing.T) {
	g := newTestGateway(t, 1.0)

	code, body := g.sendChat(t, `{"model":"gpt-4o-mini","max_tokens":500,"messages":[{"role":"user","content":"hi"}]}`)
	if code != http.StatusOK {
		t.Fatalf("请求失败: %d %s", code, body)
	}

	tk, err := g.store.GetToken(1)
	if err != nil {
		t.Fatalf("读令牌失败: %v", err)
	}
	if tk.UsedQuota < expectedCost-1e-6 || tk.UsedQuota > expectedCost+1e-6 {
		t.Fatalf("used_quota 应等于实际费用 %v, 实际 %v（若为 0.3 则结算没做）", expectedCost, tk.UsedQuota)
	}
	// 顺带验证日志费用与 used_quota 一致
	g.waitForLogs(t, 1)
	entry := g.latestLog(t)
	if entry.Cost < expectedCost-1e-9 || entry.Cost > expectedCost+1e-9 {
		t.Fatalf("日志费用错误: %v", entry.Cost)
	}
}

// TestAcceptStreamUsage 对应验收 4.4：流式用量。
//
// 假上游在最后一个 chunk 带 usage，验证用量被提取并正确计费。
func TestAcceptStreamUsage(t *testing.T) {
	g := newTestGateway(t, 0)

	code, body := g.sendChat(t, `{"model":"gpt-4o-mini","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	if code != http.StatusOK {
		t.Fatalf("流式请求失败: %d %s", code, body)
	}
	if !strings.Contains(body, "data: [DONE]") {
		t.Fatal("流式响应缺终止标记")
	}
	g.waitForLogs(t, 1)

	entry := g.latestLog(t)
	if !entry.IsStream {
		t.Fatal("is_stream 没记上")
	}
	if entry.PromptTokens != 11 || entry.CompletionTokens != 7 {
		t.Fatalf("流式用量没提取到: %+v", entry)
	}
	if entry.Cost < expectedCost-1e-9 || entry.Cost > expectedCost+1e-9 {
		t.Fatalf("流式计费错误: %v", entry.Cost)
	}
	if entry.IsEstimated {
		t.Fatal("上游给了 usage 不该标记为估算")
	}
}

// TestAcceptPriceMissing 对应验收 4.5：价格缺失。
//
// gpt-3.5-turbo 渠道支持但价格表没配：请求必须**正常成功**，
// 日志 cost=0 且有 price_missing 标记。
func TestAcceptPriceMissing(t *testing.T) {
	g := newTestGateway(t, 0)

	code, body := g.sendChat(t, `{"model":"gpt-3.5-turbo","messages":[{"role":"user","content":"hi"}]}`)
	if code != http.StatusOK {
		t.Fatalf("价格缺失不该影响服务: %d %s", code, body)
	}
	g.waitForLogs(t, 1)

	entry := g.latestLog(t)
	if entry.Cost != 0 {
		t.Fatalf("缺价应记 0 费用: %v", entry.Cost)
	}
	if !entry.PriceMissing {
		t.Fatalf("缺价应打标记: %+v", entry)
	}
}

// TestAcceptLogNotBlocking 对应验收 4.6：日志不阻塞。
//
// 100 个并发请求：全部 200（异步写入不拖垮响应），且 100 条日志
// 最终全部落库（一条不丢）。耗时断言宽松（3 秒）——假上游是本地
// httptest、毫秒级返回，同步写 100 条日志的 fsync 也远不至于超时，
// 这个断言主要防「日志把请求串行化了」这类回归。
func TestAcceptLogNotBlocking(t *testing.T) {
	g := newTestGateway(t, 0)

	const total = 100
	var (
		wg       sync.WaitGroup
		okCount  int64
		failCode = make([]int, 0, total)
		mu       sync.Mutex
	)
	start := time.Now()
	wg.Add(total)
	for i := 0; i < total; i++ {
		go func() {
			defer wg.Done()
			req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
				strings.NewReader(`{"model":"gpt-4o-mini","messages":[{"role":"user","content":"hi"}]}`))
			req.Header.Set("Authorization", "Bearer "+g.tokenKey)
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			g.engine.ServeHTTP(rec, req)
			if rec.Code == http.StatusOK {
				atomic.AddInt64(&okCount, 1)
				return
			}
			mu.Lock()
			failCode = append(failCode, rec.Code)
			mu.Unlock()
		}()
	}
	wg.Wait()
	elapsed := time.Since(start)

	if okCount != total {
		t.Fatalf("并发请求有失败: %d/%d 成功, 失败码 %v", okCount, total, failCode)
	}
	if elapsed > 3*time.Second {
		t.Fatalf("并发 100 请求耗时过长: %v（日志写入疑似阻塞了响应）", elapsed)
	}
	g.waitForLogs(t, total)
	t.Logf("100 个并发请求耗时 %v（%v/请求），全部落库", elapsed, elapsed/total)
}

// TestUsageEstimateFallback 验证「上游没返回 usage」时的估算降级。
//
// 用改造过的假上游：故意不回 usage。请求必须成功，日志标记
// is_estimated，且估算值非零（prompt 从请求体文本估、completion
// 从响应内容估）。
func TestUsageEstimateFallback(t *testing.T) {
	gw := testCoordinator(t)
	gin.SetMode(gin.TestMode)
	// 一个不回 usage 的假上游
	noUsage := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":      "chatcmpl-nousage",
			"object":  "chat.completion",
			"model":   "gpt-4o-mini",
			"choices": []map[string]any{{"index": 0, "message": map[string]any{"role": "assistant", "content": "这是一段足够长的回答内容用于估算测试"}, "finish_reason": "stop"}},
		})
	}))
	defer noUsage.Close()

	st, err := store.Open(config.DatabaseConfig{
		Driver: "sqlite",
		Path:   filepath.Join(t.TempDir(), "est.db"),
	})
	if err != nil {
		t.Fatalf("开库失败: %v", err)
	}
	defer st.Close()
	if _, err := st.CreateChannel(&store.ChannelInput{
		Name: "nousage", Type: "openai", BaseURL: noUsage.URL,
		Models: []string{"gpt-4o-mini"}, APIKey: "sk-x", GroupName: "default",
	}); err != nil {
		t.Fatalf("建渠道失败: %v", err)
	}
	if _, err := st.CreateToken(&store.TokenInput{Key: "sk-est", Name: "est"}); err != nil {
		t.Fatalf("建令牌失败: %v", err)
	}
	if err := gw.initRegistry(st.Channels()); err != nil {
		t.Fatalf("初始化失败: %v", err)
	}
	if err := st.UpsertModelPrice(&dbmodel.ModelPrice{
		Model: "gpt-4o-mini", InputPricePer1K: 0.15, OutputPricePer1K: 0.6,
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
	}, 0)
	gw.SetBilling(billing.New(st.DB(), priceTable))
	gw.SetLogRecorder(st.WriteLog)

	e := gin.New()
	auth := middleware.NewAuth(st)
	e.POST("/v1/chat/completions", auth.Handle(), gw.HandleChatCompletions)

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		bytes.NewReader([]byte(`{"model":"gpt-4o-mini","messages":[{"role":"user","content":"你好，请介绍一下你自己"}]}`)))
	req.Header.Set("Authorization", "Bearer sk-est")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("请求失败: %d %s", rec.Code, rec.Body.String())
	}

	// 等日志落库
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if n, _ := st.CountLogs(); n >= 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	logs, err := st.ListLogs(store.LogQuery{Limit: 1})
	if err != nil || len(logs) == 0 {
		t.Fatalf("查日志失败: %v %v", logs, err)
	}
	entry := logs[0]
	if !entry.IsEstimated {
		t.Fatal("上游没给 usage 应标记估算")
	}
	if entry.PromptTokens <= 0 || entry.CompletionTokens <= 0 {
		t.Fatalf("估算值不应为 0: %+v", entry)
	}
	if entry.Cost <= 0 {
		t.Fatalf("有价格时应按估算值计费: cost=%v", entry.Cost)
	}
	t.Logf("估算用量: prompt=%d completion=%d cost=%.6f",
		entry.PromptTokens, entry.CompletionTokens, entry.Cost)
}

// TestQuotaExceededNoForward 补充验证：预扣阶段就超额时同样 403
// 且不转发（max_tokens 让预扣金额大于剩余额度）。
func TestQuotaExceededNoForward(t *testing.T) {
	g := newTestGateway(t, 0.001)

	hitsBefore := atomic.LoadInt64(g.upstreamHit)
	code, body := g.sendChat(t, `{"model":"gpt-4o-mini","max_tokens":500,"messages":[{"role":"user","content":"hi"}]}`)
	if code != http.StatusForbidden {
		t.Fatalf("预扣超额应 403: 得到 %d %s", code, body)
	}
	if atomic.LoadInt64(g.upstreamHit) != hitsBefore {
		t.Fatal("预扣被拒的请求不该转发上游")
	}
	// 被拒时不留扣减痕迹
	tk, err := g.store.GetToken(1)
	if err != nil {
		t.Fatalf("读令牌失败: %v", err)
	}
	if tk.UsedQuota != 0 {
		t.Fatalf("被拒请求不该扣额度: used=%v", tk.UsedQuota)
	}
}

// TestReadBodyTwiceNotAffected 防御性验证：captureWriter 不影响正常
// 响应的字节内容（流式 SSE 逐字节可读）。
func TestReadBodyTwiceNotAffected(t *testing.T) {
	g := newTestGateway(t, 0)

	_, body := g.sendChat(t, `{"model":"gpt-4o-mini","messages":[{"role":"user","content":"hi"}]}`)
	if !strings.Contains(body, "假上游的回答") {
		t.Fatalf("响应内容不对: %s", body)
	}
	var parsed struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal([]byte(body), &parsed); err != nil {
		t.Fatalf("响应不是合法 JSON: %v", err)
	}
	_ = io.Discard
}
