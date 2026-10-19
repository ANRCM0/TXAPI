package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"minigateway/internal/access"
	"minigateway/internal/billing"
	"minigateway/internal/config"
	"minigateway/internal/controlplane"
	managed "minigateway/internal/foundation/contracts/configuration/v2"
	auth "minigateway/internal/foundation/contracts/upstreamauth/v1"
	authv2 "minigateway/internal/foundation/contracts/upstreamauth/v2"
	authv3 "minigateway/internal/foundation/contracts/upstreamauth/v3"
	authentication "minigateway/internal/foundation/identity"
	"minigateway/internal/foundation/persistence"
	"minigateway/internal/foundation/upstreamhttp"
	"minigateway/internal/gateway"
	"minigateway/internal/management"
	"minigateway/internal/protocol"
	"minigateway/internal/protocol/openaiauth"
	"minigateway/internal/server"
	"minigateway/internal/server/api"
	"minigateway/internal/server/middleware"
	"minigateway/internal/store"
)

func TestManagedInferenceAdminCreateRestartAndRealResponsesConsumer(t *testing.T) {
	gin.SetMode(gin.ReleaseMode)
	ctx := context.Background()
	db := config.DatabaseConfig{Path: filepath.Join(t.TempDir(), "managed.db"), CredentialKeysFile: runtimeKeyFile(t, "managed-credentials", 41, 43)}
	st, err := store.Open(db)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	owner, err := st.BootstrapAdmin("managed-admin", "managed-admin-password-fixture")
	if err != nil {
		t.Fatal(err)
	}
	key, err := st.IssueToken(owner.ID, &store.TokenInput{Name: "managed-key", ModelMode: access.All})
	if err != nil {
		t.Fatal(err)
	}
	other, err := st.IssueToken(owner.ID, &store.TokenInput{Name: "other-key", ModelMode: access.All})
	if err != nil {
		t.Fatal(err)
	}
	account, err := st.UpstreamAccounts().SaveAuthorization(ctx, auth.AuthorizationWrite{
		ID: "ua_00000000000000000000000000000081", Provider: "openai", Name: "synthetic SIWC", ClientID: "synthetic-issued-client",
		Identity: auth.Identity{Issuer: "https://auth.openai.com", Subject: "synthetic-verified-subject"},
		Tokens:   auth.Tokens{AccessToken: auth.NewSecret("synthetic-access-token-private"), RefreshToken: auth.NewSecret("synthetic-refresh-token-private"), IDToken: auth.NewSecret("synthetic-id-token-private"), Nonce: auth.NewSecret("synthetic-nonce-private"), ExpiresAt: time.Now().Add(time.Hour), ScopesKnown: true, Scopes: []string{"chatgpt.tokens.use.direct", "openid", "resource.invoke"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	admin, _, err := st.Login("managed-admin", "managed-admin-password-fixture")
	if err != nil {
		t.Fatal(err)
	}
	configuration := management.NewConfiguration(persistence.Configuration(st), nil, nil)
	configuration.SetManagedRepository(st.ManagedChannels())
	configuration.SetEligibilityRepository(st.InferenceEligibility())
	h := api.New(api.Dependencies{Control: controlplane.New(persistence.Control(st)), Configuration: configuration, ManagedConfiguration: configuration})
	e := gin.New()
	h.RegisterAccess(e.Group(""))
	create := fmt.Sprintf(`{"channel":{"name":"managed","type":"openai_siwc","base_url":"https://api.openai.com/v1","models":["synthetic-model"]},"binding":{"account_id":%q,"owner_id":%d,"token_id":%d,"model":"synthetic-model"}}`, account.ID, owner.ID, key.ID)
	r := httptest.NewRequest("POST", "/api/admin/managed-channels", strings.NewReader(create))
	r.Header.Set("Authorization", "Bearer "+admin)
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	e.ServeHTTP(w, r)
	if w.Code != 201 {
		t.Fatalf("managed create: %d %s", w.Code, w.Body)
	}
	var created struct {
		Data managed.ManagedChannel `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.Data.Credential.CredentialID == 0 || created.Data.Credential.Source != "managed_account" {
		t.Fatalf("fake credential: %s", w.Body)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	st, err = store.Open(db)
	if err != nil {
		t.Fatal(err)
	}
	issued, err := st.InferenceCredentials().AcquireAccess(ctx, authv2.AccessRequest{CredentialID: created.Data.Credential.CredentialID, ExpectedBindingRevision: created.Data.Credential.Revision,
		AccountID: account.ID, ExpectedRevision: account.Revision, ExpectedCredentialRevision: account.CredentialRevision, OwnerID: owner.ID, TokenID: key.ID, Model: "synthetic-model"})
	if err != nil || issued.AccountID != account.ID {
		t.Fatalf("restart lost managed authority: %v", err)
	}
	for _, ch := range st.Channels() {
		for _, cred := range ch.Credentials {
			if cred.AccountRevision != account.Revision || cred.CredentialRevision != account.CredentialRevision {
				t.Fatalf("restart lost account revisions: %d/%d expected %d/%d", cred.AccountRevision, cred.CredentialRevision, account.Revision, account.CredentialRevision)
			}
		}
	}
	var catalogs, attempts, rejectStatus atomic.Int64
	client := &http.Client{Transport: generationTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Scheme != "https" || r.URL.Host != "api.openai.com" || r.Header.Get("Authorization") != "Bearer synthetic-access-token-private" {
			t.Error("wrong managed target or credential")
		}
		switch r.URL.Path {
		case "/v1/models":
			catalogs.Add(1)
			if r.Method != "GET" {
				t.Error("wrong directory method")
			}
			return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"models":[{"slug":"synthetic-model","visibility":"list"}]}`))}, nil
		case "/v1/responses":
			attempts.Add(1)
			if status := rejectStatus.Load(); status != 0 {
				return &http.Response{StatusCode: int(status), Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"error":{"code":"subscription_sharing_usage_limit_exceeded","message":"synthetic-secret-body"}}`))}, nil
			}
			body, _ := io.ReadAll(r.Body)
			if r.Method != "POST" || strings.Contains(string(body), "previous_response_id") || !strings.Contains(string(body), `"store":false`) {
				t.Error("wrong managed body", string(body))
			}
			wire := "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"sequence_number\":0,\"delta\":\"hello\"}\n\n" +
				"event: response.completed\ndata: {\"type\":\"response.completed\",\"sequence_number\":1,\"response\":{\"id\":\"synthetic-remote\",\"object\":\"response\",\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n"
			return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(wire))}, nil
		default:
			t.Errorf("unexpected managed request %s", r.URL)
			return nil, fmt.Errorf("unexpected synthetic request")
		}
	})}
	caller := upstreamhttp.NewCaller(client)
	defer caller.Close()
	limiter := access.NewMemoryLimiter()
	defer limiter.(interface{ Close() }).Close()
	capacity, err := access.NewCapacity(access.CapacityConfig{Instance: 8, User: 4, Key: 2, Channel: 2})
	if err != nil {
		t.Fatal(err)
	}
	defer capacity.Close()
	gw, err := gateway.New(gateway.Options{Protocols: protocol.Protocols(nil), Operations: protocol.Operations(), Channels: st.Channels(), BillingMode: billing.ModeDisabled, RetryTimes: 3, Limiter: limiter, Admission: gateway.CapacityAdmission{Capacity: capacity}, Client: client, ManagedInference: &gateway.ManagedInferenceOptions{Enabled: true, Credentials: st.InferenceCredentials(), DirectoryCaller: caller, ValidateModel: openaiauth.ValidateVisibleModel, FailureRecorder: st.InferenceEligibility(), ClassifyFailure: openaiauth.ClassifyInferenceFailure}})
	if err != nil {
		t.Fatal(err)
	}
	defer gw.Close()
	receiptConfig := responsesRuntimeConfig(runtimeKeyFile(t, "managed-content", 47, 53))
	receiptConfig.InstanceRecords, receiptConfig.OwnerRecords = 64, 32
	runtime, err := startResponsesRuntime(receiptConfig, st, gw)
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	configuration = management.NewConfiguration(persistence.Configuration(st), func() error {
		channels, err := st.ChannelSnapshot()
		if err != nil {
			return err
		}
		return gw.Publish(channels)
	}, nil)
	configuration.SetManagedRepository(st.ManagedChannels())
	configuration.SetEligibilityRepository(st.InferenceEligibility())
	engine := server.NewEngine(server.Deps{Generation: gateway.NewHTTPExecutor(gw), TokenAuth: authentication.NewTokenAuthenticator(st), Control: controlplane.New(persistence.Control(st)), Configuration: configuration, ManagedConfiguration: configuration, ManagedEligibility: configuration, Metrics: gw.Metrics(), Responses: runtime.Handler(), AdminAuth: middleware.AdminAuth("synthetic-admin-key"), RateLimiter: limiter, RateGate: access.NewRateGate(limiter)})
	socket := httptest.NewServer(engine)
	defer socket.Close()
	request := func(body, token string) (int, string) {
		t.Helper()
		r, err := http.NewRequest("POST", socket.URL+"/v1/responses", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Content-Type", "application/json")
		response, err := socket.Client().Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		out, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatal(err)
		}
		return response.StatusCode, string(out)
	}
	valid := `{"model":"synthetic-model","input":[{"role":"user","content":"hello"}],"store":false,"stream":true}`
	for range 2 {
		code, body := request(valid, key.Key)
		if code != 200 || !strings.Contains(body, "response.completed") || !strings.Contains(body, "hello") || strings.Contains(body, "synthetic-access-token-private") {
			t.Fatalf("real consumer: %d %s attempts=%d catalogs=%d", code, body, attempts.Load(), catalogs.Load())
		}
	}
	if attempts.Load() != 2 || catalogs.Load() != 1 {
		t.Fatalf("duplicate send or stale cache: attempts%d catalogs%d", attempts.Load(), catalogs.Load())
	}
	for _, body := range []string{strings.Replace(valid, `"stream":true`, `"stream":false`, 1), strings.Replace(valid, `"store":false`, `"store":true`, 1), strings.Replace(valid, `"stream":true`, `"stream":true,"background":false`, 1), strings.Replace(valid, `"stream":true`, `"stream":true,"tools":[]`, 1)} {
		if code, raw := request(body, key.Key); code < 400 {
			t.Fatalf("forbidden input sent: %d %s", code, raw)
		}
	}
	if code, raw := request(valid, other.Key); code < 400 {
		t.Fatalf("wrong key accepted: %d %s", code, raw)
	}
	if attempts.Load() != 2 || catalogs.Load() != 1 {
		t.Fatal("rejected input made upstream request")
	}

	// A real response is observed, persisted, and enforced by the next real
	// request. Administrator resume changes the epoch and invalidates directory
	// cache, while a late failure from before resume becomes stale.
	rejectStatus.Store(429)
	if code, raw := request(valid, key.Key); code < 400 || strings.Contains(raw, "response.completed") {
		t.Fatalf("managed refusal: %d %s", code, raw)
	}
	rejectStatus.Store(0)
	if code, _ := request(valid, key.Key); code < 400 {
		t.Fatal("blocked eligibility sent")
	}
	if attempts.Load() != 3 || catalogs.Load() != 1 {
		t.Fatal("refusal retried or gate not enforced")
	}
	adminRequest := func(method, path, body string) (int, authv3.Eligibility) {
		t.Helper()
		req, e := http.NewRequest(method, socket.URL+path, strings.NewReader(body))
		if e != nil {
			t.Fatal(e)
		}
		req.Header.Set("Authorization", "Bearer "+admin)
		req.Header.Set("Content-Type", "application/json")
		res, e := socket.Client().Do(req)
		if e != nil {
			t.Fatal(e)
		}
		defer res.Body.Close()
		raw, e := io.ReadAll(res.Body)
		if e != nil {
			t.Fatal(e)
		}
		var envelope struct {
			Data authv3.Eligibility `json:"data"`
		}
		if res.StatusCode == 200 {
			if e = json.Unmarshal(raw, &envelope); e != nil {
				t.Fatal(e)
			}
		}
		if strings.Contains(string(raw), "synthetic-secret-body") || strings.Contains(string(raw), "synthetic-access-token-private") {
			t.Fatal("eligibility leaked provider content")
		}
		return res.StatusCode, envelope.Data
	}
	gatePath := fmt.Sprintf("/api/admin/managed-credentials/%d/eligibility", created.Data.Credential.CredentialID)
	code, gate := adminRequest("GET", gatePath, "")
	if code != 200 || !gate.Blocked || gate.Reason != authv3.UsageLimited || gate.ResetKnown || gate.ResetAt != nil {
		t.Fatalf("refusal not queryable: %d %+v", code, gate)
	}
	separate, err := store.Open(db)
	if err != nil {
		t.Fatal(err)
	}
	persisted, err := separate.InferenceEligibility().ReadEligibility(ctx, gate.CredentialID)
	if err != nil || !persisted.Blocked || persisted.GateEpoch != gate.GateEpoch {
		t.Fatal("independent reopen lost refusal", err)
	}
	if err = separate.Close(); err != nil {
		t.Fatal(err)
	}
	resume := fmt.Sprintf(`{"expected_binding_revision":"%d","expected_gate_revision":"%d","expected_gate_epoch":"%d"}`, gate.BindingRevision, gate.Revision, gate.GateEpoch)
	code, resumed := adminRequest("POST", gatePath+"/resume", resume)
	if code != 200 || resumed.Blocked || resumed.GateEpoch <= gate.GateEpoch {
		t.Fatalf("resume failed: %d %+v", code, resumed)
	}
	if code, _ := adminRequest("POST", gatePath+"/resume", resume); code != 409 {
		t.Fatal("stale resume accepted")
	}
	stale, err := st.InferenceEligibility().RecordFailure(ctx, authv3.FailureWrite{Issued: authv2.AccessRequest{CredentialID: gate.CredentialID, ExpectedBindingRevision: gate.BindingRevision, AccountID: account.ID, ExpectedRevision: account.Revision, ExpectedCredentialRevision: account.CredentialRevision, OwnerID: owner.ID, TokenID: key.ID, Model: "synthetic-model"}, GateEpoch: gate.GateEpoch, Signal: authv3.FailureSignal{Reason: authv3.UsageLimited, Scope: authv3.Grant, ProviderCode: "subscription_sharing_usage_limit_exceeded", HTTPStatus: 429, Source: authv3.ResponsesHTTP}})
	if err != nil || stale.Applied {
		t.Fatal("late failure reapplied after resume", err)
	}
	if code, raw := request(valid, key.Key); code != 200 || !strings.Contains(raw, "response.completed") {
		t.Fatalf("resumed consumer: %d %s", code, raw)
	}
	if attempts.Load() != 4 || catalogs.Load() != 2 {
		t.Fatal("resume failed to refresh directory or retried")
	}
	independent, err := store.Open(db)
	if err != nil {
		t.Fatal(err)
	}
	defer independent.Close()
	if _, err := independent.UpstreamAccounts().Disable(ctx, account.ID, account.Revision); err != nil {
		t.Fatal(err)
	}
	if code, raw := request(valid, key.Key); code < 400 || strings.Contains(raw, "response.completed") {
		t.Fatalf("cross-process disable ignored: %d %s", code, raw)
	}
	if attempts.Load() != 4 || catalogs.Load() != 2 {
		t.Fatal("disable retried or fell back")
	}
}
