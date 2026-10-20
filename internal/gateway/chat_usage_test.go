package gateway

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"minigateway/internal/access"
	"minigateway/internal/billing"
	"minigateway/internal/core"
	v2 "minigateway/internal/foundation/contracts/execution/v2"
	dbmodel "minigateway/internal/model"
	"minigateway/internal/protocol"
)

func TestNativeChatUsageRecordingOverBothSockets(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, tc := range []struct {
			name, usage string
			state       v2.Completeness
		}{
			{"complete", `{"prompt_tokens":10,"completion_tokens":4,"total_tokens":14,"prompt_tokens_details":{"cached_tokens":2},"completion_tokens_details":{"reasoning_tokens":3}}`, v2.Complete},
			{"zero", `{"prompt_tokens":0,"completion_tokens":0,"total_tokens":0,"prompt_tokens_details":{"cached_tokens":0},"completion_tokens_details":{"reasoning_tokens":0}}`, v2.Complete},
			{"partial", `{"prompt_tokens":10,"completion_tokens":4}`, v2.Partial},
			{"detail only", `{"prompt_tokens_details":{"cached_tokens":0}}`, v2.Partial},
			{"unknown", `null`, v2.Unknown},
		} {
			t.Run(fmt.Sprintf("%s/stream=%v", tc.name, stream), func(t *testing.T) {
				var sends atomic.Int32
				closed := make(chan struct{}, 1)
				wire := `{"id":"native-chat","choices":[{"message":{"role":"assistant","content":"snow 雪"},"finish_reason":"stop"}],"usage":` + tc.usage + `,"extension":{"unmodeled":true}}`
				if stream {
					wire = "data: {\"choices\":[{\"delta\":{\"content\":\"snow 雪\"}}]}\n\n" +
						"data: {\"choices\":[{\"finish_reason\":\"stop\"}],\"usage\":" + tc.usage + "}\n\ndata: [DONE]\n\n"
				}
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					defer func() { closed <- struct{}{} }()
					_, _ = io.Copy(io.Discard, r.Body)
					sends.Add(1)
					w.Header().Set("Content-Type", "application/json")
					if stream {
						w.Header().Set("Content-Type", "text/event-stream")
					}
					_, _ = io.WriteString(w, wire)
					if stream {
						_ = http.NewResponseController(w).Flush()
						select {
						case <-r.Context().Done():
						case <-time.After(3 * time.Second):
							t.Error("validated DONE did not release upstream")
						}
					}
				}))
				defer upstream.Close()
				probe := &historyRecorderProbe{}
				gateway := newP0SocketGatewayWithOptions(t, upstream.URL, protocol.Chat, func(options *Options) { options.UsageHistory = probe })
				request := fmt.Sprintf(`{"model":"m","messages":[{"role":"user","content":"hello"}],"stream":%v}`, stream)
				response, err := gateway.server.Client().Post(gateway.server.URL+"/test", "application/json", strings.NewReader(request))
				if err != nil {
					t.Fatal(err)
				}
				data, readErr := io.ReadAll(response.Body)
				_ = response.Body.Close()
				result := gateway.result(t)
				if readErr != nil || response.StatusCode != 200 || string(data) != wire || !result.ProtocolComplete || result.RecordingState != "sealed" || sends.Load() != 1 {
					t.Fatalf("delivery/recording changed: %+v response=%d wire=%q err=%v", result, response.StatusCode, data, readErr)
				}
				if response.Header.Get("X-TXAPI-Usage-ID") != result.UsageRecordID || len(probe.receipts) != 1 || len(probe.finishes) != 1 || probe.starts[0].ID != result.UsageRecordID {
					t.Fatal("receipt identity or attempt mismatch", result, probe.events)
				}
				snapshot := probe.receipts[0].Usage
				if snapshot.Completeness != tc.state || snapshot.Breakdown.CacheWriteInputTokens != nil {
					t.Fatal("native completeness or unsupported cache-write changed", snapshot)
				}
				if tc.name == "complete" && (*snapshot.Counts.TotalTokens != 14 || *snapshot.Breakdown.CachedInputTokens != 2 || *snapshot.Breakdown.ReasoningOutputTokens != 3 || result.Usage == nil || result.Usage.TotalTokens != 14) {
					t.Fatal("complete usage not projected once", snapshot, result.Usage)
				}
				if tc.name == "zero" && (snapshot.Counts.TotalTokens == nil || *snapshot.Counts.TotalTokens != 0 || snapshot.Breakdown.CachedInputTokens == nil || *snapshot.Breakdown.CachedInputTokens != 0 || result.Usage == nil || result.Usage.TotalTokens != 0) {
					t.Fatal("native zero lost", snapshot, result.Usage)
				}
				if tc.name == "partial" && (snapshot.Counts.TotalTokens != nil || result.Usage != nil) {
					t.Fatal("partial observation became billable complete usage", snapshot, result.Usage)
				}
				if tc.name == "detail only" && (snapshot.Counts.InputTokens != nil || snapshot.Breakdown.CachedInputTokens == nil || *snapshot.Breakdown.CachedInputTokens != 0 || result.Usage != nil) {
					t.Fatal("detail-only observation inferred input or charge", snapshot, result.Usage)
				}
				if tc.name == "unknown" && (snapshot.Source != v2.SourceUnknown || snapshot.Counts.TotalTokens != nil || result.Usage != nil) {
					t.Fatal("unknown usage estimated or zeroed", snapshot, result.Usage)
				}
				select {
				case <-closed:
				case <-time.After(time.Second):
					t.Fatal("upstream handler not joined")
				}
			})
		}
	}
}

func TestNativeChatUsageKeepsLastValidObservationWithoutInventingTerminal(t *testing.T) {
	for _, scenario := range []string{"decrease", "truncate", "cancel", "null"} {
		t.Run(scenario, func(t *testing.T) {
			var sends atomic.Int32
			upstreamDone := make(chan struct{})
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				defer close(upstreamDone)
				_, _ = io.Copy(io.Discard, r.Body)
				sends.Add(1)
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"visible\"}}],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":4,\"total_tokens\":14}}\n\n")
				_ = http.NewResponseController(w).Flush()
				switch scenario {
				case "decrease":
					_, _ = io.WriteString(w, "data: {\"usage\":{\"prompt_tokens\":9,\"completion_tokens\":4,\"total_tokens\":13}}\n\n")
				case "null":
					_, _ = io.WriteString(w, "data: {\"usage\":null,\"choices\":[{\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
				case "cancel":
					select {
					case <-r.Context().Done():
					case <-time.After(3 * time.Second):
						t.Error("cancellation did not release upstream")
					}
				}
			}))
			defer upstream.Close()
			probe := &historyRecorderProbe{}
			gateway := newP0SocketGatewayWithOptions(t, upstream.URL, protocol.Chat, func(options *Options) { options.UsageHistory = probe })
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			response := gateway.request(t, ctx, protocol.Chat)
			if scenario == "cancel" {
				one := make([]byte, 1)
				if _, err := io.ReadFull(response.Body, one); err != nil {
					t.Fatal(err)
				}
				cancel()
			}
			data, _ := io.ReadAll(response.Body)
			_ = response.Body.Close()
			result := gateway.result(t)
			if sends.Load() != 1 || result.RecordingState != "sealed" || len(probe.receipts) != 1 || len(probe.finishes) != 1 || result.Usage != nil {
				t.Fatal("interruption replayed or manufactured aggregate charge", result, probe.events, sends.Load())
			}
			if scenario == "null" {
				if !result.ProtocolComplete || result.Termination != core.Success || probe.receipts[0].Usage.Completeness != v2.Unknown || strings.Count(string(data), "[DONE]") != 1 {
					t.Fatal("explicit unknown snapshot inherited old complete usage", result, probe.receipts)
				}
			} else if result.ProtocolComplete || strings.Contains(string(data), "[DONE]") || probe.receipts[0].Usage.Counts.TotalTokens == nil || *probe.receipts[0].Usage.Counts.TotalTokens != 14 {
				t.Fatal("interrupted native usage lost facts or acquired terminal", result, probe.receipts, string(data))
			}
			select {
			case <-upstreamDone:
			case <-time.After(time.Second):
				t.Fatal("interrupted upstream not joined")
			}
		})
	}
}

func TestNativeChatUsageStrictSettlementAndUnknownDualHolds(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, tc := range []struct {
			name, usage string
			known       bool
			charge      int64
		}{
			{"complete", `{"prompt_tokens":11,"completion_tokens":7,"total_tokens":18}`, true, 5_850_000},
			{"zero", `{"prompt_tokens":0,"completion_tokens":0,"total_tokens":0}`, true, 0},
			{"partial", `{"prompt_tokens":11,"completion_tokens":7}`, false, 0},
			{"detail only", `{"prompt_tokens_details":{"cached_tokens":0}}`, false, 0},
			{"unknown", `null`, false, 0},
			{"invalid", `{"prompt_tokens":11,"completion_tokens":7,"total_tokens":18,"completion_tokens_details":{"reasoning_tokens":8}}`, false, 0},
		} {
			t.Run(fmt.Sprintf("%s/stream=%v", tc.name, stream), func(t *testing.T) {
				fixture := newTestGateway(t, 0)
				st := fixture.store
				var hits atomic.Int64
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					_, _ = io.Copy(io.Discard, r.Body)
					hits.Add(1)
					wire := `{"choices":[{"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],"usage":` + tc.usage + `}`
					w.Header().Set("Content-Type", "application/json")
					if stream {
						w.Header().Set("Content-Type", "text/event-stream")
						wire = "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\ndata: {\"choices\":[{\"finish_reason\":\"stop\"}],\"usage\":" + tc.usage + "}\n\ndata: [DONE]\n\n"
					}
					_, _ = io.WriteString(w, wire)
				}))
				defer upstream.Close()
				channels, client := verifiedChatChannels(t, st.Channels(), upstream.URL)
				engine := billing.New(st.DB(), billing.NewPriceTableWithHistory(st.PriceData, time.Hour, st.RecordPriceSnapshot))
				if err := engine.EnableStrict("20", time.Minute); err != nil {
					t.Fatal(err)
				}
				configureVerifiedChatCosts(t, engine, channels)
				g, err := newRegisteredGateway(Options{Channels: channels, Client: client, Billing: engine, BillingMode: billing.ModeStrict, RetryTimes: 2})
				if err != nil {
					t.Fatal(err)
				}
				defer g.Close()
				token, err := st.GetToken(1)
				if err != nil {
					t.Fatal(err)
				}
				ctx := access.WithPrincipal(context.Background(), access.Principal{UserID: token.UserID, TokenID: token.ID, Policy: access.Policy{Mode: access.All}})
				out := &observerOutput{}
				body := fmt.Sprintf(`{"model":"gpt-4o-mini","max_completion_tokens":32,"service_tier":"default","stream":%v,"messages":[{"role":"user","content":"hello"}]}`, stream)
				result := g.ExecuteTo(ctx, out, Execution{Operation: protocol.Chat, Body: []byte(body)})
				if hits.Load() != 1 || len(result.Attempts) != 1 || result.ProtocolComplete != (tc.name != "invalid") {
					t.Fatal("native observation changed attempt/terminal facts", result, hits.Load())
				}
				var reservation dbmodel.BillingReservation
				if err := st.DB().First(&reservation, "id=?", out.metadata.Values["X-TXAPI-Billing-ID"]).Error; err != nil {
					t.Fatal(err)
				}
				wantState, wantReserved := billing.Pending, int64(20*billing.UnitScale)
				if tc.known {
					wantState, wantReserved = billing.Settled, 0
				}
				if result.BillingState != wantState || reservation.State != wantState || reservation.ChargeUnits != tc.charge || reservation.ProviderHeldUnits <= 0 {
					t.Fatal("unknown observation became free or changed historical cost proof", result, reservation)
				}
				var attempts []dbmodel.BillingAttempt
				if err := st.DB().Where("reservation_id=?", reservation.ID).Find(&attempts).Error; err != nil || len(attempts) != 1 || attempts[0].CostKnown != tc.known {
					t.Fatal("native details fabricated a known financial receipt", attempts, err)
				}
				var balances []dbmodel.BillingBalance
				if err := st.DB().Find(&balances).Error; err != nil || len(balances) != 2 {
					t.Fatal("missing dual balances", balances, err)
				}
				for _, balance := range balances {
					if balance.ReservedUnits != wantReserved || balance.UsedUnits != tc.charge {
						t.Fatal("observation released unknown hold or charged twice", balance)
					}
				}
			})
		}
	}
}
