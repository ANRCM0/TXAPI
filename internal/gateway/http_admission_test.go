package gateway

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"minigateway/internal/access"
	"minigateway/internal/compat/httpoutput"
	execution "minigateway/internal/foundation/contracts/execution/v1"
)

func TestHTTPExecutorConsumesMiddlewareAdmissionOnceAcrossGenerations(t *testing.T) {
	limiter := &generationLimiter{}
	g, err := newRegisteredGateway(Options{Channels: lifecycleChannels("http://upstream.test"), Limiter: limiter,
		Client: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"choices":[],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))}, nil
		})}})
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	ctx := access.WithPrincipal(context.Background(), access.Principal{UserID: 1, TokenID: 1, RPMLimit: 100, Policy: access.Policy{Mode: access.All}})
	_, _ = limiter.AllowRequest(ctx, access.TokenBucketKey(1), 100, time.Now())
	ctx = access.WithRateAdmission(ctx, 1)
	in := execution.Invocation{Operation: execution.Chat, Body: []byte(`{"model":"m"}`)}
	for i := range 4 {
		out := NewHTTPExecutor(g).Execute(ctx, httpoutput.New(httptest.NewRecorder()), in)
		if out.StatusCode != 200 || limiter.rpm.Load() != int64(i+1) {
			t.Fatalf("generation=%d rpm=%d result=%+v", i, limiter.rpm.Load(), out)
		}
	}
	out := NewPublicExecutor(g).Execute(ctx, httpoutput.New(httptest.NewRecorder()), in)
	if out.StatusCode != 200 || limiter.rpm.Load() != 5 {
		t.Fatalf("regular executor inherited HTTP admission: rpm=%d result=%+v", limiter.rpm.Load(), out)
	}
}
