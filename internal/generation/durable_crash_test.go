package generation

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"minigateway/internal/access"
	"minigateway/internal/billing"
	"minigateway/internal/config"
	"minigateway/internal/gateway"
	"minigateway/internal/model"
	relaymodel "minigateway/internal/protocol/model"
	"minigateway/internal/store"
)

const durableCrashReady = "TXAPI_DURABLE_CRASH_READY:"

type crashIntentBilling struct {
	*billing.Engine
	mode  string
	pause func()
}

func (b crashIntentBilling) EndAttempt(ctx context.Context, pre *billing.PreDeduct, number, status int, termination string, usage *relaymodel.Usage) error {
	err := b.Engine.EndAttempt(ctx, pre, number, status, termination, usage)
	if err == nil && b.mode == "after-receipt" {
		b.pause()
	}
	return err
}

func (b crashIntentBilling) Finish(ctx context.Context, pre *billing.PreDeduct, usage *relaymodel.Usage, actual, group string, complete bool) (int64, string, error) {
	charge, state, err := b.Engine.Finish(ctx, pre, usage, actual, group, complete)
	if err == nil && state == billing.Settled && b.mode == "after-finish" {
		b.pause()
	}
	return charge, state, err
}

type crashSealedOutput struct {
	ArchiveStore
	pause func()
}

func (s crashSealedOutput) AppendGenerationSegment(ctx context.Context, record model.GenerationRecord, segment model.GenerationSegment) error {
	err := s.ArchiveStore.AppendGenerationSegment(ctx, record, segment)
	if err == nil && segment.Kind == "result" {
		s.pause()
	}
	return err
}

// Crash barriers are test-only wrappers AFTER successful transactions. A killed
// process runs none of the gateway/runner/lease/output deferred cleanup paths.
func TestDurableExecutionCrashWorker(t *testing.T) {
	path := os.Getenv("TXAPI_TEST_DURABLE_CRASH_DB")
	if path == "" {
		t.Skip("subprocess helper")
	}
	mode := os.Getenv("TXAPI_TEST_DURABLE_CRASH_MODE")
	st, err := store.Open(config.DatabaseConfig{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	user, err := st.CreateUser(&store.UserInput{Username: "durable-crash-owner", Password: "fixture-password-123", ModelMode: access.All})
	if err != nil {
		t.Fatal(err)
	}
	key, err := st.IssueToken(user.ID, &store.TokenInput{ModelMode: access.All})
	if err != nil {
		t.Fatal(err)
	}
	engine, ch := strictRecordingFixture(t, st)
	acceptorOpts := intentOptions(st, engine)
	acceptorOpts.QueueTTL = 30 * time.Second
	acceptor := intentAcceptor(t, acceptorOpts)
	in := strictSocketExecution()
	in.Body = bytes.ReplaceAll(in.Body, []byte("hello"), []byte("durable-input-private-fixture-979c"))
	receipt, err := acceptor.Admit(context.Background(), access.Binding{UserID: user.ID, TokenID: key.ID}, "durable-idempotency-private-fixture-c299", in)
	if err != nil {
		t.Fatal(err)
	}
	pause := func() {
		fmt.Println(durableCrashReady + receipt.ID)
		for {
			time.Sleep(time.Hour)
		}
	}
	if mode == "after-claim" {
		if _, _, err := engine.ClaimGenerationIntent(context.Background(), time.Second); err != nil {
			t.Fatal(err)
		}
		pause()
	}
	port := crashIntentBilling{Engine: engine, mode: mode, pause: pause}
	g, _ := newGateway(t, gateway.Options{Channels: ch, StrictBilling: port, BillingMode: billing.ModeStrict,
		Client: &http.Client{Transport: transportFunc(func(*http.Request) (*http.Response, error) {
			if mode == "after-start" {
				pause()
			}
			return response(), nil
		})}})
	opts := durableOptions(t, st, engine, g)
	opts.Acceptor = acceptor
	if mode == "after-seal" {
		archiveOpts := archiveOptions(st)
		archiveOpts.Store = crashSealedOutput{ArchiveStore: st, pause: pause}
		opts.Archive = testArchive(t, archiveOpts)
	}
	if err := (&DurableRunner{opts: opts}).runOne(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Fatal("crash boundary was not reached")
}

func TestDurableProcessDeathAndRestartNeverBlindlyReplaysAttemptedWork(t *testing.T) {
	for _, mode := range []string{"after-claim", "after-start", "after-receipt", "after-finish", "after-seal"} {
		t.Run(mode, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "durable-crash.db")
			id := killGenerationChild(t, path, mode, "TestDurableExecutionCrashWorker", "TXAPI_TEST_DURABLE_CRASH", durableCrashReady)
			st, err := store.Open(config.DatabaseConfig{Path: path})
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			original := readDurableIntent(t, st, id)
			if remaining := time.Until(time.UnixMilli(original.ClaimUntil)); remaining > 0 {
				time.Sleep(remaining + 20*time.Millisecond)
			}
			assertIntentDatabaseNoPlaintext(t, path, "durable-input-private-fixture-979c", "durable-idempotency-private-fixture-c299")
			engine, ch := strictRecordingFixture(t, st)
			var hits atomic.Int64
			g, _ := newGateway(t, gateway.Options{Channels: ch, StrictBilling: engine, BillingMode: billing.ModeStrict,
				Client: &http.Client{Transport: transportFunc(func(*http.Request) (*http.Response, error) { hits.Add(1); return response(), nil })}})
			opts := durableOptions(t, st, engine, g)
			r := &DurableRunner{opts: opts}
			for range 2 {
				if _, err := engine.RecoverGenerationIntents(context.Background(), time.Now(), 200); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "after-claim" {
				if err := r.runOne(context.Background()); err != nil {
					t.Fatal("proved-unstarted work could not restart", err)
				}
				if hits.Load() != 1 {
					t.Fatal("restarted unstarted work was not dispatched once")
				}
			} else if err := r.runOne(context.Background()); !errors.Is(err, model.ErrIntentNotFound) || hits.Load() != 0 {
				t.Fatal("attempted work replayed after process death", err)
			}
			got := readDurableIntent(t, st, id)
			wantState, financial, held, charge := model.IntentUncertain, billing.Settled, int64(0), int64(900)
			switch mode {
			case "after-claim", "after-seal":
				wantState = model.IntentSucceeded
			case "after-start":
				financial, held, charge = billing.Pending, billing.UnitScale, 0
			}
			if got.State != wantState {
				t.Fatalf("restart fabricated terminal state: got=%s want=%s", got.State, wantState)
			}
			assertDurableMoney(t, st, got, financial, held, charge)
			var attempts, reservations int64
			if err := st.DB().Model(&model.BillingAttempt{}).Count(&attempts).Error; err != nil || attempts != 1 {
				t.Fatal("restart changed attempt count", err)
			}
			if err := st.DB().Model(&model.BillingReservation{}).Count(&reservations).Error; err != nil || reservations != 1 {
				t.Fatal("restart reserved twice", err)
			}
			if mode == "after-seal" {
				if _, err := opts.Archive.Read(context.Background(), access.Binding{UserID: got.UserID, TokenID: got.TokenID}, got.OutputRecordID); err != nil {
					t.Fatal("sealed output lost after restart", err)
				}
			}
		})
	}
}
