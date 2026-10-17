package gateway

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"minigateway/internal/access"
	"minigateway/internal/billing"
	"minigateway/internal/core"
	dbmodel "minigateway/internal/model"
	"minigateway/internal/protocol"
)

func TestTypedCoordinatorRequiresIdentityAndIsolatesExecutions(t *testing.T) {
	var hits atomic.Int64
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		hits.Add(1)
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"choices":[],"usage":{"prompt_tokens":2,"completion_tokens":1,"total_tokens":3}}`))}, nil
	})}
	g, err := New(Options{Channels: lifecycleChannels("http://upstream.test"), Client: client})
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	in := Execution{Operation: protocol.Chat, Body: []byte(`{"model":"m"}`)}
	denied := g.Execute(context.Background(), httptest.NewRecorder(), in)
	if denied.StatusCode != 401 || hits.Load() != 0 || denied.Phase != core.NotDispatched {
		t.Fatalf("identity bypass: %+v", denied)
	}
	ctx := access.WithPrincipal(context.Background(), access.Principal{UserID: 1, TokenID: 1, Policy: access.Policy{Mode: access.All}})
	ctx = access.WithRequestID(ctx, "same-client-id")
	var workers sync.WaitGroup
	for range 24 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			result := g.Execute(ctx, httptest.NewRecorder(), in)
			if result.StatusCode != 200 || len(result.Attempts) != 1 || !result.ProtocolComplete || result.Phase != core.ResponseReceived {
				t.Errorf("shared execution state: %+v", result)
			}
		}()
	}
	workers.Wait()
	if hits.Load() != 24 || ResultFromContext(ctx) != nil {
		t.Fatalf("leaked mutable result: hits=%d", hits.Load())
	}
}

type generationLimiter struct {
	rpm, tpm    atomic.Int64
	mu          sync.Mutex
	adjustments map[string]int
}

func (l *generationLimiter) AllowRequest(context.Context, string, int, time.Time) (bool, int) {
	l.rpm.Add(1)
	return true, 0
}
func (l *generationLimiter) AllowTokens(context.Context, string, int, int, time.Time) (bool, int) {
	l.tpm.Add(1)
	return true, 0
}
func (l *generationLimiter) AddTokens(_ context.Context, key string, n int, _ time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.adjustments[key] += n
}

func TestTypedCoordinatorEachGenerationOwnsRateReservation(t *testing.T) {
	limiter := &generationLimiter{adjustments: map[string]int{}}
	g, err := New(Options{Channels: lifecycleChannels("http://upstream.test"), Limiter: limiter,
		Client: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"choices":[],"usage":{"prompt_tokens":2,"completion_tokens":1,"total_tokens":3}}`))}, nil
		})}})
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	ctx := access.WithPrincipal(context.Background(), access.Principal{UserID: 1, TokenID: 1, RPMLimit: 10, TPMLimit: 100, Policy: access.Policy{Mode: access.All}})
	ctx = access.WithTokenReservation(ctx, 77, 100)
	ctx = access.WithRateAdmission(ctx, 1)
	for range 2 {
		result := g.Execute(ctx, httptest.NewRecorder(), Execution{Operation: protocol.Chat, Body: []byte(`{"model":"m","messages":[{"role":"user","content":"hello"}]}`)})
		if result.StatusCode != 200 {
			t.Fatal(result)
		}
	}
	if limiter.rpm.Load() != 2 || limiter.tpm.Load() != 2 {
		t.Fatalf("generation reused admission: rpm=%d tpm=%d", limiter.rpm.Load(), limiter.tpm.Load())
	}
	access.FinishTokens(ctx, limiter, 20)
	if limiter.adjustments[access.TokenUsageBucketKey(77)] != -80 {
		t.Fatal("generation consumed parent's TPM reservation")
	}
}

func TestTypedCoordinatorPanicReleasesAdmission(t *testing.T) {
	var active atomic.Int64
	g, err := New(Options{Channels: lifecycleChannels("http://upstream.test"), Admission: admissionFunc(func(context.Context, AdmissionScope) (func(), error) {
		active.Add(1)
		return func() { active.Add(-1) }, nil
	}), Client: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) { panic("injected transport panic") })}})
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	func() {
		defer func() {
			if recover() == nil {
				t.Error("panic was swallowed before the transport boundary")
			}
		}()
		ctx := access.WithPrincipal(context.Background(), access.Principal{UserID: 1, TokenID: 1, Policy: access.Policy{Mode: access.All}})
		g.Execute(ctx, httptest.NewRecorder(), Execution{Operation: protocol.Chat, Body: []byte(`{"model":"m"}`)})
	}()
	if active.Load() != 0 {
		t.Fatal("panic leaked request or attempt slots")
	}
}

func TestTypedCoordinatorRefundsTPMOnConversionRejection(t *testing.T) {
	limiter := &generationLimiter{adjustments: map[string]int{}}
	channels := lifecycleChannels("http://upstream.test")
	channels[0].Type = "anthropic"
	var hits atomic.Int64
	g, err := New(Options{Channels: channels, Limiter: limiter, Client: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) { hits.Add(1); panic("rejection reached upstream") })}})
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	ctx := access.WithPrincipal(context.Background(), access.Principal{UserID: 1, TokenID: 1, TPMLimit: 1000, Policy: access.Policy{Mode: access.All}})
	body := []byte(`{"model":"m","messages":[{"role":"user","content":"hello world"}],"seed":1}`)
	result := g.Execute(ctx, httptest.NewRecorder(), Execution{Operation: protocol.Chat, Body: body})
	if result.StatusCode != 400 || result.Phase != core.NotDispatched || hits.Load() != 0 {
		t.Fatalf("unsafe conversion result: %+v", result)
	}
	want := -billing.EstimateUsage(billing.PromptTextFromBody(body), "").PromptTokens
	if want == 0 || limiter.adjustments[access.TokenUsageBucketKey(1)] != want {
		t.Fatalf("TPM leaked on unsent request: %+v want=%d", limiter.adjustments, want)
	}
}

func TestTypedCoordinatorRoutesByCapabilityBeforePriority(t *testing.T) {
	channels := lifecycleChannels("http://gemini.test")
	channels[0].Type = "gemini"
	channels[0].Priority = 100
	other := channels[0]
	other.ID = 2
	other.Name = "tools"
	other.Type = "anthropic"
	other.BaseURL = "http://anthropic.test"
	other.Priority = 0
	other.Credentials = []dbmodel.Credential{{ID: 10, APIKey: "isolated-fixture", Weight: 1, Status: dbmodel.CredentialActive}}
	channels = append(channels, other)
	var hits atomic.Int64
	g, err := New(Options{Channels: channels, Client: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host != "anthropic.test" {
			t.Errorf("unsupported target selected: %s", r.URL.Host)
		}
		hits.Add(1)
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"id":"msg","model":"m","content":[{"type":"tool_use","id":"call-one","name":"lookup","input":{"city":"one"}}],"stop_reason":"tool_use","usage":{"input_tokens":2,"output_tokens":1}}`))}, nil
	})}})
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	ctx := access.WithPrincipal(context.Background(), access.Principal{UserID: 1, TokenID: 1, Policy: access.Policy{Mode: access.All}})
	rec := httptest.NewRecorder()
	result := g.Execute(ctx, rec, Execution{Operation: protocol.Chat, Body: []byte(`{"model":"m","tools":[{"type":"function","function":{"name":"lookup","parameters":{"type":"object"}}}],"messages":[{"role":"user","content":"one"}]}`)})
	if result.StatusCode != 200 || result.ChannelID != 2 || hits.Load() != 1 || !strings.Contains(rec.Body.String(), `"tool_calls"`) || !strings.Contains(rec.Body.String(), `"call-one"`) {
		t.Fatalf("capability route failed: %+v %s", result, rec.Body)
	}
}

func TestTypedCoordinatorRetainsCapacityThroughFinalization(t *testing.T) {
	var active atomic.Int64
	gate := admissionFunc(func(_ context.Context, scope AdmissionScope) (func(), error) {
		if scope.Level == RequestAdmission {
			active.Add(1)
			return func() { active.Add(-1) }, nil
		}
		return func() {}, nil
	})
	g, err := New(Options{Channels: lifecycleChannels("http://upstream.test"), Admission: gate,
		Client: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"choices":[]}`))}, nil
		})}, LogRecorder: func(*dbmodel.RequestLog) {
			if active.Load() != 1 {
				t.Error("request slot released before finalization")
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	ctx := access.WithPrincipal(context.Background(), access.Principal{UserID: 1, TokenID: 1, Policy: access.Policy{Mode: access.All}})
	g.Execute(ctx, httptest.NewRecorder(), Execution{Operation: protocol.Chat, Body: []byte(`{"model":"m"}`)})
	if active.Load() != 0 {
		t.Fatal("request slot leaked")
	}
}
