package generation

import (
	"context"
	"errors"
	"minigateway/internal/foundation/persistence"
	bridge "minigateway/internal/foundation/runtime"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"minigateway/internal/access"
	"minigateway/internal/billing"
	execv1 "minigateway/internal/foundation/contracts/execution/v1"
	"minigateway/internal/gateway"
	"minigateway/internal/model"
	relaymodel "minigateway/internal/protocol/model"
	"minigateway/internal/store"
)

func durableOptions(t *testing.T, st *store.Store, engine *billing.Engine, g *gateway.Gateway) DurableOptions {
	t.Helper()
	return DurableOptions{Acceptor: intentAcceptor(t, intentOptions(st, persistence.Admission(engine))), Archive: testArchive(t, archiveOptions(st)), Leases: persistence.Leases(engine),
		Workers: 1, PollInterval: 20 * time.Millisecond, LeaseDuration: time.Second, Execute: bridge.NewExecutor(g).ExecuteClaimed}
}

func closeDurable(t *testing.T, r *DurableRunner) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := r.Close(ctx); err != nil {
		t.Fatal(err)
	}
}

func readDurableIntent(t *testing.T, st *store.Store, id string) model.GenerationIntent {
	t.Helper()
	var in model.GenerationIntent
	if err := st.DB().First(&in, "id = ?", id).Error; err != nil {
		t.Fatal(err)
	}
	return in
}

func waitDurableState(t *testing.T, st *store.Store, id, state string) model.GenerationIntent {
	t.Helper()
	until := time.Now().Add(5 * time.Second)
	for {
		in := readDurableIntent(t, st, id)
		if in.State == state {
			return in
		}
		if time.Now().After(until) {
			t.Fatalf("intent remained %s, want %s", in.State, state)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func assertDurableMoney(t *testing.T, st *store.Store, in model.GenerationIntent, state string, held, charge int64) {
	t.Helper()
	var row model.BillingReservation
	if err := st.DB().First(&row, "id = ?", in.BillingID).Error; err != nil || row.State != state || row.ChargeUnits != charge {
		t.Fatalf("financial state changed: state=%s charge=%d err=%v", row.State, row.ChargeUnits, err)
	}
	var balances []model.BillingBalance
	if err := st.DB().Where("(owner_type = 'user' AND owner_id = ?) OR (owner_type = 'key' AND owner_id = ?)", in.UserID, in.TokenID).Find(&balances).Error; err != nil || len(balances) != 2 {
		t.Fatal("dual balance missing", err)
	}
	for _, balance := range balances {
		if balance.ReservedUnits != held || balance.UsedUnits != charge {
			t.Fatalf("hold/charge mismatch: %+v", balance)
		}
	}
}

func TestDurableDispatchUsesOriginalReservationPriceAndEncryptedOutput(t *testing.T) {
	st, binding, other := fixture(t)
	engine, ch := strictRecordingFixture(t, st)
	var hits atomic.Int64
	var result execv1.Outcome
	g, capacity := newGateway(t, gateway.Options{Channels: ch, StrictBilling: engine, BillingMode: billing.ModeStrict,
		Client: &http.Client{Transport: transportFunc(func(*http.Request) (*http.Response, error) { hits.Add(1); return response(), nil })}})
	opts := durableOptions(t, st, engine, g)
	receipt := admitIntent(t, opts.Acceptor, binding, "durable-dispatch")
	expected, err := engine.Prices().Snapshot().CostUnits(&relaymodel.Usage{PromptTokens: 2, CompletionTokens: 1, TotalTokens: 3}, "m", "default")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertModelPrice(&model.ModelPrice{Model: "m", InputPricePer1K: 0.0015, OutputPricePer1K: 0.006}); err != nil {
		t.Fatal(err)
	}
	if err := engine.Prices().ReloadPrices(); err != nil {
		t.Fatal(err)
	}
	opts.Execute = func(ctx context.Context, output execv1.Output, in execv1.Invocation, claim model.GenerationClaim) execv1.Outcome {
		result = bridge.NewExecutor(g).ExecuteClaimed(ctx, output, in, claim)
		return result
	}
	r := &DurableRunner{opts: opts}
	if err := r.runOne(context.Background()); err != nil {
		t.Fatal(err)
	}
	in := readDurableIntent(t, st, receipt.ID)
	if in.State != model.IntentSucceeded || in.OutputRecordID == "" || result.Identity.RequestID != receipt.ID || result.BillingState != billing.Settled || result.StatusCode != 200 || hits.Load() != 1 {
		t.Fatalf("durable execution did not finish original receipt: state=%s result=%+v hits=%d", in.State, result, hits.Load())
	}
	assertDurableMoney(t, st, in, billing.Settled, 0, expected)
	if recorded, err := opts.Archive.Read(context.Background(), binding, in.OutputRecordID); err != nil || recorded.Result.StatusCode != 200 || !recorded.Result.ProtocolComplete || !recorded.Result.Committed || recorded.Metadata.Values["X-TXAPI-Billing-ID"] != receipt.BillingID {
		t.Fatal("successful output not durably readable on original reservation", err)
	}
	if _, err := opts.Archive.Read(context.Background(), other, in.OutputRecordID); err == nil {
		t.Fatal("another Key read claimed output")
	}
	observer, err := opts.Archive.OpenCursor(context.Background(), binding, in.OutputRecordID, "")
	if err != nil {
		t.Fatal(err)
	}
	page, err := observer.Next(context.Background(), 4096)
	_ = observer.Close()
	if err != nil || !page.Done || page.Result == nil || page.Result.BillingState != billing.Settled {
		t.Fatal("durable output cursor did not expose the original settled observation", err)
	}
	if err := r.runOne(context.Background()); !errors.Is(err, model.ErrIntentNotFound) || hits.Load() != 1 {
		t.Fatal("completed intent was dispatched twice", err)
	}
	var reservations int64
	if err := st.DB().Model(&model.BillingReservation{}).Count(&reservations).Error; err != nil || reservations != 1 {
		t.Fatal("worker created another hold", err)
	}
	if snapshot := capacity.Snapshot(); snapshot.Requests != 0 || snapshot.Attempts != 0 {
		t.Fatal("worker leaked admission resources")
	}
}

func TestDurableDispatchRevocationTamperAndPreExecutionFailures(t *testing.T) {
	for _, mode := range []string{"revoked", "policy", "tamper", "output-capacity", "revoked-after-output", "panic"} {
		t.Run(mode, func(t *testing.T) {
			st, binding, _ := fixture(t)
			engine, ch := strictRecordingFixture(t, st)
			var hits atomic.Int64
			g, _ := newGateway(t, gateway.Options{Channels: ch, StrictBilling: engine, BillingMode: billing.ModeStrict,
				Client: &http.Client{Transport: transportFunc(func(*http.Request) (*http.Response, error) { hits.Add(1); return response(), nil })}})
			opts := durableOptions(t, st, engine, g)
			receipt := admitIntent(t, opts.Acceptor, binding, "refuse-before-send")
			switch mode {
			case "revoked":
				if err := st.DB().Model(&model.Token{}).Where("id = ?", binding.TokenID).Update("status", model.TokenStatusDisabled).Error; err != nil {
					t.Fatal(err)
				}
			case "policy":
				if err := st.DB().Model(&model.Token{}).Where("id = ?", binding.TokenID).Updates(map[string]any{"model_mode": access.Allowlist, "models": model.StringList{}}).Error; err != nil {
					t.Fatal(err)
				}
			case "tamper":
				in := readDurableIntent(t, st, receipt.ID)
				in.Ciphertext[len(in.Ciphertext)-1] ^= 1
				if err := st.DB().Model(&model.GenerationIntent{}).Where("id = ?", in.ID).Update("ciphertext", in.Ciphertext).Error; err != nil {
					t.Fatal(err)
				}
			case "output-capacity":
				opts.Archive.limits.OwnerRecords = 1
				if _, err := opts.Archive.start(context.Background(), binding); err != nil {
					t.Fatal(err)
				}
			case "revoked-after-output":
				opts.Archive.resolve = func(ctx context.Context, b access.Binding) (access.Principal, error) {
					principal, err := st.ResolvePrincipal(ctx, b)
					if err != nil {
						return access.Principal{}, err
					}
					if err := st.DB().Model(&model.Token{}).Where("id = ?", b.TokenID).Update("status", model.TokenStatusDisabled).Error; err != nil {
						return access.Principal{}, err
					}
					return principal, nil
				}
			case "panic":
				opts.Execute = func(context.Context, execv1.Output, execv1.Invocation, model.GenerationClaim) execv1.Outcome {
					panic("private callback failure")
				}
			}
			r := &DurableRunner{opts: opts}
			if err := r.runOne(context.Background()); err == nil {
				t.Fatal("failed dispatch reported success")
			}
			in := readDurableIntent(t, st, receipt.ID)
			if hits.Load() != 0 {
				t.Fatal("rejected durable work reached upstream")
			}
			if mode == "tamper" {
				if in.State != model.IntentUncertain {
					t.Fatal("bad content was replayable/refunded")
				}
				assertDurableMoney(t, st, in, billing.Pending, billing.UnitScale, 0)
			} else {
				if in.State != model.IntentFailed {
					t.Fatal("unstarted rejection not terminal", in.State)
				}
				assertDurableMoney(t, st, in, billing.Settled, 0, 0)
			}
		})
	}
}

func TestDurableOutputSealFailureDoesNotUndoSettlementOrReplay(t *testing.T) {
	st, binding, _ := fixture(t)
	engine, ch := strictRecordingFixture(t, st)
	var hits atomic.Int64
	g, _ := newGateway(t, gateway.Options{Channels: ch, StrictBilling: engine, BillingMode: billing.ModeStrict,
		Client: &http.Client{Transport: transportFunc(func(*http.Request) (*http.Response, error) { hits.Add(1); return response(), nil })}})
	opts := durableOptions(t, st, engine, g)
	archiveOpts := archiveOptions(st)
	archiveOpts.Store = failingArchiveStore{ArchiveStore: persistence.Archive(st), kind: "result"}
	opts.Archive = testArchive(t, archiveOpts)
	receipt := admitIntent(t, opts.Acceptor, binding, "output-seal-failure")
	r := &DurableRunner{opts: opts}
	if err := r.runOne(context.Background()); !errors.Is(err, ErrArchiveUnavailable) {
		t.Fatal("failed output seal hidden", err)
	}
	in := readDurableIntent(t, st, receipt.ID)
	if in.State != model.IntentUncertain || hits.Load() != 1 {
		t.Fatal("incomplete output published success or replayed")
	}
	var reservation model.BillingReservation
	if err := st.DB().First(&reservation, "id = ?", receipt.BillingID).Error; err != nil || reservation.State != billing.Settled || reservation.ChargeUnits <= 0 {
		t.Fatal("output failure undid settled bill", err)
	}
	assertDurableMoney(t, st, in, billing.Settled, 0, reservation.ChargeUnits)
	if _, err := opts.Archive.Read(context.Background(), binding, in.OutputRecordID); !errors.Is(err, ErrArchiveIncomplete) {
		t.Fatal("partial output exposed", err)
	}
	for range 2 {
		if _, err := engine.RecoverGenerationIntents(context.Background(), time.Now().Add(time.Hour), 200); err != nil {
			t.Fatal(err)
		}
		if err := r.runOne(context.Background()); !errors.Is(err, model.ErrIntentNotFound) {
			t.Fatal("unknown observation replayed", err)
		}
	}
	if hits.Load() != 1 {
		t.Fatal("recovery sent another request")
	}
}

func TestDurableLeaseHeartbeatAndCancellationJoinUnknownExecution(t *testing.T) {
	for _, mode := range []string{"renew-and-complete", "cancel-request", "shutdown"} {
		t.Run(mode, func(t *testing.T) {
			st, binding, _ := fixture(t)
			engine, ch := strictRecordingFixture(t, st)
			started, release := make(chan struct{}), make(chan struct{})
			var hits atomic.Int64
			g, capacity := newGateway(t, gateway.Options{Channels: ch, StrictBilling: engine, BillingMode: billing.ModeStrict,
				Client: &http.Client{Transport: transportFunc(func(req *http.Request) (*http.Response, error) {
					if hits.Add(1) == 1 {
						close(started)
					}
					select {
					case <-release:
						return response(), nil
					case <-req.Context().Done():
						return nil, req.Context().Err()
					}
				})}})
			opts := durableOptions(t, st, engine, g)
			receipt := admitIntent(t, opts.Acceptor, binding, "lease-lifecycle")
			r, err := NewDurableRunner(context.Background(), opts)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { closeDurable(t, r) })
			signal(t, started)
			first := readDurableIntent(t, st, receipt.ID)
			switch mode {
			case "renew-and-complete":
				until := time.Now().Add(3 * time.Second)
				for readDurableIntent(t, st, receipt.ID).ClaimUntil <= first.ClaimUntil {
					if time.Now().After(until) {
						t.Fatal("live lease was not renewed")
					}
					time.Sleep(10 * time.Millisecond)
				}
				close(release)
				waitDurableState(t, st, receipt.ID, model.IntentSucceeded)
			case "cancel-request":
				if got, err := engine.CancelGenerationIntent(context.Background(), binding.UserID, binding.TokenID, receipt.ID); err != nil || !got.CancelRequested || got.State != model.IntentRunning {
					t.Fatal("attempted cancellation pretended to be free", err)
				}
				waitDurableState(t, st, receipt.ID, model.IntentUncertain)
			case "shutdown":
				closeDurable(t, r)
			}
			closeDurable(t, r)
			in := readDurableIntent(t, st, receipt.ID)
			if mode != "renew-and-complete" {
				assertDurableMoney(t, st, in, billing.Pending, billing.UnitScale, 0)
			}
			if hits.Load() != 1 {
				t.Fatal("lease/cancel/shutdown replayed request")
			}
			if snapshot := capacity.Snapshot(); snapshot.Requests != 0 || snapshot.Attempts != 0 {
				t.Fatal("runner returned before network/admission joined")
			}
		})
	}
}
