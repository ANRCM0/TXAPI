package gateway

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"minigateway/internal/access"
	"minigateway/internal/billing"
	"minigateway/internal/core"
	dbmodel "minigateway/internal/model"
	"minigateway/internal/protocol"
)

func TestHTTPStageTimeoutPreservesStrictUnknownHolds(t *testing.T) {
	for _, mode := range []core.TimeoutStage{core.TimeoutHeaders, core.TimeoutFirstEffectiveOutput, core.TimeoutStreamIdle} {
		t.Run(string(mode), func(t *testing.T) {
			fixture := newTestGateway(t, 0)
			st := fixture.store
			var hits, penalties atomic.Int64
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hits.Add(1)
				io.Copy(io.Discard, r.Body)
				if mode != core.TimeoutHeaders {
					w.Header().Set("Content-Type", "text/event-stream")
					w.WriteHeader(200)
					if mode == core.TimeoutStreamIdle {
						io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\n")
					}
					w.(http.Flusher).Flush()
				}
				<-r.Context().Done()
			}))
			defer upstream.Close()
			channels, client := verifiedChatChannels(t, st.Channels(), upstream.URL)
			engine := billing.New(st.DB(), billing.NewPriceTableWithHistory(st.PriceData, time.Hour, st.RecordPriceSnapshot))
			if err := engine.EnableStrict("20", time.Minute); err != nil {
				t.Fatal(err)
			}
			configureVerifiedChatCosts(t, engine, channels)
			stages := HTTPStageTimeouts{}
			switch mode {
			case core.TimeoutHeaders:
				stages.Headers = 100 * time.Millisecond
			case core.TimeoutFirstEffectiveOutput:
				stages.FirstEffectiveOutput = 100 * time.Millisecond
			case core.TimeoutStreamIdle:
				stages.StreamIdle = 100 * time.Millisecond
			}
			g, err := newRegisteredGateway(Options{Channels: channels, Client: client, Billing: engine, BillingMode: billing.ModeStrict, RetryTimes: 2, TotalTimeout: 2 * time.Second, HTTPStageTimeouts: stages, HealthReporter: func(*dbmodel.Credential, int, string) { penalties.Add(1) }})
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
			result := g.ExecuteTo(ctx, out, Execution{Operation: protocol.Chat, Body: []byte(`{"model":"gpt-4o-mini","max_completion_tokens":32,"service_tier":"default","stream":true,"messages":[{"role":"user","content":"hello"}]}`)})
			if hits.Load() != 1 || len(result.Attempts) != 1 || result.TimeoutStage != mode || result.ProtocolComplete || result.Termination != core.Deadline || penalties.Load() != 0 {
				t.Fatalf("timeout execution facts: hits=%d penalties=%d result=%+v", hits.Load(), penalties.Load(), result)
			}
			detail, err := engine.ReservationDetail(context.Background(), out.metadata.Values["X-TXAPI-Billing-ID"])
			if err != nil {
				t.Fatal(err)
			}
			if result.BillingState != billing.Pending || detail.Reservation.State != billing.Pending || detail.Reservation.ProviderHeldUnits == "0" || detail.Reservation.ChargeUnits != "0" || len(detail.Attempts) != 1 {
				t.Fatalf("unknown execution became free or charged: %+v %+v", result, detail)
			}
			var balances []dbmodel.BillingBalance
			if err := st.DB().Find(&balances).Error; err != nil || len(balances) != 2 {
				t.Fatalf("dual hold facts: %+v %v", balances, err)
			}
			for _, balance := range balances {
				if balance.ReservedUnits <= 0 || balance.UsedUnits != 0 {
					t.Fatalf("unknown budget released: %+v", balance)
				}
			}
		})
	}
}
