package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"minigateway/internal/access"
	"minigateway/internal/billing"
	"minigateway/internal/core"
	dbmodel "minigateway/internal/model"
	"minigateway/internal/protocol"
	"minigateway/internal/protocol/adaptor"
)

func closeFaultSocket(t *testing.T, w http.ResponseWriter, wire string) {
	t.Helper()
	conn, _, err := w.(http.Hijacker).Hijack()
	if err != nil {
		t.Error(err)
		return
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(time.Second))
	if wire != "" {
		if _, err := io.WriteString(conn, wire); err != nil {
			t.Error(err)
		}
	}
}

func TestTransportFaultMarkerAdapterCannotHideAcceptedReplay(t *testing.T) {
	for _, operation := range []protocol.Operation{protocol.Chat, protocol.Embeddings, protocol.Responses} {
		t.Run(string(operation), func(t *testing.T) {
			var hits, acquired, reused atomic.Int64
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				io.Copy(io.Discard, r.Body)
				if r.URL.Path == "/warm" {
					io.WriteString(w, `{"ok":true}`)
					return
				}
				if r.Header.Get("Idempotency-Key") == "" {
					t.Error("registered adapter marker was lost")
				}
				if hits.Add(1) == 1 {
					closeFaultSocket(t, w, "")
					return
				}
				wire := `{"choices":[],"usage":{"prompt_tokens":1,"total_tokens":1}}`
				if operation == protocol.Embeddings {
					wire = `{"data":[{"embedding":[0.1],"index":0}],"usage":{"prompt_tokens":1,"total_tokens":1}}`
				}
				if operation == protocol.Responses {
					wire = `{"id":"resp_provider","object":"response","status":"completed","output":[]}`
				}
				io.WriteString(w, wire)
			}))
			defer upstream.Close()
			client := upstream.Client()
			client.Timeout = 3 * time.Second
			warm, err := client.Post(upstream.URL+"/warm", "application/json", strings.NewReader(`{"warm":true}`))
			if err != nil {
				t.Fatal(err)
			}
			io.Copy(io.Discard, warm.Body)
			warm.Body.Close()
			g, err := newRegisteredGateway(Options{Channels: lifecycleChannels(upstream.URL), Client: client, RetryTimes: 1, BillingMode: billing.ModeDisabled,
				Protocols: protocol.Protocols(map[string]adaptor.Adaptor{"openai": &tariffHeaderAdaptor{header: "Idempotency-Key"}})})
			if err != nil {
				t.Fatal(err)
			}
			defer g.Close()
			ctx := httptrace.WithClientTrace(outputIdentity(), &httptrace.ClientTrace{GotConn: func(info httptrace.GotConnInfo) {
				acquired.Add(1)
				if info.Reused {
					reused.Add(1)
				}
			}})
			body := `{"model":"m","messages":[]}`
			if operation == protocol.Embeddings || operation == protocol.Responses {
				body = `{"model":"m","input":"hello"}`
			}
			out := &observerOutput{}
			result := g.ExecuteTo(ctx, out, Execution{Operation: operation, Body: []byte(body), ResponseID: "resp_local"})
			count := 2
			if operation == protocol.Responses {
				count = 1
			}
			if hits.Load() != int64(count) || acquired.Load() != int64(count) || reused.Load() != 1 || len(result.Attempts) != count || result.Attempts[0].Phase != core.Dispatching || result.Attempts[0].Committed {
				t.Fatalf("registered adapter bypassed attempt accounting: hits=%d acquired=%d reused=%d result=%+v", hits.Load(), acquired.Load(), reused.Load(), result)
			}
			if operation == protocol.Responses {
				if result.Error == nil || result.ProtocolComplete {
					t.Fatalf("nonreplayable response succeeded: %+v", result)
				}
			} else if result.Error != nil || !result.ProtocolComplete {
				t.Fatalf("authorized L1 retry failed: %+v", result)
			}
		})
	}
}

func TestTransportFaultsKeepOperationRetryAndCommitFacts(t *testing.T) {
	for _, operation := range []protocol.Operation{protocol.Chat, protocol.Embeddings, protocol.Responses} {
		for _, fault := range []string{"accepted-no-headers", "json-truncated", "sse-no-event", "sse-after-event", "429", "503"} {
			stream := strings.HasPrefix(fault, "sse-")
			if operation == protocol.Embeddings && stream {
				continue
			}
			t.Run(string(operation)+"/"+fault, func(t *testing.T) {
				body := fmt.Sprintf(`{"model":"m","messages":[{"role":"user","content":"hello"}],"stream":%v}`, stream)
				complete := `{"choices":[],"usage":{"prompt_tokens":2,"completion_tokens":1,"total_tokens":3}}`
				if operation == protocol.Embeddings {
					body = `{"model":"m","input":"hello"}`
					complete = `{"data":[{"embedding":[0.1],"index":0}],"usage":{"prompt_tokens":2,"total_tokens":2}}`
				}
				if operation == protocol.Responses {
					body = fmt.Sprintf(`{"model":"m","input":"hello","store":false,"stream":%v}`, stream)
					complete = `{"id":"resp_provider","object":"response","status":"completed","output":[]}`
				}
				partial, terminal := p0SocketFrames(operation)
				if stream {
					complete = partial + terminal
				}
				var hits, leases atomic.Int64
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					input, err := io.ReadAll(r.Body)
					if err != nil {
						t.Error(err)
						return
					}
					if operation == protocol.Responses {
						// Responses owns a normalized independent model; it does not
						// promise the Chat fast path's JSON object-key byte order.
						var actual, expected any
						if json.Unmarshal(input, &actual) != nil || json.Unmarshal([]byte(body), &expected) != nil || !reflect.DeepEqual(actual, expected) {
							t.Errorf("Responses semantics changed: %s", input)
						}
					} else if string(input) != body {
						t.Errorf("attempt body changed: %s", input)
					}
					if hits.Add(1) == 1 {
						switch fault {
						case "accepted-no-headers":
							closeFaultSocket(t, w, "")
							return
						case "json-truncated":
							closeFaultSocket(t, w, "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: 500\r\n\r\n{\"partial\":")
							return
						case "sse-no-event":
							w.Header().Set("Content-Type", "text/event-stream")
							w.WriteHeader(200)
							w.(http.Flusher).Flush()
							return
						case "sse-after-event":
							w.Header().Set("Content-Type", "text/event-stream")
							io.WriteString(w, partial)
							w.(http.Flusher).Flush()
							return
						case "429", "503":
							status := 429
							if fault == "503" {
								status = 503
							}
							w.WriteHeader(status)
							io.WriteString(w, `{"error":{"message":"retry fixture"}}`)
							return
						}
					}
					if stream {
						w.Header().Set("Content-Type", "text/event-stream")
					}
					io.WriteString(w, complete)
				}))
				defer upstream.Close()
				g, err := newRegisteredGateway(Options{Channels: lifecycleChannels(upstream.URL), BillingMode: billing.ModeDisabled, RetryTimes: 1, TotalTimeout: 3 * time.Second, Admission: admissionFunc(func(context.Context, AdmissionScope) (func(), error) {
					leases.Add(1)
					return func() { leases.Add(-1) }, nil
				})})
				if err != nil {
					t.Fatal(err)
				}
				defer g.Close()
				out := httptest.NewRecorder()
				result := g.ExecuteTo(outputIdentity(), core.NewHTTPOutput(out), Execution{Operation: operation, ResponseID: "resp_local", Body: []byte(body)})
				wantAttempts := 1
				canRetry := operation != protocol.Responses && fault != "sse-after-event"
				if canRetry {
					wantAttempts = 2
				}
				if len(result.Attempts) != wantAttempts || hits.Load() != int64(wantAttempts) || leases.Load() != 0 {
					t.Fatalf("send/attempt/lease mismatch: hits=%d leases=%d result=%+v", hits.Load(), leases.Load(), result)
				}
				first := result.Attempts[0]
				phase := core.ResponseReceived
				if fault == "accepted-no-headers" {
					phase = core.Dispatching
				}
				if first.Phase != phase || first.ProtocolComplete || first.Replayable != (operation != protocol.Responses) || first.Committed != (fault == "sse-after-event") {
					t.Fatalf("first failure facts changed: %+v", first)
				}
				if canRetry {
					if result.Error != nil || !result.ProtocolComplete || out.Code != 200 || result.Attempts[1].CredentialID == first.CredentialID {
						t.Fatalf("authorized retry failed/reused credential: status=%d result=%+v", out.Code, result)
					}
				} else {
					if result.Error == nil || result.ProtocolComplete || strings.Contains(out.Body.String(), "[DONE]") || strings.Contains(out.Body.String(), "response.completed") {
						t.Fatalf("failure fabricated completion: status=%d result=%+v body=%s", out.Code, result, out.Body.String())
					}
					if fault == "sse-after-event" && (!result.Committed || out.Code != 200) {
						t.Fatalf("committed status changed: %+v status=%d", result, out.Code)
					}
				}
			})
		}
	}
}

func TestTransportFaultStrictLostResponseAndAuthorizedRetryRemainSeparate(t *testing.T) {
	for _, retry := range []int{0, 1} {
		t.Run(fmt.Sprintf("retries=%d", retry), func(t *testing.T) {
			fixture := newTestGateway(t, 0)
			st := fixture.store
			var hits, leases, acquired, reused atomic.Int64
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, err := io.ReadAll(r.Body)
				if err != nil {
					t.Error(err)
					return
				}
				if r.Header.Get("X-Fixture-Warm") == "true" {
					io.WriteString(w, `{"ok":true}`)
					return
				}
				if !strings.Contains(string(body), `"model":"gpt-4o-mini-2024-07-18"`) {
					t.Errorf("unverified wire: %s", body)
				}
				if hits.Add(1) == 1 {
					closeFaultSocket(t, w, "")
					return
				}
				io.WriteString(w, `{"choices":[],"usage":{"prompt_tokens":2,"completion_tokens":1,"total_tokens":3}}`)
			}))
			defer upstream.Close()
			channels, client := verifiedChatChannels(t, st.Channels(), upstream.URL)
			channels[0].Credentials = append(channels[0].Credentials, dbmodel.Credential{ID: 1001, APIKey: "fixture-second-credential", Status: dbmodel.CredentialActive, Weight: 1})
			engine := billing.New(st.DB(), billing.NewPriceTableWithHistory(st.PriceData, time.Hour, st.RecordPriceSnapshot))
			if err := engine.EnableStrict("20", time.Minute); err != nil {
				t.Fatal(err)
			}
			configureVerifiedChatCosts(t, engine, channels)
			warm, err := http.NewRequest(http.MethodPost, "https://api.openai.com/v1/chat/completions", strings.NewReader(`{"warm":true}`))
			if err != nil {
				t.Fatal(err)
			}
			warm.Header.Set("X-Fixture-Warm", "true")
			warmCtx, cancelWarm := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancelWarm()
			warm = warm.WithContext(warmCtx)
			resp, err := client.Do(warm)
			if err != nil {
				t.Fatal(err)
			}
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			g, err := newRegisteredGateway(Options{Channels: channels, Client: client, Billing: engine, BillingMode: billing.ModeStrict, RetryTimes: retry, TotalTimeout: 3 * time.Second, Admission: admissionFunc(func(context.Context, AdmissionScope) (func(), error) {
				leases.Add(1)
				return func() { leases.Add(-1) }, nil
			})})
			if err != nil {
				t.Fatal(err)
			}
			defer g.Close()
			token, err := st.GetToken(1)
			if err != nil {
				t.Fatal(err)
			}
			ctx := httptrace.WithClientTrace(context.Background(), &httptrace.ClientTrace{GotConn: func(info httptrace.GotConnInfo) {
				acquired.Add(1)
				if info.Reused {
					reused.Add(1)
				}
			}})
			ctx = access.WithPrincipal(ctx, access.Principal{UserID: token.UserID, TokenID: token.ID, Policy: access.Policy{Mode: access.All}})
			out := &observerOutput{}
			result := g.ExecuteTo(ctx, out, Execution{Operation: protocol.Chat, Body: []byte(`{"model":"gpt-4o-mini","messages":[{"role":"user","content":"hello"}],"max_completion_tokens":32,"service_tier":"default"}`)})
			count := retry + 1
			if hits.Load() != int64(count) || acquired.Load() != int64(count) || reused.Load() != 1 || len(result.Attempts) != count || leases.Load() != 0 || result.Attempts[0].Phase != core.Dispatching {
				t.Fatalf("retry execution facts: hits=%d acquired=%d reused=%d leases=%d result=%+v", hits.Load(), acquired.Load(), reused.Load(), leases.Load(), result)
			}
			var row dbmodel.BillingReservation
			if err := st.DB().First(&row, "id=?", out.metadata.Values["X-TXAPI-Billing-ID"]).Error; err != nil {
				t.Fatal(err)
			}
			var attempts []dbmodel.BillingAttempt
			if err := st.DB().Where("reservation_id=?", row.ID).Order("number").Find(&attempts).Error; err != nil {
				t.Fatal(err)
			}
			if len(attempts) != count || attempts[0].CostKnown || attempts[0].ProviderBoundUnits <= 0 {
				t.Fatalf("first unknown attempt vanished: %+v", attempts)
			}
			var sum int64
			for _, a := range attempts {
				sum += a.ProviderBoundUnits
				if a.State != "finished" || a.ProviderBoundUnits <= 0 {
					t.Fatalf("durable attempt incomplete: %+v", a)
				}
			}
			if row.ProviderHeldUnits != sum {
				t.Fatalf("cumulative provider hold lost: %+v %+v", row, attempts)
			}
			var balances []dbmodel.BillingBalance
			if err := st.DB().Find(&balances).Error; err != nil || len(balances) != 2 {
				t.Fatalf("dual balance facts: %+v %v", balances, err)
			}
			if retry == 0 {
				if result.Error == nil || result.ProtocolComplete || result.BillingState != billing.Pending || row.State != billing.Pending || row.ChargeUnits != 0 {
					t.Fatalf("unknown charged/finalized: %+v %+v", result, row)
				}
				for _, balance := range balances {
					if balance.ReservedUnits <= 0 || balance.UsedUnits != 0 {
						t.Fatalf("unknown dual hold released: %+v", balance)
					}
				}
			} else {
				if result.Error != nil || !result.ProtocolComplete || result.BillingState != billing.Settled || row.State != billing.Settled || row.ChargeUnits <= 0 || !attempts[1].CostKnown || attempts[1].CredentialID == attempts[0].CredentialID {
					t.Fatalf("known final answer settlement changed: %+v %+v %+v", result, row, attempts)
				}
				for _, balance := range balances {
					if balance.ReservedUnits != 0 || balance.UsedUnits != row.ChargeUnits {
						t.Fatalf("final-answer dual settlement changed: %+v", balance)
					}
				}
			}
		})
	}
}
