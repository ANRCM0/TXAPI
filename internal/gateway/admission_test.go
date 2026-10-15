package gateway

import (
	"context"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"minigateway/internal/access"
	dbmodel "minigateway/internal/model"
	"minigateway/internal/protocol/adaptor"
	"minigateway/internal/protocol/adaptor/openai"
	"minigateway/internal/protocol/meta"
	"minigateway/internal/protocol/model"
)

type admissionFunc func(context.Context, AdmissionScope) (func(), error)

func (f admissionFunc) Acquire(ctx context.Context, s AdmissionScope) (func(), error) {
	return f(ctx, s)
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestAdmissionLeaseOrderingAcrossRetry(t *testing.T) {
	var events []string
	hits := 0
	gate := admissionFunc(func(ctx context.Context, s AdmissionScope) (func(), error) {
		if s.UserID != 4 || s.TokenID != 5 {
			t.Fatalf("untrusted scope: %+v", s)
		}
		events = append(events, "acquire "+string(s.Level))
		return func() { events = append(events, "release "+string(s.Level)) }, nil
	})
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		hits++
		code := 200
		body := `{"choices":[]}`
		if hits == 1 {
			code = 503
			body = `{"error":{"message":"retry"}}`
		}
		return &http.Response{StatusCode: code, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})}
	g, err := New(Options{Channels: lifecycleChannels("http://upstream.test"), Client: client, RetryTimes: 1, Admission: gate})
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	ctx := access.WithPrincipal(context.Background(), access.Principal{UserID: 4, TokenID: 5, Policy: access.Policy{Mode: access.All}})
	c, _ := relayContext(ctx)
	g.Relay(c, &model.ModelRequest{Model: "m"}, []byte(`{"model":"m"}`), nil, meta.ModeChatCompletions, time.Second)
	want := []string{"acquire request", "acquire attempt", "release attempt", "acquire attempt", "release attempt", "release request"}
	if !reflect.DeepEqual(events, want) || hits != 2 {
		t.Fatalf("events=%v hits=%d", events, hits)
	}
}
func TestAdmissionRejectsWithoutUpstreamOrHealthPenalty(t *testing.T) {
	for _, level := range []AdmissionLevel{RequestAdmission, AttemptAdmission} {
		t.Run(string(level), func(t *testing.T) {
			released, hits, health := 0, 0, 0
			gate := admissionFunc(func(ctx context.Context, s AdmissionScope) (func(), error) {
				if s.Level == level {
					return nil, &AdmissionError{StatusCode: 429, RetryAfterSeconds: 2, Message: "capacity reached"}
				}
				return func() { released++ }, nil
			})
			client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) { hits++; return nil, errors.New("unexpected upstream") })}
			opts := Options{Channels: lifecycleChannels("http://upstream.test"), Admission: gate, Client: client}
			opts.HealthReporter = func(_ *dbmodel.Credential, _ int, _ string) { health++ }
			g, err := New(opts)
			if err != nil {
				t.Fatal(err)
			}
			defer g.Close()
			c, rec := relayContext(context.Background())
			g.Relay(c, &model.ModelRequest{Model: "m"}, []byte(`{"model":"m"}`), nil, meta.ModeChatCompletions, time.Second)
			wantReleased := 0
			if level == AttemptAdmission {
				wantReleased = 1
			}
			if rec.Code != 429 || rec.Header().Get("Retry-After") != "2" || hits != 0 || health != 0 || released != wantReleased {
				t.Fatalf("rejected admission: status=%d hits=%d health=%d released=%d", rec.Code, hits, health, released)
			}
		})
	}
}
func TestAdmissionReleasesDuringPanic(t *testing.T) {
	var events []string
	gate := admissionFunc(func(_ context.Context, s AdmissionScope) (func(), error) {
		return func() { events = append(events, string(s.Level)) }, nil
	})
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) { panic("upstream extension panic") })}
	g, err := New(Options{Channels: lifecycleChannels("http://upstream.test"), Admission: gate, Client: client})
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	c, _ := relayContext(context.Background())
	defer func() {
		if recover() == nil {
			t.Error("expected panic")
		}
		if !reflect.DeepEqual(events, []string{"attempt", "request"}) {
			t.Errorf("leaked panic leases: %v", events)
		}
	}()
	g.Relay(c, &model.ModelRequest{Model: "m"}, []byte(`{"model":"m"}`), nil, meta.ModeChatCompletions, time.Second)
}
func TestAdmissionCancellationDoesNotLeakLateLease(t *testing.T) {
	released := 0
	gate := admissionFunc(func(ctx context.Context, _ AdmissionScope) (func(), error) {
		<-ctx.Done()
		return func() { released++ }, nil
	})
	g, err := New(Options{Channels: lifecycleChannels("http://upstream.test"), Admission: gate, TotalTimeout: 20 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	c, _ := relayContext(context.Background())
	g.Relay(c, &model.ModelRequest{Model: "m"}, []byte(`{"model":"m"}`), nil, meta.ModeChatCompletions, time.Second)
	if released != 1 || len(state(c).Attempts) != 0 || state(c).StatusCode != 504 {
		t.Fatalf("late lease=%d result=%+v", released, state(c))
	}
}
func TestRegisteredAdaptorIsCopiedAndUsed(t *testing.T) {
	calls := 0
	custom := &customAdaptor{}
	adaptors := map[string]adaptor.Adaptor{"custom": custom}
	channels := lifecycleChannels("http://upstream.test")
	channels[0].Type = "custom"
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"choices":[]}`)), Header: make(http.Header)}, nil
	})}
	g, err := New(Options{Channels: channels, Adaptors: adaptors, Client: client})
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	adaptors["custom"] = nil
	c, _ := relayContext(context.Background())
	g.Relay(c, &model.ModelRequest{Model: "m"}, []byte(`{"model":"m"}`), nil, meta.ModeChatCompletions, time.Second)
	if calls != 1 || state(c).StatusCode != 200 || !custom.used {
		t.Fatalf("custom registration not used: calls=%d result=%+v", calls, state(c))
	}

}

type customAdaptor struct {
	openai.Adaptor
	used bool
}

func (a *customAdaptor) GetRequestURL(m *meta.Meta) (string, error) {
	a.used = true
	return a.Adaptor.GetRequestURL(m)
}

func TestAdmissionIsNotReachedForDeniedModel(t *testing.T) {
	calls := 0
	gate := admissionFunc(func(context.Context, AdmissionScope) (func(), error) { calls++; return func() {}, nil })
	g, err := New(Options{Channels: lifecycleChannels("http://upstream.test"), Admission: gate})
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	ctx := access.WithPrincipal(context.Background(), access.Principal{UserID: 4, TokenID: 5, Policy: access.Policy{Mode: access.Allowlist, Models: []string{}}})
	c, rec := relayContext(ctx)
	g.Relay(c, &model.ModelRequest{Model: "m"}, []byte(`{"model":"m"}`), nil, meta.ModeChatCompletions, time.Second)
	if rec.Code != 403 || calls != 0 || len(state(c).Attempts) != 0 {
		t.Fatalf("denied model entered admission: status=%d calls=%d", rec.Code, calls)
	}
}

func TestAdmissionMisconfigurationFailsClosedWithoutExposingBackendError(t *testing.T) {
	for _, backendErr := range []error{nil, errors.New("redis://private-backend:1234 secret-value")} {
		gate := admissionFunc(func(context.Context, AdmissionScope) (func(), error) { return nil, backendErr })
		g, err := New(Options{Channels: lifecycleChannels("http://upstream.test"), Admission: gate})
		if err != nil {
			t.Fatal(err)
		}
		c, rec := relayContext(context.Background())
		g.Relay(c, &model.ModelRequest{Model: "m"}, []byte(`{"model":"m"}`), nil, meta.ModeChatCompletions, time.Second)
		g.Close()
		if rec.Code != 503 || strings.Contains(rec.Body.String(), "private-backend") || strings.Contains(rec.Body.String(), "secret-value") {
			t.Fatalf("bad admission failure: %d %s", rec.Code, rec.Body.String())
		}
	}
}
