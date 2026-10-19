package gateway

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"minigateway/internal/access"
	"minigateway/internal/billing"
	"minigateway/internal/config"
	"minigateway/internal/core"
	v1 "minigateway/internal/foundation/contracts/execution/v1"
	v2 "minigateway/internal/foundation/contracts/execution/v2"
	history "minigateway/internal/foundation/contracts/usagehistory/v1"
	identityimpl "minigateway/internal/foundation/identity"
	"minigateway/internal/model"
	"minigateway/internal/store"
)

type historyKillBarrier struct {
	ID                    string `json:"id"`
	Stage                 string `json:"stage"`
	ProviderUsageObserved bool   `json:"provider_usage_observed"`
}

type historyKillOutput struct {
	mu   sync.Mutex
	data bytes.Buffer
}

func (b *historyKillOutput) Write(data []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.data.Write(data)
}
func (b *historyKillOutput) String() string { b.mu.Lock(); defer b.mu.Unlock(); return b.data.String() }

type historyKillRecorder struct {
	history.Recorder
	t           *testing.T
	stage, path string
}

func (r historyKillRecorder) pause(stage, id string, providerUsage bool) {
	if stage != r.stage {
		return
	}
	payload, err := json.Marshal(historyKillBarrier{ID: id, Stage: stage, ProviderUsageObserved: providerUsage})
	if err != nil {
		r.t.Fatal(err)
	}
	// Rename publishes one complete barrier document to the parent process.
	if err := os.WriteFile(r.path+".tmp", payload, 0600); err != nil {
		r.t.Fatal(err)
	}
	if err := os.Rename(r.path+".tmp", r.path); err != nil {
		r.t.Fatal(err)
	}
	select {}
}

func (r historyKillRecorder) Begin(ctx context.Context, in history.Start) error {
	if err := r.Recorder.Begin(ctx, in); err != nil {
		return err
	}
	r.pause("after-begin", in.ID, false)
	return nil
}

func (r historyKillRecorder) MayDispatch(ctx context.Context, id string, number int) error {
	if err := r.Recorder.MayDispatch(ctx, id, number); err != nil {
		return err
	}
	r.pause("after-dispatch-marker", id, false)
	return nil
}

func (r historyKillRecorder) Observe(ctx context.Context, in history.Receipt) error {
	if r.stage == "before-observe" {
		if in.Host.Phase != v1.ResponseReceived || in.Usage.Completeness != v2.Complete || in.Usage.Counts.TotalTokens == nil || *in.Usage.Counts.TotalTokens != 14 || in.Usage.Breakdown.CachedInputTokens == nil || *in.Usage.Breakdown.CachedInputTokens != 2 {
			r.t.Fatal("crash boundary did not follow real native usage decoding", in)
		}
		r.pause("before-observe", in.ID, true)
	}
	return r.Recorder.Observe(ctx, in)
}

func TestUsageRecordingProcessKillHelper(t *testing.T) {
	stage := os.Getenv("TXAPI_USAGE_HISTORY_KILL_STAGE")
	if stage == "" {
		t.Skip("subprocess helper")
	}
	if stage != "after-begin" && stage != "after-dispatch-marker" && stage != "before-observe" {
		t.Fatal("invalid crash stage")
	}
	st, err := store.Open(config.DatabaseConfig{Driver: "sqlite", Path: os.Getenv("TXAPI_USAGE_HISTORY_KILL_DB")})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx, _, ok := identityimpl.NewTokenAuthenticator(st).Authenticate(context.Background(), os.Getenv("TXAPI_USAGE_HISTORY_KILL_KEY"))
	if !ok {
		t.Fatal("child could not authenticate the persisted real API key")
	}
	channels, err := st.ChannelSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	recorder := historyKillRecorder{Recorder: st.UsageRecorder(model.UsageHistoryLimits{InstanceRecords: 32, OwnerRecords: 8, RetentionSeconds: 604800}), t: t, stage: stage, path: os.Getenv("TXAPI_USAGE_HISTORY_KILL_BARRIER")}
	g, err := newRegisteredGateway(Options{Channels: channels, BillingMode: billing.ModeDisabled, UsageHistory: recorder, TotalTimeout: 10 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	result := g.ExecuteTo(ctx, core.NewHTTPOutput(httptest.NewRecorder()), historyInvocation(false))
	t.Fatal("crash boundary did not block the child", result.StatusCode, result.RecordingState)
}

type historyKillFinancial struct {
	Balances     []model.BillingBalance
	Reservations []model.BillingReservation
	Attempts     []model.BillingAttempt
	Prices       []model.BillingPriceVersion
}

func readHistoryKillFinancial(t *testing.T, st *store.Store) historyKillFinancial {
	t.Helper()
	var out historyKillFinancial
	if err := st.DB().Order("owner_type, owner_id").Find(&out.Balances).Error; err != nil {
		t.Fatal(err)
	}
	if err := st.DB().Order("id").Find(&out.Reservations).Error; err != nil {
		t.Fatal(err)
	}
	if err := st.DB().Order("reservation_id, number").Find(&out.Attempts).Error; err != nil {
		t.Fatal(err)
	}
	if err := st.DB().Order("version").Find(&out.Prices).Error; err != nil {
		t.Fatal(err)
	}
	return out
}

func TestUsageRecordingProcessKillPreservesInterruptedMetadata(t *testing.T) {
	for _, stage := range []string{"after-begin", "after-dispatch-marker", "before-observe"} {
		t.Run(stage, func(t *testing.T) {
			var hits atomic.Int64
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hits.Add(1)
				var request struct{ Model string }
				if json.NewDecoder(r.Body).Decode(&request) != nil || request.Model != "provider-model" || r.Header.Get("Authorization") != "Bearer crash-fixture-upstream" {
					t.Error("child bypassed actual target preparation")
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, historyDocument)
			}))
			defer upstream.Close()
			directory := t.TempDir()
			database := config.DatabaseConfig{Driver: "sqlite", Path: filepath.Join(directory, "usage-crash.db")}
			st, err := store.Open(database)
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			user, err := st.CreateUser(&store.UserInput{Username: "history-crash-owner", Password: "fixture-password-123", ModelMode: access.Allowlist, Models: []string{"m"}})
			if err != nil {
				t.Fatal(err)
			}
			key, err := st.IssueToken(user.ID, &store.TokenInput{Name: "history-crash-key", Models: []string{"m"}})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := st.CreateChannel(&store.ChannelInput{Name: "history-crash-upstream", Type: "openai", BaseURL: upstream.URL, Models: []string{"m"}, ModelMapping: map[string]string{"m": "provider-model"}, GroupName: "crash-sale-group", Weight: 1, APIKey: "crash-fixture-upstream"}); err != nil {
				t.Fatal(err)
			}
			balances := []model.BillingBalance{{OwnerType: "user", OwnerID: user.ID, UsedUnits: 37, ReservedUnits: 11}, {OwnerType: "key", OwnerID: key.ID, UsedUnits: 37, ReservedUnits: 11}}
			if err := st.DB().Create(&balances).Error; err != nil {
				t.Fatal(err)
			}
			payload := `{"format":1,"unit_scale":1000000000,"prices":[],"rate_units":{}}`
			digest := sha256.Sum256([]byte(payload))
			priceVersion := hex.EncodeToString(digest[:])
			if err := st.RecordPriceSnapshot(priceVersion, payload); err != nil {
				t.Fatal(err)
			}
			hold := model.BillingReservation{ID: "prior-unknown-hold", UserID: user.ID, TokenID: key.ID, OriginModel: "m", PriceVersion: priceVersion, State: "pending", ReservedUnits: 11, ProviderBudgetUnits: 19, ProviderHeldUnits: 7, LeaseUntil: time.Now().Add(time.Hour).UnixMilli()}
			if err := st.DB().Create(&hold).Error; err != nil {
				t.Fatal(err)
			}
			before := readHistoryKillFinancial(t, st)
			if err := st.Close(); err != nil {
				t.Fatal(err)
			}
			executable, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			barrierPath := filepath.Join(directory, "barrier.json")
			ctx, cancel := context.WithTimeout(context.Background(), 14*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, executable, "-test.run=^TestUsageRecordingProcessKillHelper$", "-test.timeout=15s")
			command.Env = append(os.Environ(), "TXAPI_USAGE_HISTORY_KILL_STAGE="+stage, "TXAPI_USAGE_HISTORY_KILL_DB="+database.Path, "TXAPI_USAGE_HISTORY_KILL_KEY="+key.Key, "TXAPI_USAGE_HISTORY_KILL_BARRIER="+barrierPath)
			var output historyKillOutput
			command.Stdout, command.Stderr = &output, &output
			if err := command.Start(); err != nil {
				t.Fatal(err)
			}
			waited := make(chan error, 1)
			go func() { waited <- command.Wait() }()
			var stopOnce sync.Once
			stop := func() {
				stopOnce.Do(func() {
					if err := command.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
						t.Errorf("could not kill crash helper: %v", err)
					}
					select {
					case <-waited:
					case <-time.After(time.Second):
						t.Error("killed crash helper did not exit")
					}
				})
			}
			defer stop()
			var barrier historyKillBarrier
			ticker := time.NewTicker(10 * time.Millisecond)
			defer ticker.Stop()
			for {
				if data, err := os.ReadFile(barrierPath); err == nil {
					if err := json.Unmarshal(data, &barrier); err != nil {
						stop()
						t.Fatal(err)
					}
					break
				}
				select {
				case err := <-waited:
					stopOnce.Do(func() {})
					t.Fatalf("crash helper exited before barrier: %v\n%s", err, output.String())
				case <-ctx.Done():
					stop()
					t.Fatalf("crash helper did not reach %s: %v\n%s", stage, ctx.Err(), output.String())
				case <-ticker.C:
				}
			}
			stop()
			if barrier.Stage != stage || barrier.ID == "" || barrier.ProviderUsageObserved != (stage == "before-observe") {
				t.Fatal("invalid reached boundary", barrier)
			}
			wantHits := int64(0)
			if stage == "before-observe" {
				wantHits = 1
			}
			if hits.Load() != wantHits {
				t.Fatal("crash stage changed dispatch count", hits.Load(), wantHits)
			}
			reopened, err := store.Open(database)
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			var invocation model.UsageInvocation
			if err := reopened.DB().First(&invocation, "id = ?", barrier.ID).Error; err != nil {
				t.Fatal(err)
			}
			if invocation.State != "open" || invocation.UserID != user.ID || invocation.TokenID != key.ID || invocation.FinishFingerprint != "" {
				t.Fatal("killed invocation lost real ownership or falsely sealed", invocation)
			}
			if err := reopened.InterruptUsageHistory(context.Background()); err != nil {
				t.Fatal(err)
			}
			if err := reopened.DB().First(&invocation, "id = ?", barrier.ID).Error; err != nil {
				t.Fatal(err)
			}
			if invocation.State != "interrupted" {
				t.Fatal("restart pretended metadata was complete", invocation)
			}
			var attempts []model.UsageAttemptReceipt
			if err := reopened.DB().Where("invocation_id = ?", barrier.ID).Order("number").Find(&attempts).Error; err != nil {
				t.Fatal(err)
			}
			wantAttempts := 1
			if stage == "after-begin" {
				wantAttempts = 0
			}
			if len(attempts) != wantAttempts {
				t.Fatal("restart invented or lost prepared attempts", attempts)
			}
			for _, attempt := range attempts {
				if attempt.State != "interrupted" || !attempt.MayDispatch || attempt.Host.Phase != v1.Dispatching || attempt.ActualModel != "provider-model" || attempt.Group != "crash-sale-group" || attempt.FinishFingerprint != "" {
					t.Fatal("restart changed possible-dispatch target facts", attempt)
				}
				if attempt.InputTokens != nil || attempt.OutputTokens != nil || attempt.TotalTokens != nil || attempt.CachedInputTokens != nil || attempt.CacheWriteInputTokens != nil || attempt.ReasoningOutputTokens != nil || attempt.Completeness != string(v2.Unknown) || attempt.Source != string(v2.SourceUnknown) || attempt.Profile != "" {
					t.Fatal("restart fabricated zeros or recovered an unwritten provider snapshot", attempt)
				}
			}
			if after := readHistoryKillFinancial(t, reopened); !reflect.DeepEqual(before, after) {
				t.Fatal("metadata recording/recovery changed prior financial authority", before, after)
			}
			if hits.Load() != wantHits {
				t.Fatal("metadata restart replayed generation", hits.Load())
			}
		})
	}
}
