package generation

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"minigateway/internal/billing"
	bg "minigateway/internal/foundation/contracts/background/v1"
	v1 "minigateway/internal/foundation/contracts/execution/v1"
	"minigateway/internal/gateway"
	bs "minigateway/internal/generation/backgroundstate"
	"minigateway/internal/model"
	"minigateway/internal/protocol"
	native "minigateway/internal/protocol/responses"
)

type unavailableBackgroundFinance struct {
	bs.Finance
	unavailable atomic.Bool
}

func (f *unavailableBackgroundFinance) CompleteBackgroundReservation(ctx context.Context, id string, key, user uint, name string, usage *v1.Usage, complete bool) error {
	if complete && f.unavailable.Load() {
		return errors.New("controlled financial persistence failure")
	}
	return f.Finance.CompleteBackgroundReservation(ctx, id, key, user, name, usage, complete)
}

func TestBackgroundStrictFinancialWriteFailureRetriesDurableReceiptWithoutAnotherRPC(t *testing.T) {
	var sends atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sends.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"resp_provider","object":"response","model":"m","status":"completed","output":[],"usage":{"input_tokens":2,"output_tokens":1,"total_tokens":3}}`)
	}))
	defer upstream.Close()
	f, engine := strictBackgroundFixture(t, upstream.URL)
	finance := &unavailableBackgroundFinance{Finance: engine}
	finance.unavailable.Store(true)
	f.s.finance = finance
	strictBackgroundSubmit(t, f, "finance-write-failure")
	if err := f.runner.runOne(context.Background()); err == nil {
		t.Fatal("financial write failure was hidden")
	}
	var row model.BackgroundJob
	f.st.DB().First(&row)
	if row.Stage != "terminal" || !row.ReplayPending || row.Usage.Completeness != "complete" {
		t.Fatalf("terminal financial receipt not retained: %+v", row)
	}
	finance.unavailable.Store(false)
	runStrictBackground(t, f)
	f.st.DB().First(&row)
	var hold model.BillingReservation
	f.st.DB().First(&hold, "id = ?", row.BillingID)
	if sends.Load() != 1 || row.ReplayPending || hold.State != billing.Settled || hold.ChargeUnits != 30_000 {
		t.Fatalf("finance recovery reissued/failed: %+v %+v sends=%d", row, hold, sends.Load())
	}
}

func strictBackgroundFixture(t *testing.T, upstream string) (bgRuntimeFixture, *billing.Engine) {
	t.Helper()
	f := bgRuntimeOpen(t, filepath.Join(t.TempDir(), "strict-background.db"), upstream, nil)
	if err := f.st.UpsertModelPrice(&model.ModelPrice{Model: "m", InputPricePer1K: 0.01, OutputPricePer1K: 0.01}); err != nil {
		t.Fatal(err)
	}
	engine := billing.New(f.st.DB(), billing.NewPriceTableWithHistory(f.st.PriceData, time.Hour, f.st.RecordPriceSnapshot))
	if err := engine.EnableStrict("1", 6*time.Minute); err != nil {
		t.Fatal(err)
	}
	ch := f.st.Channels()[0]
	cred := ch.Credentials[0]
	digest := sha256.Sum256([]byte(cred.APIKey))
	c := billing.ProviderCostContract{ChannelID: ch.ID, BaseURL: upstream, Protocol: "openai", ActualModel: "m", Operation: "responses", OutputLimitField: "max_output_tokens", InputTokenCeiling: 2048, InputBoundProfile: billing.OperatorResponsesV1, MaxOutputTokens: 32, InputPricePer1K: "0.001", OutputPricePer1K: "0.002", Evidence: "fixture all-token upper bounds and <=32 control RPC envelope", TariffProfile: billing.OperatorDeclaredTokensV1, Currency: "USD", CredentialBindings: map[string]string{fmt.Sprint(cred.ID): hex.EncodeToString(digest[:])}, PolicyVersion: "background-fixture-v1", PolicyExpiresAt: time.Now().Add(time.Hour).Unix(), MaxRequestBytes: 1024, AdditionalCostPerAttempt: "0.0001"}
	if err := engine.ConfigureProviderCosts("1", []billing.ProviderCostContract{c}); err != nil {
		t.Fatal(err)
	}
	f.s.mode, f.s.finance = "strict", engine
	g, err := gateway.New(gateway.Options{Protocols: protocol.Protocols(nil), Operations: protocol.Operations(), Channels: f.st.Channels(), BillingMode: billing.ModeStrict, Billing: engine})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(g.Close)
	remote, err := gateway.NewBackgroundRemote(g, gateway.BackgroundRemoteOptions{Registrations: []bg.Registration{native.BackgroundRegistration()}, Channels: map[uint]string{ch.ID: native.BackgroundRegistrationID}})
	if err != nil {
		t.Fatal(err)
	}
	f.remote = remote
	f.runner.opts.Remote = remote
	return f, engine
}

func strictBackgroundSubmit(t *testing.T, f bgRuntimeFixture, key string) bg.Receipt {
	t.Helper()
	receipt, err := f.s.Submit(f.ctx, bg.Submit{ID: "resp_" + strings.Repeat("9", 64), Operation: v1.Responses, Body: []byte(`{"model":"m","input":"hello","max_output_tokens":32,"background":true,"store":false,"stream":false}`), IdempotencyKey: key})
	if err != nil {
		t.Fatal(err)
	}
	return receipt
}

func runStrictBackground(t *testing.T, f bgRuntimeFixture) {
	t.Helper()
	if err := f.st.DB().Model(&model.BackgroundJob{}).Where("1 = 1").Update("due_at", 0).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.runner.runOne(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestBackgroundStrictDurableTerminalThenLateUsageAndRestartSettleOnce(t *testing.T) {
	var posts, gets atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Header.Get("Authorization") != "Bearer fixture-upstream-key" {
			t.Error("wrong bound credential")
		}
		if r.Method == http.MethodPost {
			posts.Add(1)
			_, _ = io.WriteString(w, `{"id":"resp_provider","object":"response","model":"m","status":"queued","output":[]}`)
			return
		}
		n := gets.Add(1)
		if n == 1 {
			_, _ = io.WriteString(w, `{"id":"resp_provider","object":"response","model":"m","status":"completed","output":[]}`)
			return
		}
		_, _ = io.WriteString(w, `{"id":"resp_provider","object":"response","model":"m","status":"completed","output":[],"usage":{"input_tokens":2,"output_tokens":1,"total_tokens":3}}`)
	}))
	defer upstream.Close()
	f, engine := strictBackgroundFixture(t, upstream.URL)
	first := strictBackgroundSubmit(t, f, "same-generation")
	repeated := strictBackgroundSubmit(t, f, "same-generation")
	if first.ID != repeated.ID {
		t.Fatal("duplicate task admitted")
	}
	var row model.BackgroundJob
	f.st.DB().First(&row)
	var reservations int64
	f.st.DB().Model(&model.BillingReservation{}).Where("state <> ?", billing.Settled).Count(&reservations)
	if reservations != 1 || row.BillingID == "" || row.BillingMode != "strict" {
		t.Fatalf("task/hold not linked: %+v %d", row, reservations)
	}
	runStrictBackground(t, f)
	runStrictBackground(t, f)
	f.st.DB().First(&row)
	var hold model.BillingReservation
	f.st.DB().First(&hold, "id = ?", row.BillingID)
	if row.Stage != "terminal" || !row.ReplayPending || row.ControlCalls != 1 || hold.State != billing.Pending || hold.ChargeUnits != 0 {
		t.Fatalf("missing usage was charged/refunded: %+v %+v", row, hold)
	}
	// Restart recovery consumes only durable terminal facts; it does not POST.
	opts := BackgroundOptions{Repository: f.st, Parser: native.BackgroundParser{}, BillingMode: "strict", Finance: engine, ActiveKey: "event", Keys: map[string][]byte{"event": bytes.Repeat([]byte{61}, 32)}, DigestKey: bytes.Repeat([]byte{67}, 32), Retention: 10 * time.Minute, MetadataRetention: 24 * time.Hour, Limits: bs.Limits{InstanceRecords: 32, OwnerRecords: 16, InstanceActive: 8, OwnerActive: 4, InstanceBytes: 64 << 20, OwnerBytes: 32 << 20}}
	service, err := NewBackgroundService(f.ctx, opts)
	if err != nil {
		t.Fatal(err)
	}
	f.s, f.runner.opts.Service = service, service
	runStrictBackground(t, f)
	f.st.DB().First(&row)
	f.st.DB().First(&hold, "id = ?", row.BillingID)
	if row.ReplayPending || row.ControlCalls != 2 || hold.State != billing.Settled || hold.ChargeUnits != 30_000 || posts.Load() != 1 || gets.Load() != 2 {
		t.Fatalf("late receipt/restart wrong: task=%+v hold=%+v posts=%d gets=%d", row, hold, posts.Load(), gets.Load())
	}
	if _, err := NewBackgroundService(f.ctx, opts); err != nil {
		t.Fatal("idempotent recovery", err)
	}
	var attempts int64
	f.st.DB().Model(&model.BillingAttempt{}).Where("reservation_id = ?", row.BillingID).Count(&attempts)
	if attempts != 1 {
		t.Fatal("poll created another inference attempt", attempts)
	}
}

func TestBackgroundStrictQueuedCancelAndExpiredHoldNeverSend(t *testing.T) {
	for _, cancelTask := range []bool{true, false} {
		t.Run(fmt.Sprint(cancelTask), func(t *testing.T) {
			var sends atomic.Int64
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { sends.Add(1); w.WriteHeader(500) }))
			defer upstream.Close()
			f, engine := strictBackgroundFixture(t, upstream.URL)
			receipt := strictBackgroundSubmit(t, f, "cancel-or-expire")
			var row model.BackgroundJob
			f.st.DB().First(&row)
			if cancelTask {
				if _, err := f.s.Cancel(f.ctx, receipt.ID); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := f.st.DB().Model(&model.BillingReservation{}).Where("id = ?", row.BillingID).Update("lease_until", time.Now().Add(-time.Second).UnixMilli()).Error; err != nil {
					t.Fatal(err)
				}
				if err := engine.RecoverExpired(context.Background(), time.Now()); err != nil {
					t.Fatal(err)
				}
				_ = f.runner.runOne(context.Background())
			}
			if sends.Load() != 0 {
				t.Fatal("canceled/expired hold sent upstream")
			}
			var attempts int64
			f.st.DB().Model(&model.BillingAttempt{}).Count(&attempts)
			if attempts != 0 {
				t.Fatal("never-sent task created attempt")
			}
			var hold model.BillingReservation
			f.st.DB().First(&hold, "id = ?", row.BillingID)
			if hold.State != billing.Settled || hold.ChargeUnits != 0 {
				t.Fatalf("never-sent hold not released: %+v", hold)
			}
		})
	}
}

func TestBackgroundStrictControlRPCBudgetPersistsAndStopsAt32(t *testing.T) {
	var sends atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sends.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"resp_provider","object":"response","model":"m","status":"in_progress","output":[]}`)
	}))
	defer upstream.Close()
	f, _ := strictBackgroundFixture(t, upstream.URL)
	strictBackgroundSubmit(t, f, "control-budget")
	runStrictBackground(t, f)
	if err := f.st.DB().Model(&model.BackgroundJob{}).Where("1 = 1").Update("control_calls", 31).Error; err != nil {
		t.Fatal(err)
	}
	runStrictBackground(t, f)
	runStrictBackground(t, f)
	var row model.BackgroundJob
	f.st.DB().First(&row)
	var hold model.BillingReservation
	f.st.DB().First(&hold, "id = ?", row.BillingID)
	if row.ControlCalls != 32 || row.Stage != "uncertain" || sends.Load() != 2 || hold.State != billing.Pending || hold.ChargeUnits != 0 {
		t.Fatalf("control calls escaped bound: %+v %+v sends=%d", row, hold, sends.Load())
	}
}
