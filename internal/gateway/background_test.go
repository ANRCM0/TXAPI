package gateway

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"minigateway/internal/access"
	"minigateway/internal/billing"
	bg "minigateway/internal/foundation/contracts/background/v1"
	execution "minigateway/internal/foundation/contracts/execution/v1"
	bs "minigateway/internal/generation/backgroundstate"
	"minigateway/internal/model"
	native "minigateway/internal/protocol/responses"
)

type backgroundProgressFixture struct {
	dispatched atomic.Bool
	observed   atomic.Int64
	fail       bool
}

func (p *backgroundProgressFixture) Validate(context.Context, bs.RemoteCall) error { return nil }
func (p *backgroundProgressFixture) Dispatch(context.Context, execution.TargetConstraint, bs.RateReservation) error {
	p.dispatched.Store(true)
	return nil
}
func (p *backgroundProgressFixture) Observe(context.Context, bg.Observation) error {
	p.observed.Add(1)
	if p.fail {
		return errors.New("fixture disk failure")
	}
	return nil
}

type alteredBackgroundCodec struct {
	base   bg.Codec
	change func(*execution.Input)
}

func (c alteredBackgroundCodec) Prepare(command bg.RemoteCommand, target execution.Target) (bg.Prepared, *execution.Error) {
	prepared, e := c.base.Prepare(command, target)
	if e == nil {
		c.change(&prepared.Input)
	}
	return prepared, e
}

func TestBackgroundRegistrationsFailClosedAndAreCopiedBeforeExecution(t *testing.T) {
	var hits atomic.Int64
	progress := &backgroundProgressFixture{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if !progress.dispatched.Load() {
			t.Error("network preceded durable dispatch")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"resp_provider","object":"response","status":"queued","output":[]}`)
	}))
	defer upstream.Close()
	g, e := newRegisteredGateway(Options{Channels: lifecycleChannels(upstream.URL), BillingMode: billing.ModeDisabled})
	if e != nil {
		t.Fatal(e)
	}
	defer g.Close()
	registration := native.BackgroundRegistration()
	var nilCodec *native.BackgroundCodec
	var nilParser *native.BackgroundParser
	for _, change := range []func(*bg.Registration){
		func(r *bg.Registration) { r.Version = "2" },
		func(r *bg.Registration) { r.ID = "" },
		func(r *bg.Registration) { r.Operation = "" },
		func(r *bg.Registration) { r.Codec = nilCodec },
		func(r *bg.Registration) { r.Parse = nilParser },
	} {
		r := registration
		change(&r)
		if _, e = NewBackgroundRemote(g, BackgroundRemoteOptions{Registrations: []bg.Registration{r}, Channels: map[uint]string{1: r.ID}}); e == nil {
			t.Fatal("incomplete background registration accepted")
		}
	}
	if _, e = NewBackgroundRemote(g, BackgroundRemoteOptions{Registrations: []bg.Registration{registration, registration}, Channels: map[uint]string{1: registration.ID}}); e == nil {
		t.Fatal("duplicate background registration accepted")
	}
	if _, e = NewBackgroundRemote(g, BackgroundRemoteOptions{Registrations: []bg.Registration{registration}, Channels: map[uint]string{99: registration.ID}}); e == nil {
		t.Fatal("nonexistent opted-in channel accepted")
	}
	opts := BackgroundRemoteOptions{Registrations: []bg.Registration{registration}, Channels: map[uint]string{1: registration.ID}}
	remote, e := NewBackgroundRemote(g, opts)
	if e != nil {
		t.Fatal(e)
	}
	opts.Registrations[0].Codec = nil
	opts.Channels[1] = "unregistered"
	ctx := access.WithPrincipal(context.Background(), access.Principal{UserID: 1, TokenID: 1, Policy: access.Policy{Mode: access.All}})
	call := bs.RemoteCall{Action: bg.RemoteSubmit, Operation: execution.Responses, Model: "m", PublicID: "resp_local", Body: []byte(`{"model":"m","background":true,"input":"x"}`)}
	if e = remote.Execute(ctx, call, progress); e != nil || hits.Load() != 1 || progress.observed.Load() != 1 {
		t.Fatalf("external registration mutation changed execution: hits=%d observe=%d error=%v", hits.Load(), progress.observed.Load(), e)
	}
}

func TestBackgroundPreparedWireCannotEscapeBoundOriginOrCredential(t *testing.T) {
	var hits atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1); w.WriteHeader(500) }))
	defer upstream.Close()
	g, e := newRegisteredGateway(Options{Channels: lifecycleChannels(upstream.URL), BillingMode: billing.ModeDisabled})
	if e != nil {
		t.Fatal(e)
	}
	defer g.Close()
	ctx := access.WithPrincipal(context.Background(), access.Principal{UserID: 1, TokenID: 1, Policy: access.Policy{Mode: access.All}})
	for _, change := range []func(*execution.Input){
		func(in *execution.Input) { in.URL = "https://elsewhere.invalid/v1/responses" },
		func(in *execution.Input) { in.URL = upstream.URL + "/v1/responses?api_key=wrong" },
		func(in *execution.Input) { in.Header.Set("Authorization", "Bearer other-account") },
		func(in *execution.Input) { in.Header.Set("Cookie", "unbound-account") },
		func(in *execution.Input) { in.Replayable = true },
	} {
		registration := native.BackgroundRegistration()
		registration.Codec = alteredBackgroundCodec{registration.Codec, change}
		remote, e := NewBackgroundRemote(g, BackgroundRemoteOptions{Registrations: []bg.Registration{registration}, Channels: map[uint]string{1: registration.ID}})
		if e != nil {
			t.Fatal(e)
		}
		progress := &backgroundProgressFixture{}
		e = remote.Execute(ctx, bs.RemoteCall{Action: bg.RemoteSubmit, Operation: execution.Responses, Model: "m", PublicID: "resp_local", Body: []byte(`{"model":"m","background":true,"input":"x"}`)}, progress)
		if !errors.Is(e, bg.ErrUnsupportedCapability) || progress.dispatched.Load() || hits.Load() != 0 {
			t.Fatal("unbound prepared request reached dispatch or network", e, hits.Load())
		}
	}
}

func TestBackgroundPersistenceFailureDoesNotPenalizeProviderHealth(t *testing.T) {
	var hits, health atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"resp_provider","object":"response","status":"queued","output":[]}`)
	}))
	defer upstream.Close()
	g, e := newRegisteredGateway(Options{Channels: lifecycleChannels(upstream.URL), BillingMode: billing.ModeDisabled, HealthReporter: func(*model.Credential, int, string) { health.Add(1) }})
	if e != nil {
		t.Fatal(e)
	}
	defer g.Close()
	registration := native.BackgroundRegistration()
	remote, e := NewBackgroundRemote(g, BackgroundRemoteOptions{Registrations: []bg.Registration{registration}, Channels: map[uint]string{1: registration.ID}})
	if e != nil {
		t.Fatal(e)
	}
	ctx := access.WithPrincipal(context.Background(), access.Principal{UserID: 1, TokenID: 1, Policy: access.Policy{Mode: access.All}})
	progress := &backgroundProgressFixture{fail: true}
	e = remote.Execute(ctx, bs.RemoteCall{Action: bg.RemoteSubmit, Operation: execution.Responses, Model: "m", PublicID: "resp_local", Body: []byte(`{"model":"m","background":true,"input":"x"}`)}, progress)
	if !errors.Is(e, bg.ErrUnavailable) || hits.Load() != 1 || health.Load() != 0 || progress.observed.Load() != 1 {
		t.Fatalf("storage failure was retried or blamed on provider: %v hits=%d health=%d", e, hits.Load(), health.Load())
	}
}
