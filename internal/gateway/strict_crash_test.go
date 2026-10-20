package gateway

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"minigateway/internal/access"
	"minigateway/internal/billing"
	"minigateway/internal/config"
	execution "minigateway/internal/foundation/contracts/execution/v1"
	authentication "minigateway/internal/foundation/identity"
	dbmodel "minigateway/internal/model"
	"minigateway/internal/server/middleware"
	"minigateway/internal/store"
)

type strictCrashSettings struct {
	Database, Upstream, Ready, Marker, Stage string
}

type strictCrashBilling struct {
	StrictBilling
	settings strictCrashSettings
}

func (p strictCrashBilling) checkpoint(stage string, pre *billing.PreDeduct) {
	if p.settings.Stage != stage {
		return
	}
	if err := os.WriteFile(p.settings.Marker, []byte(pre.BillingID()), 0600); err != nil {
		panic(err)
	}
	// Only the test wrapper stops here. The parent must Kill and Wait; no
	// graceful finalizer, fake clock or production crash switch runs instead.
	select {}
}

func (p strictCrashBilling) Reserve(ctx context.Context, key, owner uint, model string) (*billing.PreDeduct, error) {
	pre, err := p.StrictBilling.Reserve(ctx, key, owner, model)
	if err == nil {
		p.checkpoint("reserved", pre)
	}
	return pre, err
}

func (p strictCrashBilling) StartPreparedAttempt(ctx context.Context, pre *billing.PreDeduct, attempt dbmodel.BillingAttempt, target billing.ProviderTarget, request billing.StrictRequest, url string, body []byte, header http.Header) error {
	if err := p.StrictBilling.StartPreparedAttempt(ctx, pre, attempt, target, request, url, body, header); err != nil {
		return err
	}
	p.checkpoint("prepared", pre)
	return nil
}

func (p strictCrashBilling) EndAttempt(ctx context.Context, pre *billing.PreDeduct, number, status int, termination string, usage *execution.Usage) error {
	p.checkpoint("before-receipt", pre)
	if err := p.StrictBilling.EndAttempt(ctx, pre, number, status, termination, usage); err != nil {
		return err
	}
	p.checkpoint("receipt", pre)
	return nil
}

func (p strictCrashBilling) Finish(ctx context.Context, pre *billing.PreDeduct, usage *execution.Usage, model, group string, complete bool) (int64, string, error) {
	charge, state, err := p.StrictBilling.Finish(ctx, pre, usage, model, group, complete)
	if err == nil {
		p.checkpoint("settled", pre)
	}
	return charge, state, err
}

func strictCrashEngine(t *testing.T, st *store.Store) *billing.Engine {
	t.Helper()
	engine := billing.New(st.DB(), billing.NewPriceTableWithHistory(st.PriceData, time.Hour, st.RecordPriceSnapshot))
	if err := engine.EnableStrict("20", 2*time.Second); err != nil {
		t.Fatal(err)
	}
	return engine
}

func TestStrictGatewayCrashProcess(t *testing.T) {
	path := os.Getenv("TXAPI_TEST_STRICT_GATEWAY_CRASH")
	if path == "" {
		t.Skip("subprocess helper")
	}
	var settings strictCrashSettings
	raw, err := os.ReadFile(path)
	if err != nil || json.Unmarshal(raw, &settings) != nil {
		t.Fatal("invalid child settings", err)
	}
	st, err := store.Open(config.DatabaseConfig{Path: settings.Database})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	engine := strictCrashEngine(t, st)
	channels, client := verifiedChatChannels(t, st.Channels(), settings.Upstream)
	configureVerifiedChatCosts(t, engine, channels)
	capacity, err := access.NewCapacity(access.CapacityConfig{Instance: 3, User: 2, Key: 1, Channel: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer capacity.Close()
	g, err := newRegisteredGateway(Options{Channels: channels, Client: client, BillingMode: billing.ModeStrict,
		StrictBilling: strictCrashBilling{StrictBilling: engine, settings: settings},
		Admission:     CapacityAdmission{Capacity: capacity}, Limiter: access.NewMemoryLimiter(),
		AttemptTimeout: 15 * time.Second, TotalTimeout: 15 * time.Second, RetryTimes: 2})
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	router := gin.New()
	router.POST("/v1/chat/completions", middleware.NewAuth(authentication.NewTokenAuthenticator(st)).Handle(), g.HandleChatCompletions)
	edge := httptest.NewServer(router)
	defer edge.Close()
	if err := os.WriteFile(settings.Ready, []byte(edge.URL), 0600); err != nil {
		t.Fatal(err)
	}
	select {}
}

func strictCrashWait(t *testing.T, ctx context.Context, label string, predicate func() bool) {
	t.Helper()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for !predicate() {
		select {
		case <-ctx.Done():
			t.Fatal("deadline waiting for", label)
		case <-ticker.C:
		}
	}
}

func strictCrashImage(t *testing.T, st *store.Store) ([]byte, dbmodel.BillingReservation, []dbmodel.BillingAttempt) {
	t.Helper()
	var reservations []dbmodel.BillingReservation
	var attempts []dbmodel.BillingAttempt
	var balances []dbmodel.BillingBalance
	for _, read := range []func() error{
		func() error { return st.DB().Order("id").Find(&reservations).Error },
		func() error { return st.DB().Order("reservation_id, number").Find(&attempts).Error },
		func() error { return st.DB().Order("owner_type, owner_id").Find(&balances).Error },
	} {
		if err := read(); err != nil {
			t.Fatal(err)
		}
	}
	if len(reservations) != 1 || len(balances) != 2 {
		t.Fatalf("split ledger authority: reservations=%d balances=%d", len(reservations), len(balances))
	}
	image, err := json.Marshal([]any{reservations, attempts, balances})
	if err != nil {
		t.Fatal(err)
	}
	return image, reservations[0], attempts
}

func TestStrictGatewayProcessKillFinancialBoundaries(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, stage := range []string{"reserved", "prepared", "received", "before-receipt", "receipt", "settled"} {
			if stage == "before-receipt" && !stream {
				continue // SSE has an observable terminal before HTTP handler return.
			}
			t.Run(fmt.Sprintf("stream=%v/%s", stream, stage), func(t *testing.T) {
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				directory := t.TempDir()
				settings := strictCrashSettings{Database: filepath.Join(directory, "strict.db"), Stage: stage,
					Ready: filepath.Join(directory, "ready"), Marker: filepath.Join(directory, "marker")}
				var hits, active atomic.Int64
				received, upstreamEnded := make(chan struct{}, 1), make(chan struct{}, 1)
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					active.Add(1)
					defer func() {
						active.Add(-1)
						select {
						case upstreamEnded <- struct{}{}:
						default:
						}
					}()
					raw, err := io.ReadAll(io.LimitReader(r.Body, 65537))
					if err != nil || len(raw) > 65536 || r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer isolated-crash-provider" {
						t.Error("invalid wire request", err)
						w.WriteHeader(400)
						return
					}
					var input struct {
						Model  string `json:"model"`
						Max    int    `json:"max_completion_tokens"`
						Tier   string `json:"service_tier"`
						Stream bool   `json:"stream"`
					}
					if json.Unmarshal(raw, &input) != nil || input.Model != verifiedChatModel || input.Max != 32 || input.Tier != "default" || input.Stream != stream {
						t.Error("strict wire proof did not reach actual upstream")
						w.WriteHeader(400)
						return
					}
					hits.Add(1)
					select {
					case received <- struct{}{}:
					default:
					}
					if stage == "received" {
						select {
						case <-r.Context().Done():
						case <-ctx.Done():
						}
						return
					}
					if stream {
						w.Header().Set("Content-Type", "text/event-stream")
						for _, frame := range []string{
							`data: {"choices":[{"delta":{"content":"fixture"}}]}`,
							`data: {"choices":[{"delta":{},"finish_reason":"stop"}]}`,
							`data: {"choices":[],"usage":{"prompt_tokens":11,"completion_tokens":7,"total_tokens":18}}`,
							`data: [DONE]`,
						} {
							fmt.Fprint(w, frame+"\n\n")
							w.(http.Flusher).Flush()
						}
					} else {
						w.Header().Set("Content-Type", "application/json")
						fmt.Fprint(w, `{"choices":[{"message":{"content":"fixture"},"finish_reason":"stop"}],"usage":{"prompt_tokens":11,"completion_tokens":7,"total_tokens":18}}`)
					}
				}))
				defer upstream.Close()
				settings.Upstream = upstream.URL
				st, err := store.Open(config.DatabaseConfig{Path: settings.Database})
				if err != nil {
					t.Fatal(err)
				}
				defer st.Close()
				owner, err := st.CreateUser(&store.UserInput{Username: "crash-owner", Password: "isolated-crash-password", ModelMode: access.All, Quota: 100})
				if err != nil {
					t.Fatal(err)
				}
				key, err := st.CreateToken(&store.TokenInput{UserID: owner.ID, Key: "sk-isolated-crash-client", Name: "crash", ModelMode: access.All, Quota: 100})
				if err != nil {
					t.Fatal(err)
				}
				if _, err := st.CreateChannel(&store.ChannelInput{Name: "crash", Type: "openai", BaseURL: "https://api.openai.com", Models: []string{"gpt-4o-mini"}, APIKey: "isolated-crash-provider"}); err != nil {
					t.Fatal(err)
				}
				if err := st.UpsertModelPrice(&dbmodel.ModelPrice{Model: "gpt-4o-mini", InputPricePer1K: .15, OutputPricePer1K: .6}); err != nil {
					t.Fatal(err)
				}
				if err := st.Close(); err != nil {
					t.Fatal(err)
				}
				raw, _ := json.Marshal(settings)
				path := filepath.Join(directory, "child.json")
				if err := os.WriteFile(path, raw, 0600); err != nil {
					t.Fatal(err)
				}
				log, err := os.Create(filepath.Join(directory, "child.log"))
				if err != nil {
					t.Fatal(err)
				}
				defer log.Close()
				command := exec.Command(os.Args[0], "-test.run=^TestStrictGatewayCrashProcess$", "-test.count=1", "-test.timeout=60s")
				command.Env = append(os.Environ(), "TXAPI_TEST_STRICT_GATEWAY_CRASH="+path)
				command.Stdout, command.Stderr = log, log
				if err := command.Start(); err != nil {
					t.Fatal(err)
				}
				processDone := make(chan struct{})
				var waitErr error
				go func() { waitErr = command.Wait(); close(processDone) }()
				defer func() {
					_ = command.Process.Kill()
					select {
					case <-processDone:
					case <-time.After(5 * time.Second):
						t.Error("child did not join after Kill")
					}
					if t.Failed() {
						body, _ := os.ReadFile(log.Name())
						t.Logf("child diagnostics: %s", body)
					}
				}()
				var edge string
				strictCrashWait(t, ctx, "child HTTP listener", func() bool {
					value, err := os.ReadFile(settings.Ready)
					edge = string(value)
					return err == nil && strings.HasPrefix(edge, "http://127.0.0.1:")
				})
				body := fmt.Sprintf(`{"model":"gpt-4o-mini","messages":[{"role":"user","content":"hello"}],"max_completion_tokens":32,"service_tier":"default","stream":%v}`, stream)
				protocolDone, requestDone := make(chan []byte, 1), make(chan error, 1)
				requestFinished := make(chan struct{})
				go func() {
					defer close(requestFinished)
					req, err := http.NewRequestWithContext(ctx, http.MethodPost, edge+"/v1/chat/completions", strings.NewReader(body))
					if err != nil {
						requestDone <- err
						return
					}
					req.Header.Set("Authorization", "Bearer sk-isolated-crash-client")
					response, err := http.DefaultClient.Do(req)
					if err != nil {
						requestDone <- err
						return
					}
					defer response.Body.Close()
					if response.StatusCode != 200 {
						requestDone <- fmt.Errorf("status %d", response.StatusCode)
						return
					}
					if stream {
						reader := bufio.NewReader(io.LimitReader(response.Body, 65537))
						var observed bytes.Buffer
						for observed.Len() < 65536 {
							line, err := reader.ReadString('\n')
							observed.WriteString(line)
							if strings.HasSuffix(observed.String(), "data: [DONE]\n\n") {
								protocolDone <- bytes.Clone(observed.Bytes())
								break
							}
							if err != nil {
								requestDone <- err
								return
							}
						}
						_, err = io.Copy(io.Discard, reader)
					} else {
						_, err = io.Copy(io.Discard, io.LimitReader(response.Body, 65537))
					}
					requestDone <- err
				}()
				defer func() {
					cancel()
					select {
					case <-requestFinished:
					case <-time.After(3 * time.Second):
						t.Error("request goroutine did not join")
					}
				}()
				if stage == "received" {
					select {
					case <-received:
					case <-ctx.Done():
						t.Fatal("upstream did not accept request")
					}
				} else {
					strictCrashWait(t, ctx, "durable "+stage, func() bool { value, err := os.ReadFile(settings.Marker); return err == nil && len(value) == 32 })
				}
				if stage == "before-receipt" {
					select {
					case observed := <-protocolDone:
						if !bytes.Contains(observed, []byte(`"content":"fixture"`)) || !bytes.Contains(observed, []byte(`"total_tokens":18`)) || bytes.Count(observed, []byte("data: [DONE]")) != 1 {
							t.Fatal("client did not observe one complete stream with native usage")
						}
					case <-ctx.Done():
						t.Fatal("no client protocol terminal before receipt")
					}
				}
				if err := command.Process.Kill(); err != nil {
					t.Fatal(err)
				}
				select {
				case <-processDone:
					if waitErr == nil {
						t.Fatal("child exited gracefully")
					}
				case <-ctx.Done():
					t.Fatal("killed child did not join")
				}
				cancel()
				select {
				case <-requestDone:
				case <-time.After(3 * time.Second):
					t.Fatal("client read did not join after process death")
				}
				wantHits := int64(1)
				if stage == "reserved" || stage == "prepared" {
					wantHits = 0
				}
				if hits.Load() != wantHits {
					t.Fatalf("wrong execution window: hits=%d want=%d", hits.Load(), wantHits)
				}
				if wantHits == 1 {
					select {
					case <-upstreamEnded:
					case <-time.After(3 * time.Second):
						t.Fatal("killed process left upstream active")
					}
				}
				if active.Load() != 0 {
					t.Fatal("upstream handlers remain active after their completion notification")
				}
				strictCrashRecover(t, settings, stage, stream, owner.ID, key.ID, hits.Load())
				if hits.Load() != wantHits {
					t.Fatal("recovery resent unknown generation")
				}
			})
		}
	}
}

func strictCrashRecover(t *testing.T, settings strictCrashSettings, stage string, stream bool, owner, key uint, hits int64) {
	t.Helper()
	st, err := store.Open(config.DatabaseConfig{Path: settings.Database})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	_, original, attempts := strictCrashImage(t, st)
	wantAttempts := 1
	if stage == "reserved" {
		wantAttempts = 0
	}
	if len(attempts) != wantAttempts || original.PriceVersion == "" || original.ReservedUnits != 20*billing.UnitScale {
		t.Fatalf("durable boundary missing: %+v attempts=%+v", original, attempts)
	}
	known := stage == "receipt" || stage == "settled"
	wantOriginalState, wantOriginalCharge := billing.Active, int64(0)
	if stage == "reserved" {
		wantOriginalState = billing.Reserved
	}
	if stage == "settled" {
		wantOriginalState, wantOriginalCharge = billing.Settled, 5_850_000
	}
	if original.State != wantOriginalState || original.ChargeUnits != wantOriginalCharge {
		t.Fatalf("did not kill at the declared durable boundary: %+v", original)
	}
	if len(attempts) == 1 {
		a := attempts[0]
		hash := sha256.Sum256([]byte(a.ProviderContractSnapshot))
		if a.ProviderBoundUnits != 19_219_200 || original.ProviderHeldUnits != a.ProviderBoundUnits || a.ProviderContractVersion != hex.EncodeToString(hash[:]) || a.CostKnown != known || a.ActualModel != verifiedChatModel {
			t.Fatalf("lost actual wire cost proof: %+v row=%+v", a, original)
		}
		if known && (a.CostUnits != 5_850_000 || a.PromptTokens != 11 || a.OutputTokens != 7 || a.State != "finished") {
			t.Fatalf("known receipt lost: %+v", a)
		}
		if !known && (a.CostKnown || a.PromptTokens != 0 || a.OutputTokens != 0 || a.State != billing.Active) {
			t.Fatalf("undurable client receipt became financial fact: %+v", a)
		}
	}
	if err := st.UpsertModelPrice(&dbmodel.ModelPrice{Model: "gpt-4o-mini", InputPricePer1K: 1, OutputPricePer1K: 1}); err != nil {
		t.Fatal(err)
	}
	if wait := time.Until(time.UnixMilli(original.LeaseUntil)); wait > 0 {
		time.Sleep(wait + 10*time.Millisecond)
	}
	engine := strictCrashEngine(t, st)
	if current, found := engine.PriceOf(&execution.Usage{PromptTokens: 11, CompletionTokens: 7, TotalTokens: 18}, "gpt-4o-mini", "gpt-4o-mini", ""); !found || current != .018 {
		t.Fatalf("current quote was not changed before old receipt recovery: %v %v", current, found)
	}
	for range 2 {
		if err := engine.RecoverExpired(context.Background(), time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	image, row, recoveredAttempts := strictCrashImage(t, st)
	wantState, used, reserved := billing.Pending, int64(0), int64(20*billing.UnitScale)
	if stage == "reserved" {
		wantState, reserved = billing.Settled, 0
	}
	if known {
		wantState, used, reserved = billing.Settled, 5_850_000, 0
	}
	if row.State != wantState || row.ChargeUnits != used || row.PriceVersion != original.PriceVersion || row.ProviderHeldUnits != original.ProviderHeldUnits {
		t.Fatalf("unsafe crash recovery: row=%+v original=%+v", row, original)
	}
	before, _ := json.Marshal(attempts)
	after, _ := json.Marshal(recoveredAttempts)
	if !bytes.Equal(before, after) {
		t.Fatal("recovery rewrote immutable attempt proof")
	}
	for _, target := range []struct {
		kind string
		id   uint
	}{{"user", owner}, {"key", key}} {
		var balance dbmodel.BillingBalance
		if err := st.DB().First(&balance, "owner_type=? AND owner_id=?", target.kind, target.id).Error; err != nil {
			t.Fatal(err)
		}
		if balance.UsedUnits != used || balance.ReservedUnits != reserved {
			t.Fatalf("split dual balances: %+v", balance)
		}
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	st, err = store.Open(config.DatabaseConfig{Path: settings.Database})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	engine = strictCrashEngine(t, st)
	for range 2 {
		if err := engine.RecoverExpired(context.Background(), time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	again, _, _ := strictCrashImage(t, st)
	if !bytes.Equal(image, again) {
		t.Fatal("second reopen/recovery changed financial facts")
	}
	t.Logf("killed stream=%v stage=%s sends=%d state=%s charge=%d reserved/user/key=%d provider_authorized_history=%d", stream, stage, hits, row.State, used, reserved, row.ProviderHeldUnits)
}
