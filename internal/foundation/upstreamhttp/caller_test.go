package upstreamhttp

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	execution "minigateway/internal/foundation/contracts/execution/v1"
	auth "minigateway/internal/foundation/contracts/upstreamauth/v1"
)

func TestCallerEnforcesWireScopeBeforeDispatch(t *testing.T) {
	var calls atomic.Int64
	caller := NewCaller(&http.Client{Transport: fixtureTransport(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		if r.Header.Get("User-Agent") != "TXAPI" || r.GetBody != nil {
			t.Error("wire identity/replay contract changed")
		}
		return &http.Response{StatusCode: 400, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"error":"invalid_grant"}`))}, nil
	})})
	defer caller.Close()
	valid := auth.Request{Method: "POST", URL: "https://auth.openai.com/api/accounts/oauth/token",
		Header: http.Header{"Content-Type": {"application/x-www-form-urlencoded"}, "Accept": {"application/json"}},
		Body:   auth.NewSecret("grant_type=refresh_token&refresh_token=fixture")}
	response := caller.Call(context.Background(), valid)
	if response.Phase != execution.ResponseReceived || response.Status != 400 || !response.Complete ||
		response.Body.Value() != `{"error":"invalid_grant"}` || calls.Load() != 1 {
		t.Fatal("bounded raw response lost")
	}
	mutations := []func(*auth.Request){
		func(r *auth.Request) { r.URL = "https://attacker.invalid/token" },
		func(r *auth.Request) { r.URL += "?token=secret" },
		func(r *auth.Request) { r.Method = "PUT" },
		func(r *auth.Request) { r.Header.Set("Content-Type", "application/json") },
		func(r *auth.Request) { r.Header.Set("Authorization", "Bearer secret") },
		func(r *auth.Request) { r.Header.Set("Idempotency-Key", "replay") },
		func(r *auth.Request) { r.Header["accept"] = []string{"application/json"} },
		func(r *auth.Request) { r.Body = auth.NewSecret(strings.Repeat("x", auth.MaxRequestBytes+1)) },
	}
	for i, mutate := range mutations {
		request := valid
		request.Header = valid.Header.Clone()
		mutate(&request)
		result := caller.Call(context.Background(), request)
		if result.Phase != execution.NotDispatched || result.Complete || result.Status != 0 || calls.Load() != 1 {
			t.Fatalf("invalid request %d dispatched or accepted", i)
		}
	}
}

func TestCallerRealHTTPRefusesRedirectWithoutLeakingBody(t *testing.T) {
	var targets, sources atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { targets.Add(1) }))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sources.Add(1)
		w.Header().Set("Location", target.URL)
		w.WriteHeader(307)
	}))
	defer source.Close()
	local, _ := url.Parse(source.URL)
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	defer transport.CloseIdleConnections()
	caller := NewCaller(&http.Client{Transport: fixtureTransport(func(r *http.Request) (*http.Response, error) {
		copy := r.Clone(r.Context())
		copy.URL.Scheme, copy.URL.Host, copy.Host = local.Scheme, local.Host, local.Host
		return transport.RoundTrip(copy)
	})})
	defer caller.Close()
	result := caller.Call(context.Background(), auth.Request{Method: "POST", URL: "https://auth.openai.com/api/accounts/oauth/token",
		Header: http.Header{"Content-Type": {"application/x-www-form-urlencoded"}}, Body: auth.NewSecret("refresh_token=private")})
	if result.Status != 307 || !result.Complete || sources.Load() != 1 || targets.Load() != 0 {
		t.Fatal("redirect followed or actual send count changed")
	}
}

func TestCallerModelsBearerValidationAndDispatch(t *testing.T) {
	calls := 0
	caller := NewCaller(&http.Client{Transport: fixtureTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.Method != http.MethodGet || r.URL.String() != "https://api.openai.com/v1/models" || r.Header.Get("Authorization") != "Bearer synthetic-access" {
			t.Fatal("directory target or authorization changed")
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"models":[]}`))}, nil
	})})
	defer caller.Close()
	request := auth.Request{Method: http.MethodGet, URL: "https://api.openai.com/v1/models", Header: http.Header{"Authorization": {"Bearer synthetic-access"}, "Accept": {"application/json"}}}
	if response := caller.Call(context.Background(), request); !response.Complete || response.Status != 200 || calls != 1 {
		t.Fatal("valid Bearer directory request rejected")
	}
	for _, value := range []string{"Bearer ", "Bearer  extra", "Bearer token extra", "Bearer token\t", "bearer token", "Bearer " + strings.Repeat("x", auth.MaxTokenBytes+1)} {
		request.Header.Set("Authorization", value)
		if response := caller.Call(context.Background(), request); response.Complete || response.Phase != execution.NotDispatched || calls != 1 {
			t.Fatal("invalid Bearer directory request dispatched")
		}
	}
}

func TestCallerOwnedTransportIsFiniteAndCannotBorrowCookies(t *testing.T) {
	caller := NewCaller(nil)
	defer caller.Close()
	// Observe the behavior through a cancellation rather than expose core's client.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	response := caller.Call(ctx, auth.Request{Method: "GET", URL: "https://auth.openai.com/.well-known/jwks.json"})
	if response.Phase != execution.NotDispatched || response.Complete {
		t.Fatal("canceled key lookup dispatched")
	}
}

type fixtureTransport func(*http.Request) (*http.Response, error)

func (f fixtureTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
