package gateway

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"minigateway/internal/access"
	"minigateway/internal/billing"
	"minigateway/internal/core"
	dbmodel "minigateway/internal/model"
	"minigateway/internal/protocol"
)

type legacyBillingProbe struct{ calls []string }

func (p *legacyBillingProbe) PreRequest(uint, uint, int, string) (*billing.PreDeduct, error) {
	p.calls = append(p.calls, "pre")
	return &billing.PreDeduct{}, nil
}
func (p *legacyBillingProbe) PriceOfSnapshot(*billing.PreDeduct, *core.Usage, string, string, string) (float64, bool) {
	p.calls = append(p.calls, "price")
	return 0, true
}
func (p *legacyBillingProbe) Settle(uint, uint, *billing.PreDeduct, float64) error {
	p.calls = append(p.calls, "settle")
	return nil
}

type forbiddenStrictBilling struct{ StrictBilling }

func (forbiddenStrictBilling) StrictReady() bool { panic("non-strict mode touched strict billing") }

func TestOptionalBillingPortsAreModeScopedAndNilSafe(t *testing.T) {
	for _, mode := range []billing.Mode{billing.ModeDisabled, billing.ModeLegacy, billing.ModeStrict} {
		for _, typedNil := range []bool{false, true} {
			t.Run(string(mode)+map[bool]string{false: "/ports", true: "/typed-nil"}[typedNil], func(t *testing.T) {
				legacy := &legacyBillingProbe{}
				opts := Options{Channels: lifecycleChannels("http://upstream.test"), BillingMode: mode, Billing: legacy}
				if mode != billing.ModeStrict {
					opts.StrictBilling = forbiddenStrictBilling{}
				}
				if typedNil {
					var engine *billing.Engine
					opts.Billing, opts.StrictBilling = engine, engine
				}
				hits := 0
				opts.Client = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
					hits++
					return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"choices":[],"usage":{"prompt_tokens":2,"completion_tokens":1,"total_tokens":3}}`))}, nil
				})}
				g, err := New(opts)
				if err != nil {
					t.Fatal(err)
				}
				defer g.Close()
				result := g.ExecuteTo(outputIdentity(), &observerOutput{}, Execution{Operation: protocol.Chat, Body: []byte(`{"model":"m"}`)})
				if mode == billing.ModeStrict {
					if result.StatusCode != 503 || hits != 0 || len(result.Attempts) != 0 || len(legacy.calls) != 0 {
						t.Fatalf("strict fell back to optional legacy provider: %+v calls=%v", result, legacy.calls)
					}
				} else if result.StatusCode != 200 || hits != 1 {
					t.Fatalf("optional provider broke execution: %+v", result)
				}
				var want []string
				if mode == billing.ModeLegacy && !typedNil {
					want = []string{"pre", "price", "settle"}
				}
				if !reflect.DeepEqual(legacy.calls, want) {
					t.Fatalf("billing mode crossed ports: got %v want %v", legacy.calls, want)
				}
			})
		}
	}
}

// Implements only the strict consumer port, delegating financial authority to
// the real Engine. Fault injection never grants unprofiled production attempts.
type strictBillingProbe struct {
	StrictBilling
	calls      []string
	failEnd    bool
	wireBody   []byte
	wireHeader http.Header
}

func (p *strictBillingProbe) Reserve(ctx context.Context, tokenID, userID uint, model string) (*billing.PreDeduct, error) {
	p.calls = append(p.calls, "reserve")
	return p.StrictBilling.Reserve(ctx, tokenID, userID, model)
}
func (p *strictBillingProbe) StartPreparedAttempt(ctx context.Context, pre *billing.PreDeduct, attempt dbmodel.BillingAttempt, target billing.ProviderTarget, request billing.StrictRequest, url string, body []byte, header http.Header) error {
	p.calls = append(p.calls, "start")
	p.wireBody, p.wireHeader = bytes.Clone(body), header.Clone()
	err := p.StrictBilling.StartPreparedAttempt(ctx, pre, attempt, target, request, url, body, header)
	// A port may retain or reuse its input after admission. It must not change
	// the request that was approved and will actually be sent.
	body[len(body)-1] = '!'
	header.Set("Authorization", "Bearer changed-after-admission")
	return err
}
func (p *strictBillingProbe) EndAttempt(ctx context.Context, pre *billing.PreDeduct, number, status int, termination string, usage *core.Usage) error {
	p.calls = append(p.calls, "end")
	if p.failEnd {
		return errors.New("injected receipt persistence failure")
	}
	return p.StrictBilling.EndAttempt(ctx, pre, number, status, termination, usage)
}
func (p *strictBillingProbe) Finish(ctx context.Context, pre *billing.PreDeduct, usage *core.Usage, model, group string, complete bool) (int64, string, error) {
	p.calls = append(p.calls, "finish")
	return p.StrictBilling.Finish(ctx, pre, usage, model, group, complete)
}

func TestStrictOnlyPortPreservesDurableFinalization(t *testing.T) {
	for _, failEnd := range []bool{false, true} {
		t.Run(map[bool]string{false: "settlement", true: "failed_receipt"}[failEnd], func(t *testing.T) {
			fixture := newTestGateway(t, 0)
			st := fixture.store
			channels, client := verifiedChatChannels(t, st.Channels(), fixture.upstream.URL)
			engine := billing.New(st.DB(), billing.NewPriceTableWithHistory(st.PriceData, time.Hour, st.RecordPriceSnapshot))
			if err := engine.EnableStrict("20", time.Minute); err != nil {
				t.Fatal(err)
			}
			configureVerifiedChatCosts(t, engine, channels)
			port := &strictBillingProbe{StrictBilling: engine, failEnd: failEnd}
			transport := client.Transport
			client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
				port.calls = append(port.calls, "dispatch")
				copy, err := r.GetBody()
				if err != nil {
					return nil, err
				}
				body, err := io.ReadAll(copy)
				copy.Close()
				if err != nil {
					return nil, err
				}
				if !bytes.Equal(body, port.wireBody) || r.Header.Get("Authorization") != port.wireHeader.Get("Authorization") {
					t.Error("billing provider changed the actual wire after cost admission")
				}
				return transport.RoundTrip(r)
			})
			g, err := New(Options{Channels: channels, Client: client, StrictBilling: port, BillingMode: billing.ModeStrict, RetryTimes: 2})
			if err != nil {
				t.Fatal(err)
			}
			defer g.Close()
			token, err := st.GetToken(1)
			if err != nil {
				t.Fatal(err)
			}
			ctx := access.WithPrincipal(context.Background(), access.Principal{UserID: token.UserID, TokenID: token.ID, Policy: access.Policy{Mode: access.All}})
			output := &observerOutput{}
			result := g.ExecuteTo(ctx, output, Execution{Operation: protocol.Chat, Body: []byte(`{"model":"gpt-4o-mini","max_completion_tokens":32,"service_tier":"default","messages":[{"role":"user","content":"hello"}]}`)})
			if !reflect.DeepEqual(port.calls, []string{"reserve", "start", "dispatch", "end", "finish"}) || len(result.Attempts) != 1 {
				t.Fatalf("split/retried financial lifecycle: %+v %v", result, port.calls)
			}
			detail, err := engine.ReservationDetail(context.Background(), output.metadata.Values["X-TXAPI-Billing-ID"])
			if err != nil {
				t.Fatal(err)
			}
			var balances []dbmodel.BillingBalance
			if err := st.DB().Find(&balances).Error; err != nil || len(balances) != 2 {
				t.Fatalf("missing dual balances: %+v %v", balances, err)
			}
			wantState := billing.Settled
			if failEnd {
				wantState = billing.Pending
				if result.StatusCode != 503 || detail.Reservation.ProviderHeldUnits == "0" || result.Termination == core.Success {
					t.Fatalf("unknown receipt lost execution facts: %+v %+v", result, detail)
				}
			} else if result.StatusCode != 200 || detail.Reservation.ChargeUnits == "0" {
				t.Fatalf("strict-only provider not settled: %+v %+v", result, detail)
			}
			if result.BillingState != wantState || detail.Reservation.State != wantState || len(detail.Attempts) != 1 {
				t.Fatalf("wrong durable state: %+v %+v", result, detail)
			}
			for _, balance := range balances {
				if failEnd && (balance.ReservedUnits <= 0 || balance.UsedUnits != 0) || !failEnd && (balance.ReservedUnits != 0 || balance.UsedUnits <= 0) {
					t.Fatalf("incorrect financial projection: %+v", balance)
				}
			}
		})
	}
}
