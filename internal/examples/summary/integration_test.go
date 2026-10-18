package summary_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"minigateway/internal/access"
	"minigateway/internal/billing"
	"minigateway/internal/compat/httpoutput"
	"minigateway/internal/compat/summarycodec"
	"minigateway/internal/config"
	"minigateway/internal/examples/summary"
	execution "minigateway/internal/foundation/contracts/execution/v1"
	identity "minigateway/internal/foundation/contracts/identity/v1"
	identityimpl "minigateway/internal/foundation/identity"
	"minigateway/internal/gateway"
	"minigateway/internal/model"
	"minigateway/internal/store"
)

const input = `{"model":"m","text":"Keep the native operation"}`
const completed = `{"summary":"Native summary","usage":{"prompt_tokens":4,"completion_tokens":2,"total_tokens":6}}`

type fixture struct {
	service summary.Service
	store   *store.Store
	context context.Context
	key     *model.Token
	gateway *gateway.Gateway
}

// Only this test harness imports implementations. The feature and codec are
// separately checked production packages and cannot acquire these dependencies.
func assemble(t *testing.T, url string, operations []execution.OperationRegistration, protocols []execution.ProtocolRegistration, mode billing.Mode) fixture {
	t.Helper()
	st, err := store.Open(config.DatabaseConfig{Driver: "sqlite", Path: filepath.Join(t.TempDir(), "summary.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	user, err := st.CreateUser(&store.UserInput{Username: "summary-user", Password: "example-password-123", ModelMode: access.Allowlist, Models: []string{"m"}, ConcurrencyLimit: 1})
	if err != nil {
		t.Fatal(err)
	}
	key, err := st.IssueToken(user.ID, &store.TokenInput{Name: "summary-key", Models: []string{"m"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateChannel(&store.ChannelInput{Name: "summary-upstream", Type: "openai", BaseURL: url,
		Models: []string{"m"}, ModelMapping: map[string]string{"m": "provider-summary"}, Weight: 1, APIKey: "fixture-upstream-key"}); err != nil {
		t.Fatal(err)
	}
	channels, err := st.ChannelSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	capacity, err := access.NewCapacity(access.CapacityConfig{Instance: 2, User: 1, Key: 1, Channel: 1})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(capacity.Close)
	g, err := gateway.New(gateway.Options{Channels: channels, BillingMode: mode, Operations: operations, Protocols: protocols,
		Admission: gateway.CapacityAdmission{Capacity: capacity}, TotalTimeout: 3 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(g.Close)
	ctx, _, ok := identityimpl.NewTokenAuthenticator(st).Authenticate(context.Background(), key.Key)
	if !ok {
		t.Fatal("real token authentication failed")
	}
	return fixture{service: summary.Service{Authority: identityimpl.New(st.ResolvePrincipal), Executor: gateway.NewPublicExecutor(g)},
		store: st, context: ctx, key: key, gateway: g}
}

func registered(t *testing.T, url string) fixture {
	return assemble(t, url, []execution.OperationRegistration{summarycodec.Registration(summary.Operation)},
		[]execution.ProtocolRegistration{summarycodec.Protocol("openai", summary.Operation)}, billing.ModeDisabled)
}

func run(f fixture, body string) (execution.Outcome, *httptest.ResponseRecorder, error) {
	recorder := httptest.NewRecorder()
	out, err := f.service.Run(f.context, httpoutput.New(recorder), []byte(body))
	return out, recorder, err
}

func assertCompleted(t *testing.T, out execution.Outcome, recorder *httptest.ResponseRecorder, err error) {
	t.Helper()
	if err != nil || out.StatusCode != 200 || !out.Committed || !out.ProtocolComplete || out.Termination != execution.Success ||
		out.Operation != summary.Operation || out.Usage == nil || out.Usage.TotalTokens != 6 || len(out.Attempts) != 1 || recorder.Body.String() != completed {
		t.Fatalf("independent execution lost wire/output facts: err=%v outcome=%+v body=%s", err, out, recorder.Body)
	}
}

func TestIndependentFeatureUsesRealFoundationAndHTTPOutput(t *testing.T) {
	var hits atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		body, _ := io.ReadAll(r.Body)
		var data struct{ Model, Text string }
		if json.Unmarshal(body, &data) != nil || data.Model != "provider-summary" || data.Text != "Keep the native operation" ||
			r.URL.Path != "/summary" || r.Method != "POST" || r.Header.Get("Authorization") != "Bearer fixture-upstream-key" {
			t.Errorf("codec lost target mapping, credentials or native payload: %s", body)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, completed)
	}))
	defer upstream.Close()
	f := registered(t, upstream.URL)
	for range 2 {
		out, recorder, err := run(f, input)
		assertCompleted(t, out, recorder, err)
		if out.Identity.UserID != f.key.UserID || out.Identity.TokenID != f.key.ID || out.ActualModel != "provider-summary" {
			t.Fatal("current identity or selected model was lost")
		}
	}
	// A real inbound HTTP consumer uses the same public executor/output contracts.
	inbound := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(io.LimitReader(r.Body, 4097))
		_, err := f.service.Run(f.context, httpoutput.New(w), body)
		if err != nil {
			http.Error(w, "example authorization failed", 401)
		}
	}))
	defer inbound.Close()
	response, err := inbound.Client().Post(inbound.URL, "application/json", strings.NewReader(input))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if response.StatusCode != 200 || !bytes.Equal(body, []byte(completed)) || hits.Load() != 3 {
		t.Fatalf("real HTTP consumer did not preserve completion: %d %s hits=%d", response.StatusCode, body, hits.Load())
	}
}

func TestIndependentFeatureRejectsUntrustedRevokedAndUnregisteredRequests(t *testing.T) {
	var hits atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		_, _ = io.WriteString(w, completed)
	}))
	defer upstream.Close()
	f := registered(t, upstream.URL)
	// A caller may carry a copied DTO, but it cannot create trusted authentication.
	fake := context.WithValue(context.Background(), struct{}{}, identity.Principal{UserID: f.key.UserID, TokenID: f.key.ID})
	if _, err := f.service.Run(fake, httpoutput.New(httptest.NewRecorder()), []byte(input)); !errors.Is(err, identity.ErrUnauthorized) {
		t.Fatal("copied principal authenticated a context")
	}
	for _, body := range []string{`{"model":"m","model":"m","text":"x"}`, `{"model":"m","text":"x","stream":true}`, `{"model":"forbidden","text":"x"}`} {
		out, _, err := run(f, body)
		if err != nil || out.StatusCode < 400 || len(out.Attempts) != 0 {
			t.Fatalf("invalid/unauthorized input dispatched: %+v err=%v", out, err)
		}
	}
	if err := f.store.DeleteOwnedToken(f.key.UserID, f.key.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := run(f, input); !errors.Is(err, identity.ErrUnauthorized) {
		t.Fatal("old authenticated context survived current key revocation")
	}
	for _, missing := range []string{"operation", "protocol", "strict"} {
		t.Run(missing, func(t *testing.T) {
			op := summary.Operation
			protocolID := "openai"
			mode := billing.ModeDisabled
			if missing == "operation" {
				op = "example.other"
			}
			if missing == "protocol" {
				protocolID = "unselected-protocol"
			}
			if missing == "strict" {
				mode = billing.ModeStrict
			}
			other := assemble(t, upstream.URL, []execution.OperationRegistration{summarycodec.Registration(op)},
				[]execution.ProtocolRegistration{summarycodec.Protocol(protocolID, op)}, mode)
			out, _, err := run(other, input)
			if err != nil || out.StatusCode < 400 || len(out.Attempts) != 0 || out.Phase != execution.NotDispatched {
				t.Fatalf("missing capability or strict proof dispatched: %+v err=%v", out, err)
			}
		})
	}
	if hits.Load() != 0 {
		t.Fatalf("rejected calls reached upstream: %d", hits.Load())
	}
}

func TestIndependentFeatureReleasesRealAdmissionAfterCompletion(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var hits atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits.Add(1) == 1 {
			close(entered)
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
		}
		_, _ = io.WriteString(w, completed)
	}))
	defer upstream.Close()
	f := registered(t, upstream.URL)
	type result struct {
		out execution.Outcome
		rec *httptest.ResponseRecorder
		err error
	}
	first := make(chan result, 1)
	go func() { out, rec, err := run(f, input); first <- result{out, rec, err} }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("first execution never reached upstream")
	}
	out, _, err := run(f, input)
	if err != nil || out.StatusCode != 429 || len(out.Attempts) != 0 || hits.Load() != 1 {
		t.Fatalf("real per-user/key capacity was bypassed: %+v err=%v", out, err)
	}
	close(release)
	select {
	case got := <-first:
		assertCompleted(t, got.out, got.rec, got.err)
	case <-time.After(5 * time.Second):
		t.Fatal("first execution did not release")
	}
	out, rec, err := run(f, input)
	assertCompleted(t, out, rec, err)
	if hits.Load() != 2 {
		t.Fatal("successful completion did not return capacity")
	}
}
