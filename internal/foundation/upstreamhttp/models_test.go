package upstreamhttp

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	execution "minigateway/internal/foundation/contracts/execution/v1"
	auth "minigateway/internal/foundation/contracts/upstreamauth/v1"
)

func TestSIWCModelsGETIsNarrowAndBounded(t *testing.T) {
	hits := 0
	c := NewCaller(&http.Client{Transport: fixtureTransport(func(r *http.Request) (*http.Response, error) {
		hits++
		if r.Method != "GET" || r.URL.String() != "https://api.openai.com/v1/models" || r.GetBody != nil || r.Header.Get("Authorization") != "Bearer "+strings.Repeat("x", 500) {
			t.Fatal("unexpected managed directory wire request")
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"models":[]}`)), Header: http.Header{}}, nil
	})})
	defer c.Close()
	valid := auth.Request{Method: "GET", URL: "https://api.openai.com/v1/models", Header: http.Header{"Accept": {"application/json"}, "Authorization": {"Bearer " + strings.Repeat("x", 500)}}}
	if response := c.Call(context.Background(), valid); !response.Complete || response.Status != 200 || hits != 1 {
		t.Fatal("bounded catalog request rejected")
	}
	for _, mutate := range []func(*auth.Request){
		func(r *auth.Request) { r.URL += "?key=secret" },
		func(r *auth.Request) { r.URL = "https://api.openai.com/v1/responses" },
		func(r *auth.Request) { r.URL = "https://auth.openai.com/.well-known/jwks.json" },
		func(r *auth.Request) { r.URL = "http://api.openai.com/v1/models" },
		func(r *auth.Request) { r.URL = "https://api.openai.com:443/v1/models" },
		func(r *auth.Request) { r.Method = "POST" },
		func(r *auth.Request) { r.Body = auth.NewSecret("body") },
		func(r *auth.Request) { r.Header.Del("Authorization") },
		func(r *auth.Request) { r.Header.Set("Authorization", "Bearer ") },
		func(r *auth.Request) {
			r.Header.Set("Authorization", "Bearer "+strings.Repeat("x", auth.MaxTokenBytes+1))
		},
		func(r *auth.Request) { r.Header.Set("Authorization", "Bearer secret\r\ninjected") },
		func(r *auth.Request) { r.Header.Set("X-Custom", "unsupported") },
		func(r *auth.Request) {
			r.Header["authorization"] = r.Header["Authorization"]
			delete(r.Header, "Authorization")
		},
	} {
		r := valid
		r.Header = valid.Header.Clone()
		mutate(&r)
		if response := c.Call(context.Background(), r); response.Complete || response.Phase != execution.NotDispatched || hits != 1 {
			t.Fatal("directory whitelist bypassed")
		}
	}
}
