package generation

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"minigateway/internal/billing"
	"minigateway/internal/config"
	bg "minigateway/internal/foundation/contracts/background/v1"
	v1 "minigateway/internal/foundation/contracts/execution/v1"
	state "minigateway/internal/foundation/contracts/state/v1"
	bs "minigateway/internal/generation/backgroundstate"
	"minigateway/internal/model"
	"minigateway/internal/store"
)

type strictBackgroundCrashRepository struct {
	*bgCrashRepository
	latest bs.Record
}

func (r *strictBackgroundCrashRepository) AdmitBackground(ctx context.Context, in bs.Record, limits bs.Limits, guards ...state.HistoryGuard) (bs.Record, error) {
	if r.mode == "hold-before-admit" {
		r.pause(in)
	}
	row, err := r.Repository.AdmitBackground(ctx, in, limits, guards...)
	if err == nil && r.mode == "admitted-before-ack" {
		r.pause(row)
	}
	return row, err
}

func (r *strictBackgroundCrashRepository) UpdateBackground(ctx context.Context, claim bs.Claim, revision uint64, update bs.Update) (bs.Record, error) {
	if r.mode == "dispatched-before-remote-id" && update.UsageMetadata != nil {
		row, err := r.Repository.ReadClaimedBackground(ctx, claim)
		if err != nil {
			return row, err
		}
		r.pause(row)
	}
	row, err := r.Repository.UpdateBackground(ctx, claim, revision, update)
	if err == nil {
		r.latest = row
		if r.mode == "remote-id-committed" && update.Stage == bg.StageRemoteActive || r.mode == "usage-before-finance" && update.Stage == bg.StageTerminal {
			r.pause(row)
		}
	}
	return row, err
}

type strictBackgroundCrashFinance struct {
	bs.Finance
	repository *strictBackgroundCrashRepository
}

func (f strictBackgroundCrashFinance) CompleteBackgroundReservation(ctx context.Context, id string, key, user uint, name string, usage *v1.Usage, complete bool) error {
	err := f.Finance.CompleteBackgroundReservation(ctx, id, key, user, name, usage, complete)
	if err == nil && complete {
		f.repository.pause(f.repository.latest)
	}
	return err
}

func strictBackgroundCrashOpen(t *testing.T, path, upstream, mode string, expiry int64, wrap func(bs.Repository) bs.Repository) (bgRuntimeFixture, *billing.Engine) {
	t.Helper()
	return bgRuntimeOpenFinance(t, path, upstream, wrap, func(st *store.Store) *billing.Engine {
		engine := strictBackgroundEngine(t, st, upstream, expiry)
		if mode == "hold-before-admit" || mode == "dispatched-before-remote-id" {
			if err := engine.EnableStrict("1", 2*time.Second); err != nil {
				t.Fatal(err)
			}
		}
		return engine
	})
}

func TestBackgroundStrictFinanceCrashChild(t *testing.T) {
	path := os.Getenv("TXAPI_BG_FINANCE_CRASH_DB")
	if path == "" {
		t.Skip("subprocess helper")
	}
	mode := os.Getenv("TXAPI_BG_FINANCE_CRASH_MODE")
	expiry, err := strconv.ParseInt(os.Getenv("TXAPI_BG_FINANCE_CRASH_EXPIRY"), 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	var repository *strictBackgroundCrashRepository
	f, engine := strictBackgroundCrashOpen(t, path, os.Getenv("TXAPI_BG_FINANCE_CRASH_UPSTREAM"), mode, expiry, func(repo bs.Repository) bs.Repository {
		repository = &strictBackgroundCrashRepository{bgCrashRepository: &bgCrashRepository{Repository: repo, mode: mode, ready: path + ".ready"}}
		return repository
	})
	if mode == "finance-before-ack" {
		f.s.finance = strictBackgroundCrashFinance{Finance: engine, repository: repository}
	}
	strictBackgroundSubmit(t, f, "strict-crash-key")
	if err := f.runner.runOne(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Fatal("strict financial crash barrier was not reached")
}

func killStrictBackgroundChild(t *testing.T, path, upstream, mode string, expiry int64) bs.Record {
	t.Helper()
	command := exec.Command(os.Args[0], "-test.run=^TestBackgroundStrictFinanceCrashChild$", "-test.count=1", "-test.timeout=25s")
	command.Env = append(os.Environ(), "TXAPI_BG_FINANCE_CRASH_DB="+path, "TXAPI_BG_FINANCE_CRASH_MODE="+mode, "TXAPI_BG_FINANCE_CRASH_UPSTREAM="+upstream, "TXAPI_BG_FINANCE_CRASH_EXPIRY="+strconv.FormatInt(expiry, 10))
	var output bytes.Buffer
	command.Stdout, command.Stderr = &output, &output
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	waited := false
	t.Cleanup(func() {
		if !waited {
			_ = command.Process.Kill()
			<-done
		}
	})
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		var marker struct{ Row bs.Record }
		if data, err := os.ReadFile(path + ".ready"); err == nil && json.Unmarshal(data, &marker) == nil {
			if err := command.Process.Kill(); err != nil {
				t.Fatal(err)
			}
			if err := <-done; err == nil {
				t.Fatal("worker exited gracefully instead of being killed")
			}
			waited = true
			return marker.Row
		}
		select {
		case err := <-done:
			waited = true
			t.Fatalf("strict crash child exited before barrier: %v %s", err, output.String())
		default:
		}
		time.Sleep(10 * time.Millisecond)
	}
	_ = command.Process.Kill()
	<-done
	waited = true
	t.Fatalf("strict crash barrier timed out: %s", output.String())
	return bs.Record{}
}

func TestBackgroundStrictProcessDeathPreservesEveryFinancialCommitWindow(t *testing.T) {
	for _, mode := range []string{"hold-before-admit", "admitted-before-ack", "dispatched-before-remote-id", "remote-id-committed", "usage-before-finance", "finance-before-ack"} {
		t.Run(mode, func(t *testing.T) {
			var posts, gets atomic.Int64
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer fixture-upstream-key" || r.URL.Path != "/v1/responses" && r.URL.Path != "/v1/responses/resp_provider" {
					t.Error("unbound crash-test request", r.Method, r.URL.Path)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				if r.Method == http.MethodPost {
					posts.Add(1)
					if mode == "remote-id-committed" {
						_, _ = io.WriteString(w, `{"id":"resp_provider","object":"response","model":"m","status":"queued","output":[]}`)
						return
					}
				} else if r.Method == http.MethodGet {
					gets.Add(1)
				} else {
					t.Error("unexpected control RPC", r.Method)
				}
				_, _ = io.WriteString(w, `{"id":"resp_provider","object":"response","model":"m","status":"completed","output":[],"usage":{"input_tokens":2,"output_tokens":1,"total_tokens":3}}`)
			}))
			defer upstream.Close()
			path := filepath.Join(t.TempDir(), "strict-financial-crash.db")
			expiry := time.Now().Add(time.Hour).Unix()
			initial, _ := strictBackgroundCrashOpen(t, path, upstream.URL, "", expiry, nil)
			if err := initial.st.Close(); err != nil {
				t.Fatal(err)
			}
			barrier := killStrictBackgroundChild(t, path, upstream.URL, mode, expiry)
			assertStrictBackgroundCrashBarrier(t, path, mode, barrier)
			initialPosts := int64(1)
			if mode == "hold-before-admit" || mode == "admitted-before-ack" {
				initialPosts = 0
			}
			if posts.Load() != initialPosts || gets.Load() != 0 {
				t.Fatal("crash occurred outside the declared network window", posts.Load(), gets.Load())
			}
			until := barrier.ClaimUntil
			if mode == "hold-before-admit" || mode == "dispatched-before-remote-id" {
				until = max(until, barrier.Deadline)
			}
			if delay := time.Until(time.UnixMilli(until)); delay > 0 {
				time.Sleep(delay + 30*time.Millisecond)
			}
			for restart := range 2 {
				f, engine := strictBackgroundCrashOpen(t, path, upstream.URL, "", expiry, nil)
				if err := engine.RecoverExpired(context.Background(), time.Now()); err != nil {
					t.Fatal(err)
				}
				var row model.BackgroundJob
				if mode != "hold-before-admit" {
					for deadline := time.Now().Add(3 * time.Second); ; {
						if err := f.st.DB().First(&row, "id = ?", barrier.ID).Error; err != nil {
							t.Fatal(err)
						}
						if row.Stage == "uncertain" || row.Stage == "terminal" && !row.ReplayPending {
							break
						}
						if time.Now().After(deadline) {
							t.Fatal("financial crash recovery did not drain", row.Stage, row.ReplayPending)
						}
						if err := f.runner.runOne(context.Background()); err != nil && !errors.Is(err, bg.ErrNotFound) {
							t.Fatal(err)
						}
						time.Sleep(10 * time.Millisecond)
					}
				}
				assertStrictBackgroundCrashLedger(t, f, barrier.BillingID, mode, row)
				wantPosts := int64(1)
				if mode == "hold-before-admit" {
					wantPosts = 0
				}
				wantGets := int64(0)
				if mode == "remote-id-committed" {
					wantGets = 1
				}
				if posts.Load() != wantPosts || gets.Load() != wantGets {
					t.Fatalf("restart %d duplicated execution/control: POST=%d GET=%d", restart, posts.Load(), gets.Load())
				}
				if err := f.st.Close(); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

// Read the killed worker's committed state before any service recovery runs.
func assertStrictBackgroundCrashBarrier(t *testing.T, path, mode string, barrier bs.Record) {
	t.Helper()
	st, err := store.Open(config.DatabaseConfig{Path: path, CredentialKeysFile: path + ".keys"})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	var hold model.BillingReservation
	if err := st.DB().First(&hold, "id = ?", barrier.BillingID).Error; err != nil {
		t.Fatal(err)
	}
	var attempts, tasks int64
	st.DB().Model(&model.BillingAttempt{}).Where("reservation_id = ?", hold.ID).Count(&attempts)
	st.DB().Model(&model.BackgroundJob{}).Where("billing_id = ?", hold.ID).Count(&tasks)
	wantAttempts, wantTasks := int64(1), int64(1)
	wantState, wantUsed, wantHeld := billing.Active, int64(0), hold.ReservedUnits
	if mode == "hold-before-admit" || mode == "admitted-before-ack" {
		wantAttempts, wantState = 0, billing.Reserved
		if mode == "hold-before-admit" {
			wantTasks = 0
		}
	} else if mode == "finance-before-ack" {
		wantState, wantUsed, wantHeld = billing.Settled, 30_000, 0
	}
	if hold.State != wantState || hold.ChargeUnits != wantUsed || attempts != wantAttempts || tasks != wantTasks || wantAttempts == 1 && hold.ProviderHeldUnits <= 0 {
		t.Fatalf("wrong committed crash window: mode=%s hold=%+v attempts=%d tasks=%d", mode, hold, attempts, tasks)
	}
	if mode == "usage-before-finance" || mode == "finance-before-ack" {
		var task model.BackgroundJob
		if err := st.DB().First(&task, "id = ?", barrier.ID).Error; err != nil || task.Stage != "terminal" || !task.ReplayPending || task.Usage.Completeness != "complete" {
			t.Fatalf("terminal receipt not committed before kill: %+v %v", task, err)
		}
	}
	for _, owner := range []struct {
		kind string
		id   uint
	}{{"user", hold.UserID}, {"key", hold.TokenID}} {
		var balance model.BillingBalance
		if err := st.DB().First(&balance, "owner_type = ? AND owner_id = ?", owner.kind, owner.id).Error; err != nil || balance.UsedUnits != wantUsed || balance.ReservedUnits != wantHeld {
			t.Fatalf("crash barrier dual hold invalid: %+v %v", balance, err)
		}
	}
}

func assertStrictBackgroundCrashLedger(t *testing.T, f bgRuntimeFixture, id, mode string, task model.BackgroundJob) {
	t.Helper()
	var hold model.BillingReservation
	if err := f.st.DB().First(&hold, "id = ?", id).Error; err != nil {
		t.Fatal(err)
	}
	var attempts, tasks int64
	f.st.DB().Model(&model.BillingAttempt{}).Where("reservation_id = ?", id).Count(&attempts)
	f.st.DB().Model(&model.BackgroundJob{}).Where("billing_id = ?", id).Count(&tasks)
	wantAttempts, wantTasks := int64(1), int64(1)
	wantUsed, wantHeld := int64(30_000), int64(0)
	if mode == "hold-before-admit" {
		wantAttempts, wantTasks, wantUsed = 0, 0, 0
	} else if mode == "dispatched-before-remote-id" {
		wantUsed, wantHeld = 0, hold.ReservedUnits
		if hold.State != billing.Pending || hold.ProviderHeldUnits <= 0 || task.Stage != "uncertain" {
			t.Fatalf("unknown crash execution refunded or invented: %+v %+v", hold, task)
		}
	}
	if attempts != wantAttempts || tasks != wantTasks || hold.ChargeUnits != wantUsed || wantHeld == 0 && hold.State != billing.Settled {
		t.Fatalf("financial crash ledger invalid: attempts=%d tasks=%d hold=%+v", attempts, tasks, hold)
	}
	for _, owner := range []struct {
		kind string
		id   uint
	}{{"user", hold.UserID}, {"key", hold.TokenID}} {
		var balance model.BillingBalance
		if err := f.st.DB().First(&balance, "owner_type = ? AND owner_id = ?", owner.kind, owner.id).Error; err != nil || balance.UsedUnits != wantUsed || balance.ReservedUnits != wantHeld {
			t.Fatalf("dual hold changed across crash: %+v %v", balance, err)
		}
	}
}
