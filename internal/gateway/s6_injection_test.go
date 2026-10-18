package gateway

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"minigateway/internal/access"
	"minigateway/internal/core"
	dbmodel "minigateway/internal/model"
	"minigateway/internal/protocol"
	"minigateway/internal/protocol/adaptor"
	"minigateway/internal/protocol/meta"
)

type nilRegisteredAdaptor struct{ adaptor.Adaptor }

type nilRegisteredFunction func()

func (nilRegisteredFunction) GetRequestURL(*meta.Meta) (string, error) {
	panic("nil function adaptor accepted")
}
func (nilRegisteredFunction) SetupRequestHeader(*http.Request, *meta.Meta) error {
	panic("nil function adaptor accepted")
}
func (nilRegisteredFunction) ConvertRequest([]byte, *meta.Meta) ([]byte, error) {
	panic("nil function adaptor accepted")
}
func (nilRegisteredFunction) GetChannelName() string { panic("nil function adaptor accepted") }

func TestS6ConstructionRejectsTypedNilDependencies(t *testing.T) {
	var admission admissionFunc
	var limiter *generationLimiter
	var registered *nilRegisteredAdaptor
	var function nilRegisteredFunction
	for _, tc := range []struct {
		name string
		opts Options
		want string
	}{
		{"admission", Options{Admission: admission}, "admission"},
		{"limiter", Options{Limiter: limiter}, "limiter"},
		{"adaptor", Options{Protocols: protocol.Protocols(map[string]adaptor.Adaptor{"custom": registered})}, "adaptor"},
		{"nil_adaptor", Options{Protocols: protocol.Protocols(map[string]adaptor.Adaptor{"custom": nil})}, "adaptor"},
		{"nil_function_adaptor", Options{Protocols: protocol.Protocols(map[string]adaptor.Adaptor{"custom": function})}, "adaptor"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g, err := newRegisteredGateway(tc.opts)
			if g != nil {
				g.Close()
				t.Fatal("invalid dependency produced a gateway")
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected %s configuration error, got %v", tc.want, err)
			}
		})
	}
}

func TestS6AbsentOptionalDependenciesKeepDefaults(t *testing.T) {
	g, err := newRegisteredGateway(Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	if g.admission != nil || g.limiter != nil || g.billing != nil || g.strictBilling != nil {
		t.Fatal("absent optional dependencies changed semantics")
	}
}

func TestS6TypedNilOutputRejectedBeforeAdmissionOrDispatch(t *testing.T) {
	var acquisitions, dispatches int
	g, err := newRegisteredGateway(Options{
		Admission: admissionFunc(func(context.Context, AdmissionScope) (func(), error) {
			acquisitions++
			return func() {}, nil
		}),
		Client: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			dispatches++
			panic("missing output reached upstream")
		})},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	var output *observerOutput
	in := Execution{Operation: protocol.Chat, Body: []byte(`{"model":"m"}`)}
	for _, claimed := range []bool{false, true} {
		var result RequestResult
		if claimed {
			result = g.ExecuteClaimedTo(outputIdentity(), output, in, dbmodel.GenerationClaim{})
		} else {
			result = g.ExecuteTo(outputIdentity(), output, in)
		}
		if result.StatusCode != 500 || result.Phase != core.NotDispatched || result.Committed || result.ErrorMessage != "missing execution output" || result.Error == nil {
			t.Fatalf("claimed=%v: invalid missing-output result: %+v", claimed, result)
		}
	}
	if acquisitions != 0 || dispatches != 0 {
		t.Fatalf("missing output consumed resources: admissions=%d dispatches=%d", acquisitions, dispatches)
	}
}

func TestS6HTTPHandlerPreservesExistingRateAdmission(t *testing.T) {
	limiter := &generationLimiter{}
	g, err := newRegisteredGateway(Options{Channels: lifecycleChannels("http://upstream.test"), Limiter: limiter,
		Client: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"choices":[],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))}, nil
		})}})
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	ctx := access.WithPrincipal(context.Background(), access.Principal{UserID: 1, TokenID: 1, RPMLimit: 10, Policy: access.Policy{Mode: access.All}})
	// Model the successful HTTP rate middleware handoff to the real handler.
	if allowed, _ := limiter.AllowRequest(ctx, access.TokenBucketKey(1), 10, time.Now()); !allowed {
		t.Fatal("HTTP rate admission failed")
	}
	ctx = access.WithRateAdmission(ctx, 1)
	c, rec := relayContext(ctx)
	c.Request.Body = io.NopCloser(strings.NewReader(`{"model":"m","messages":[{"role":"user","content":"hello"}]}`))
	g.HandleChatCompletions(c)
	if rec.Code != 200 || limiter.rpm.Load() != 1 {
		t.Fatalf("HTTP admission counted twice: status=%d rpm=%d", rec.Code, limiter.rpm.Load())
	}
}
