package gateway

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"minigateway/internal/access"
	"minigateway/internal/billing"
	codec "minigateway/internal/compat/responsesws"
	bridge "minigateway/internal/compat/websocket"
	"minigateway/internal/config"
	v2 "minigateway/internal/foundation/contracts/execution/v2"
	identity "minigateway/internal/foundation/contracts/identity/v1"
	contract "minigateway/internal/foundation/contracts/responsesws/v1"
	authority "minigateway/internal/foundation/identity"
	"minigateway/internal/model"
	"minigateway/internal/store"
	transport "minigateway/internal/transport/websocket"
)

type nativeStrictCrashResource struct{ manager *transport.Manager }

func (r nativeStrictCrashResource) Dial(context.Context, string) (v2.Connection, error) {
	return nil, fmt.Errorf("strict crash consumer requires the selected target")
}
func (r nativeStrictCrashResource) DialTarget(ctx context.Context, endpoint, key string) (v2.Connection, error) {
	c, err := r.manager.Dial(ctx, endpoint, http.Header{"Authorization": {"Bearer " + key}, "OpenAI-Beta": {"responses_websockets=2025-06-01"}})
	if err != nil {
		return nil, err
	}
	return bridge.Adapt("strict-crash-upstream", c), nil
}
func (r nativeStrictCrashResource) Close(ctx context.Context) error { return r.manager.Close(ctx) }

func nativeStrictCrashEngine(t *testing.T, st *store.Store) *billing.Engine {
	t.Helper()
	e := billing.New(st.DB(), billing.NewPriceTableWithHistory(st.PriceData, time.Hour, st.RecordPriceSnapshot))
	if err := e.EnableStrict("1", 2*time.Second); err != nil {
		t.Fatal(err)
	}
	return e
}

func TestNativeStrictCrashProcess(t *testing.T) {
	path := os.Getenv("TXAPI_TEST_STRICT_WS_CRASH")
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
	e := nativeStrictCrashEngine(t, st)
	channels := st.Channels()
	if len(channels) != 1 || len(channels[0].Credentials) != 1 {
		t.Fatal("missing isolated static channel")
	}
	channel, credential := channels[0], channels[0].Credentials[0]
	digest := sha256.Sum256([]byte(credential.APIKey))
	policy := billing.ProviderCostContract{ChannelID: channel.ID, BaseURL: channel.BaseURL, Protocol: "openai", ActualModel: "actual", Operation: "responses", OutputLimitField: "max_output_tokens", InputTokenCeiling: 1024, InputBoundProfile: billing.OperatorResponsesV1, MaxOutputTokens: 32,
		InputPricePer1K: "0.001", OutputPricePer1K: "0.002", Currency: "USD", TariffProfile: billing.OperatorDeclaredTokensV1,
		CredentialBindings: map[string]string{fmt.Sprint(credential.ID): hex.EncodeToString(digest[:])}, PolicyVersion: "isolated-ws-crash-v1", PolicyExpiresAt: time.Now().Add(time.Hour).Unix(), MaxRequestBytes: 1024, AdditionalCostPerAttempt: "0", Evidence: "Isolated whole-input token and zero-extra-fee fixture declaration"}
	if err := e.ConfigureProviderCosts("1", []billing.ProviderCostContract{policy}); err != nil {
		t.Fatal(err)
	}
	capacity, err := access.NewCapacity(access.CapacityConfig{Instance: 3, User: 2, Key: 1, Channel: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer capacity.Close()
	g, err := newRegisteredGateway(Options{Channels: channels, BillingMode: billing.ModeStrict, StrictBilling: strictCrashBilling{StrictBilling: e, settings: settings}, Admission: CapacityAdmission{Capacity: capacity}})
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	trusted, token, ok := authority.NewTokenAuthenticator(st).Authenticate(context.Background(), "sk-isolated-ws-crash-client")
	if !ok {
		t.Fatal("child identity unavailable")
	}
	s, err := NewNativeWebSocketService(context.Background(), g, NativeWSOptions{Authority: authority.New(st.ResolvePrincipal), Gate: access.NewRateGate(access.NewMemoryLimiter()), Models: []string{"actual"}, Limits: contract.Limits{MaxMessageBytes: 256 << 10, IdleTimeout: time.Second, RoundTimeout: 15 * time.Second, MaxLifetime: time.Minute},
		Prepare: func(context.Context) (NativeWSResource, error) {
			return nativeStrictCrashResource{manager: nativeManager(t)}, nil
		},
		Parse: func(frame v2.Frame) (NativeWSCreate, error) {
			in, err := codec.Parse(frame)
			return NativeWSCreate{Model: in.Model, PreviousID: in.PreviousID, Store: in.Store, PromptText: in.PromptText, Initial: in.Initial}, err
		}, Encode: codec.MapModel, Decoder: func(name string) v2.FrameDecoder { return codec.NewDecoder(name).Decode }})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Publish(trusted, channels); err != nil {
		t.Fatal(err)
	}
	session, problem := s.Open(trusted, contract.Open{Binding: identity.Binding{UserID: token.UserID, TokenID: token.ID}, ConnectionID: "strict-crash-downstream"})
	if problem != nil {
		t.Fatal(problem)
	}
	result := session.Execute(trusted, contract.Create{JSON: []byte(`{"type":"response.create","model":"alias","input":"hello","max_output_tokens":32,"store":false}`)}, func(_ context.Context, frame v2.Frame) (int, error) {
		if bytes.Contains(frame.Data, []byte(`"response.completed"`)) {
			if err := os.WriteFile(settings.Ready, frame.Data, 0600); err != nil {
				return 0, err
			}
		}
		return len(frame.Data), nil
	})
	t.Fatalf("child passed its crash barrier: %+v", result)
}

func TestNativeStrictProcessKillFinancialBoundaries(t *testing.T) {
	for _, stage := range []string{"reserved", "prepared", "received", "before-receipt", "receipt", "settled"} {
		t.Run(stage, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			directory := t.TempDir()
			settings := strictCrashSettings{Database: filepath.Join(directory, "native.db"), Ready: filepath.Join(directory, "terminal"), Marker: filepath.Join(directory, "marker"), Stage: stage}
			manager := nativeManager(t)
			var hits atomic.Int64
			received := make(chan struct{}, 1)
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v1/responses" || r.Header.Get("Authorization") != "Bearer isolated-ws-crash-provider" || r.Header.Get("OpenAI-Beta") != "responses_websockets=2025-06-01" {
					t.Error("unproven websocket handshake")
					w.WriteHeader(400)
					return
				}
				c, err := manager.Accept(w, r)
				if err != nil {
					return
				}
				defer c.Close()
				typ, body, err := c.Read(ctx)
				var in struct {
					Type, Model string
					Lane        string `json:"stream_id"`
					Max         int    `json:"max_output_tokens"`
					Store       bool
				}
				if err != nil || typ != transport.Text || json.Unmarshal(body, &in) != nil || in.Type != "response.create" || in.Model != "actual" || in.Max != 32 || in.Lane != "main" || in.Store {
					t.Error("invalid final native create", string(body), err)
					return
				}
				hits.Add(1)
				received <- struct{}{}
				if stage == "received" {
					_, _, _ = c.Read(ctx)
					return
				}
				for _, event := range []struct{ typ, status string }{{"response.created", "in_progress"}, {"response.completed", "completed"}} {
					data := nativeJSON(map[string]any{"type": event.typ, "stream_id": "main", "response": map[string]any{"id": "response-fixture", "object": "response", "model": "actual", "status": event.status, "output": []any{}, "usage": map[string]any{"input_tokens": 2, "output_tokens": 1, "total_tokens": 3}}})
					if err := c.Write(ctx, transport.Text, data); err != nil {
						t.Error("provider terminal write", err)
						return
					}
				}
			}))
			defer upstream.Close()
			settings.Upstream = upstream.URL
			st, err := store.Open(config.DatabaseConfig{Path: settings.Database})
			if err != nil {
				t.Fatal(err)
			}
			owner, err := st.CreateUser(&store.UserInput{Username: "ws-crash-owner", Password: "isolated-ws-crash-password", ModelMode: access.All, Quota: 100})
			if err != nil {
				t.Fatal(err)
			}
			key, err := st.CreateToken(&store.TokenInput{UserID: owner.ID, Key: "sk-isolated-ws-crash-client", ModelMode: access.All, Quota: 100})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := st.CreateChannel(&store.ChannelInput{Name: "isolated-ws", Type: "openai", BaseURL: upstream.URL, APIKey: "isolated-ws-crash-provider", Models: []string{"alias"}, ModelMapping: map[string]string{"alias": "actual"}}); err != nil {
				t.Fatal(err)
			}
			if err := st.UpsertModelPrice(&model.ModelPrice{Model: "alias", InputPricePer1K: .01, OutputPricePer1K: .02}); err != nil {
				t.Fatal(err)
			}
			if err := st.Close(); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(directory, "child.json")
			data, _ := json.Marshal(settings)
			if err := os.WriteFile(path, data, 0600); err != nil {
				t.Fatal(err)
			}
			log, err := os.Create(filepath.Join(directory, "child.log"))
			if err != nil {
				t.Fatal(err)
			}
			defer log.Close()
			command := exec.Command(os.Args[0], "-test.run=^TestNativeStrictCrashProcess$", "-test.count=1", "-test.timeout=60s")
			command.Env = append(os.Environ(), "TXAPI_TEST_STRICT_WS_CRASH="+path)
			command.Stdout, command.Stderr = log, log
			if err := command.Start(); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { done <- command.Wait() }()
			joined := false
			defer func() {
				_ = command.Process.Kill()
				if !joined {
					select {
					case <-done:
					case <-time.After(5 * time.Second):
						t.Error("native child did not join")
					}
				}
				if t.Failed() {
					diagnostics, _ := os.ReadFile(log.Name())
					t.Logf("native child diagnostics: %s", diagnostics)
				}
			}()
			if stage == "received" {
				select {
				case <-received:
				case <-ctx.Done():
					t.Fatal("no native provider create")
				}
			} else {
				strictCrashWait(t, ctx, "native "+stage, func() bool { value, err := os.ReadFile(settings.Marker); return err == nil && len(value) == 32 })
			}
			if stage == "before-receipt" || stage == "receipt" || stage == "settled" {
				data, err := os.ReadFile(settings.Ready)
				if err != nil || !bytes.Contains(data, []byte(`"response.completed"`)) {
					t.Fatal("no protocol terminal before financial crash barrier", err)
				}
			}
			if err := command.Process.Kill(); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-done:
				joined = true
				if err == nil {
					t.Fatal("native child exited gracefully")
				}
			case <-ctx.Done():
				t.Fatal("killed native child did not join")
			}
			wantHits := int64(1)
			if stage == "reserved" || stage == "prepared" {
				wantHits = 0
			}
			if hits.Load() != wantHits {
				t.Fatalf("native sends=%d want=%d", hits.Load(), wantHits)
			}
			nativeStrictCrashRecover(t, settings, owner.ID, key.ID)
			if hits.Load() != wantHits {
				t.Fatal("recovery replayed native generation")
			}
		})
	}
}

func nativeStrictCrashRecover(t *testing.T, settings strictCrashSettings, owner, key uint) {
	t.Helper()
	st, err := store.Open(config.DatabaseConfig{Path: settings.Database})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	_, original, attempts := strictCrashImage(t, st)
	known := settings.Stage == "receipt" || settings.Stage == "settled"
	wantAttempts := 1
	if settings.Stage == "reserved" {
		wantAttempts = 0
	}
	if len(attempts) != wantAttempts || original.ProviderPolicyVersion == "" || original.ReservedUnits != billing.UnitScale {
		t.Fatalf("lost native reservation boundary: %+v attempts=%+v", original, attempts)
	}
	wantOriginalState, wantOriginalCharge := billing.Active, int64(0)
	if settings.Stage == "reserved" {
		wantOriginalState = billing.Reserved
	}
	if settings.Stage == "settled" {
		wantOriginalState, wantOriginalCharge = billing.Settled, 40_000
	}
	if original.State != wantOriginalState || original.ChargeUnits != wantOriginalCharge || original.ProviderBudgetUnits != billing.UnitScale {
		t.Fatalf("wrong native crash window: %+v", original)
	}
	if wantAttempts == 1 {
		a := attempts[0]
		hash := sha256.Sum256([]byte(a.ProviderContractSnapshot))
		if a.ProviderBoundUnits != 1_088_000 || original.ProviderHeldUnits != a.ProviderBoundUnits || a.ProviderContractVersion != hex.EncodeToString(hash[:]) || a.CostKnown != known || a.ActualModel != "actual" {
			t.Fatalf("lost native actual-wire proof: %+v reservation=%+v", a, original)
		}
		if known && (a.PromptTokens != 2 || a.OutputTokens != 1 || a.CostUnits != 40_000) {
			t.Fatalf("lost native usage: %+v", a)
		}
	}
	if err := st.UpsertModelPrice(&model.ModelPrice{Model: "alias", InputPricePer1K: 2, OutputPricePer1K: 2}); err != nil {
		t.Fatal(err)
	}
	if wait := time.Until(time.UnixMilli(original.LeaseUntil)); wait > 0 {
		time.Sleep(wait + 10*time.Millisecond)
	}
	e := nativeStrictCrashEngine(t, st)
	for range 2 {
		if err := e.RecoverExpired(context.Background(), time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	image, recovered, afterAttempts := strictCrashImage(t, st)
	wantState, used, held := billing.Pending, int64(0), int64(billing.UnitScale)
	if settings.Stage == "reserved" {
		wantState, held = billing.Settled, 0
	}
	if known {
		wantState, used, held = billing.Settled, 40_000, 0
	}
	if recovered.State != wantState || recovered.ChargeUnits != used || recovered.PriceVersion != original.PriceVersion || recovered.ProviderHeldUnits != original.ProviderHeldUnits {
		t.Fatalf("unsafe native financial recovery: %+v", recovered)
	}
	before, _ := json.Marshal(attempts)
	after, _ := json.Marshal(afterAttempts)
	if !bytes.Equal(before, after) {
		t.Fatal("native recovery rewrote attempt proof")
	}
	for _, binding := range []struct {
		kind string
		id   uint
	}{{"user", owner}, {"key", key}} {
		var balance model.BillingBalance
		if err := st.DB().First(&balance, "owner_type=? AND owner_id=?", binding.kind, binding.id).Error; err != nil {
			t.Fatal(err)
		}
		if balance.UsedUnits != used || balance.ReservedUnits != held {
			t.Fatalf("split native dual hold: %+v", balance)
		}
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	st, err = store.Open(config.DatabaseConfig{Path: settings.Database})
	if err != nil {
		t.Fatal(err)
	}
	e = nativeStrictCrashEngine(t, st)
	for range 2 {
		if err := e.RecoverExpired(context.Background(), time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	again, _, _ := strictCrashImage(t, st)
	if !bytes.Equal(image, again) {
		t.Fatal("second native recovery changed financial facts")
	}
	t.Logf("native stage=%s state=%s charge=%d held/user/key=%d", settings.Stage, recovered.State, used, held)
}
