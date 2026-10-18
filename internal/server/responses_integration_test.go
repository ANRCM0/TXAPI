package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"minigateway/internal/access"
	"minigateway/internal/billing"
	"minigateway/internal/config"
	"minigateway/internal/gateway"
	"minigateway/internal/generation"
	"minigateway/internal/model"
	responsesapp "minigateway/internal/responses"
	"minigateway/internal/server/middleware"
	"minigateway/internal/store"
)

type responsesBillingCounter struct {
	*billing.Engine
	pre, settled atomic.Int64
}

func (p *responsesBillingCounter) PreRequest(tokenID, userID uint, max int, name string) (*billing.PreDeduct, error) {
	p.pre.Add(1)
	return p.Engine.PreRequest(tokenID, userID, max, name)
}
func (p *responsesBillingCounter) Settle(tokenID, userID uint, pre *billing.PreDeduct, actual float64) error {
	p.settled.Add(1)
	return p.Engine.Settle(tokenID, userID, pre, actual)
}

func TestResponsesHTTPNativeJSONSSEAndDurableIdempotency(t *testing.T) {
	for _, mode := range []billing.Mode{billing.ModeDisabled, billing.ModeLegacy} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%v", mode, stream), func(t *testing.T) {
				var hits atomic.Int64
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					hits.Add(1)
					if r.URL.Path != "/v1/responses" || r.Header.Get("Authorization") != "Bearer upstream-responses-fixture" {
						http.Error(w, "wrong native target", 400)
						return
					}
					var input struct {
						Model        string          `json:"model"`
						Stream       bool            `json:"stream"`
						Input        json.RawMessage `json:"input"`
						Parent       string          `json:"previous_response_id"`
						Conversation json.RawMessage `json:"conversation"`
					}
					if json.NewDecoder(r.Body).Decode(&input) != nil || input.Model != "native-model" || input.Stream != stream {
						http.Error(w, "wrong native payload", 400)
						return
					}
					if hits.Load() == 2 && (input.Parent != "" || len(input.Conversation) != 0 || !bytes.Contains(input.Input, []byte("hello")) || !bytes.Contains(input.Input, []byte("native output")) || !bytes.Contains(input.Input, []byte("follow-up"))) {
						http.Error(w, "context must have one local source", 400)
						return
					}
					response := fmt.Sprintf(`{"id":"resp_upstream_original","object":"response","model":"native-model","status":"completed","output":[{"type":"message","id":"msg_%d","role":"assistant","content":[{"type":"output_text","text":"native output"}]}],"usage":{"input_tokens":2,"output_tokens":1,"total_tokens":3}}`, hits.Load())
					if stream {
						w.Header().Set("Content-Type", "text/event-stream")
						for sequence := range 320 {
							fmt.Fprintf(w, "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"sequence_number\":%d,\"delta\":\"x\"}\n\n", sequence)
						}
						fmt.Fprintf(w, "event: response.completed\ndata: {\"type\":\"response.completed\",\"sequence_number\":320,\"response\":%s}\n\n", response)
					} else {
						w.Header().Set("Content-Type", "application/json")
						io.WriteString(w, response)
					}
				}))
				t.Cleanup(upstream.Close)
				st, err := store.Open(config.DatabaseConfig{Driver: "sqlite", Path: filepath.Join(t.TempDir(), "responses.db")})
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = st.Close() })
				user, err := st.CreateUser(&store.UserInput{Username: "responses-user", Password: "responses-fixture-password", ModelMode: access.Allowlist, Models: []string{"public-model"}, Quota: 1000})
				if err != nil {
					t.Fatal(err)
				}
				key, err := st.IssueToken(user.ID, &store.TokenInput{Name: "responses-key", ModelMode: access.Allowlist, Models: []string{"public-model"}, RPMLimit: 1, Quota: 1000})
				if err != nil {
					t.Fatal(err)
				}
				_, err = st.CreateChannel(&store.ChannelInput{Name: "native", Type: "openai", BaseURL: upstream.URL, Models: []string{"public-model"}, ModelMapping: map[string]string{"public-model": "native-model"}, APIKey: "upstream-responses-fixture"})
				if err != nil {
					t.Fatal(err)
				}
				if err := st.UpsertModelPrice(&model.ModelPrice{Model: "public-model", InputPricePer1K: 1, OutputPricePer1K: 1}); err != nil {
					t.Fatal(err)
				}
				prices := billing.NewPriceTableWithHistory(st.PriceData, billing.DefaultPriceTTL, st.RecordPriceSnapshot)
				counter := &responsesBillingCounter{Engine: billing.New(st.DB(), prices)}
				limiter := middleware.NewMemoryLimiter()
				t.Cleanup(limiter.(interface{ Close() }).Close)
				gw, err := gateway.New(gateway.Options{BillingMode: mode, Billing: counter, Channels: st.Channels(), Limiter: limiter, LogRecorder: st.WriteLog})
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(gw.Close)
				archive, err := generation.NewArchive(generation.ArchiveOptions{Store: st, Resolve: st.ResolvePrincipal, ActiveKey: "fixture", Keys: map[string][]byte{"fixture": bytes.Repeat([]byte{23}, 32)}, Retention: time.Hour, MaxRecordBytes: 65536, Limits: model.GenerationArchiveLimits{InstanceBytes: 1 << 20, OwnerBytes: 1 << 19, InstanceRecords: 16, OwnerRecords: 8}})
				if err != nil {
					t.Fatal(err)
				}
				history, err := responsesapp.NewHistory(responsesapp.HistoryOptions{Store: st, ActiveKey: "fixture", Keys: map[string][]byte{"fixture": bytes.Repeat([]byte{23}, 32)}, Retention: time.Hour, Limits: model.HistoryLimits{InstanceRecords: 32, OwnerRecords: 16, InstanceBytes: 16 << 20, OwnerBytes: 8 << 20}})
				if err != nil {
					t.Fatal(err)
				}
				service, err := responsesapp.New(context.Background(), responsesapp.Options{History: history, Store: st, Executor: gw, Archive: archive, Resolve: st.ResolvePrincipal, DigestKey: bytes.Repeat([]byte{17}, 32), Retention: time.Hour, Limits: model.ResponseRequestLimits{InstanceRecords: 16, OwnerRecords: 8}, ActiveRequests: 4, OwnerRequests: 2})
				if err != nil {
					t.Fatal(err)
				}
				deps := Deps{Gateway: gw, Cfg: &config.Config{}, Store: st, Responses: service, RateLimiter: limiter, Billing: counter.Engine, AdminAuth: middleware.AdminAuth(testSecret)}
				server := httptest.NewServer(NewEngine(deps))
				t.Cleanup(server.Close)
				client := server.Client()
				client.Timeout = 5 * time.Second
				call := func(base, token, idempotency, body string, want int) (http.Header, []byte) {
					t.Helper()
					req, err := http.NewRequest("POST", base+"/v1/responses", strings.NewReader(body))
					if err != nil {
						t.Fatal(err)
					}
					req.Header.Set("Content-Type", "application/json")
					if token != "" {
						req.Header.Set("Authorization", "Bearer "+token)
					}
					if idempotency != "" {
						req.Header.Set("Idempotency-Key", idempotency)
					}
					res, err := client.Do(req)
					if err != nil {
						t.Fatal(err)
					}
					defer res.Body.Close()
					raw, err := io.ReadAll(res.Body)
					if err != nil {
						t.Fatal(err)
					}
					if res.StatusCode != want {
						t.Fatalf("Responses HTTP=%d want=%d", res.StatusCode, want)
					}
					return res.Header, raw
				}
				body := fmt.Sprintf(`{"model":"public-model","input":"hello","stream":%v,"max_output_tokens":16}`, stream)
				headers, first := call(server.URL, key.Key, "same-key", body, 200)
				publicID, generationID := headers.Get("X-TXAPI-Response-ID"), headers.Get("X-TXAPI-Generation-ID")
				if !strings.HasPrefix(publicID, "resp_") || generationID == "" || !bytes.Contains(first, []byte(publicID)) || bytes.Contains(first, []byte("resp_upstream_original")) || bytes.Contains(first, []byte("[DONE]")) {
					t.Fatal("native public identity/output contract lost")
				}
				if stream && bytes.Count(first, []byte("event: response.output_text.delta")) != 320 {
					t.Fatal("normal native SSE stream was truncated by the archive segment cap")
				}
				used, err := st.GetToken(key.ID)
				if err != nil {
					t.Fatal(err)
				}
				firstUsed := used.UsedQuota
				replayHeaders, replayed := call(server.URL, key.Key, "same-key", body, 200)
				used, err = st.GetToken(key.ID)
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(first, replayed) || replayHeaders.Get("X-TXAPI-Idempotency") != "replayed" || replayHeaders.Get("X-TXAPI-Response-ID") != publicID || hits.Load() != 1 || used.UsedQuota != firstUsed {
					t.Fatal("same-key delivery re-executed or changed output/billing")
				}
				wantCalls := int64(0)
				if mode == billing.ModeLegacy {
					wantCalls = 1
					if firstUsed <= 0 {
						t.Fatal("legacy fixture did not settle actual usage")
					}
				}
				if counter.pre.Load() != wantCalls || counter.settled.Load() != wantCalls {
					t.Fatal("replay duplicated legacy pre-reservation or settlement")
				}
				var receipt model.ResponseRequest
				if err := st.DB().First(&receipt, "id = ?", generationID).Error; err != nil {
					t.Fatal(err)
				}
				if receipt.ResponseID != publicID || receipt.UpstreamResponseID != "resp_upstream_original" || receipt.NativeStatus != "completed" || receipt.OutputRecordID == "" || receipt.State != model.ResponseCompleted {
					t.Fatalf("durable native receipt mismatch: %+v", receipt)
				}
				if stream {
					record, err := st.ReadGenerationRecord(context.Background(), access.Binding{UserID: user.ID, TokenID: key.ID}, receipt.OutputRecordID)
					if err != nil || record.State != "sealed" || len(record.Segments) <= 320 {
						t.Fatal("long SSE stream was not durably sealed and loaded")
					}
				}
				call(server.URL, key.Key, "same-key", strings.Replace(body, "hello", "changed", 1), 409)
				call(server.URL, key.Key, "new-key", body, 429)
				if hits.Load() != 1 {
					t.Fatal("conflict or RPM rejection reached upstream")
				}
				for _, path := range []string{"/v1/responses/" + publicID, "/v1/responses/" + publicID + "/input_items?limit=1"} {
					request, err := http.NewRequest("GET", server.URL+path, nil)
					if err != nil {
						t.Fatal(err)
					}
					request.Header.Set("Authorization", "Bearer "+key.Key)
					response, err := client.Do(request)
					if err != nil {
						t.Fatal(err)
					}
					raw, _ := io.ReadAll(response.Body)
					response.Body.Close()
					if response.StatusCode != 200 || !bytes.Contains(raw, []byte("hello")) && strings.Contains(path, "input_items") {
						t.Fatalf("history via real API %d %s", response.StatusCode, raw)
					}
				}
				if hits.Load() != 1 || counter.pre.Load() != wantCalls || counter.settled.Load() != wantCalls {
					t.Fatal("history reads consumed generation budget")
				}
				if err := st.DB().Model(&model.Token{}).Where("id = ?", key.ID).Update("rpm_limit", 0).Error; err != nil {
					t.Fatal(err)
				}
				childBody := fmt.Sprintf(`{"model":"public-model","input":"follow-up","previous_response_id":%q,"stream":%v,"max_output_tokens":16}`, publicID, stream)
				childHeaders, child := call(server.URL, key.Key, "child-key", childBody, 200)
				if childHeaders.Get("X-TXAPI-Response-ID") == publicID || !bytes.Contains(child, []byte(publicID)) || hits.Load() != 2 || counter.pre.Load() != 2*wantCalls || counter.settled.Load() != 2*wantCalls {
					t.Fatal("real native continuation lost identity or per-turn billing")
				}
				deps.Responses = nil
				disabled := httptest.NewServer(NewEngine(deps))
				t.Cleanup(disabled.Close)
				call(disabled.URL, key.Key, "disabled", body, 503)
				call(disabled.URL, "", "anonymous", body, 401)
			})
		}
	}
}
