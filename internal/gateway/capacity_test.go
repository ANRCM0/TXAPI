package gateway

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"minigateway/internal/access"
	dbmodel "minigateway/internal/model"
	"minigateway/internal/protocol/meta"
	"minigateway/internal/protocol/model"
)

func newTestCapacity(t *testing.T, channel int) *access.Capacity {
	t.Helper()
	capacity, err := access.NewCapacity(access.CapacityConfig{Instance: 8, User: 4, Key: 2, Channel: channel})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(capacity.Close)
	return capacity
}
func capacityContext() context.Context {
	return access.WithPrincipal(context.Background(), access.Principal{UserID: 4, TokenID: 5, UserRPMLimit: 1, Policy: access.Policy{Mode: access.All}})
}
func checkReleased(t *testing.T, capacity *access.Capacity) {
	t.Helper()
	s := capacity.Snapshot()
	if s.Requests != 0 || s.Attempts != 0 || s.Users != 0 || s.Keys != 0 || s.Channels != 0 {
		t.Fatalf("leaked leases: %+v", s)
	}
}

func TestCapacityAdmissionUsesVerifiedIdentity(t *testing.T) {
	gate := CapacityAdmission{Capacity: newTestCapacity(t, 1)}
	for _, ctx := range []context.Context{context.Background(), capacityContext()} {
		if _, err := gate.Acquire(ctx, AdmissionScope{Level: RequestAdmission, UserID: 999, TokenID: 5}); err == nil {
			t.Fatal("unverified scope admitted")
		}
	}
	checkReleased(t, gate.Capacity)
}

func TestCapacityRetryRetainsRequestAndReleasesEachAttempt(t *testing.T) {
	capacity := newTestCapacity(t, 1)
	hits := 0
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		hits++
		if s := capacity.Snapshot(); s.Requests != 1 || s.Attempts != 1 || s.RateEvents != 1 {
			t.Fatalf("retry reservations: %+v", s)
		}
		code, body := 200, `{"choices":[]}`
		if hits == 1 {
			code, body = 503, `{"error":{"message":"retry"}}`
		}
		return &http.Response{StatusCode: code, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})}
	g, err := New(Options{Channels: lifecycleChannels("http://upstream.test"), Client: client, RetryTimes: 1, Admission: CapacityAdmission{Capacity: capacity}})
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	c, rec := relayContext(capacityContext())
	g.Relay(c, &model.ModelRequest{Model: "m"}, []byte(`{"model":"m"}`), nil, meta.ModeChatCompletions, time.Second)
	if rec.Code != 200 || hits != 2 {
		t.Fatalf("retry failed: %d hits=%d body=%s", rec.Code, hits, rec.Body.String())
	}
	checkReleased(t, capacity)
}

func TestCapacityRealLeaseCleanupOnCancelDeadlineAndPanic(t *testing.T) {
	for _, scenario := range []string{"cancel", "deadline", "panic"} {
		t.Run(scenario, func(t *testing.T) {
			capacity := newTestCapacity(t, 1)
			ctx, cancel := context.WithCancel(capacityContext())
			defer cancel()
			health := 0
			client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				if s := capacity.Snapshot(); s.Requests != 1 || s.Attempts != 1 {
					t.Fatalf("missing reservations: %+v", s)
				}
				if scenario == "panic" {
					panic("extension panic")
				}
				if scenario == "cancel" {
					cancel()
				}
				<-r.Context().Done()
				return nil, r.Context().Err()
			})}
			g, err := New(Options{Channels: lifecycleChannels("http://upstream.test"), Client: client, TotalTimeout: 30 * time.Millisecond, Admission: CapacityAdmission{Capacity: capacity}, HealthReporter: func(*dbmodel.Credential, int, string) { health++ }})
			if err != nil {
				t.Fatal(err)
			}
			defer g.Close()
			c, _ := relayContext(ctx)
			panicked := false
			func() {
				defer func() { panicked = recover() != nil }()
				g.Relay(c, &model.ModelRequest{Model: "m"}, []byte(`{"model":"m"}`), nil, meta.ModeChatCompletions, time.Second)
			}()
			if panicked != (scenario == "panic") {
				t.Fatalf("panic=%v", panicked)
			}
			if scenario == "cancel" && ResultFromContext(c.Request.Context()).StatusCode != 499 {
				t.Fatal("cancel was not retained")
			}
			if scenario == "deadline" && ResultFromContext(c.Request.Context()).StatusCode != 504 {
				t.Fatal("deadline was not retained")
			}
			if health != 0 {
				t.Fatalf("request cancellation/total deadline penalized credential: %d", health)
			}
			checkReleased(t, capacity)
		})
	}
}

func TestCapacityChannelRejectionDoesNotExecuteOrPenalize(t *testing.T) {
	capacity := newTestCapacity(t, 1)
	occupied, err := capacity.AcquireAttempt(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	defer occupied()
	var hits, health atomic.Int64
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) { hits.Add(1); panic("unexpected upstream") })}
	g, err := New(Options{Channels: lifecycleChannels("http://upstream.test"), Client: client, Admission: CapacityAdmission{Capacity: capacity}, HealthReporter: func(*dbmodel.Credential, int, string) { health.Add(1) }})
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	c, rec := relayContext(capacityContext())
	g.Relay(c, &model.ModelRequest{Model: "m"}, []byte(`{"model":"m"}`), nil, meta.ModeChatCompletions, time.Second)
	if rec.Code != 429 || rec.Header().Get("Retry-After") != "1" || hits.Load() != 0 || health.Load() != 0 {
		t.Fatalf("incorrect rejection: %d %s", rec.Code, rec.Body.String())
	}
	occupied()
	checkReleased(t, capacity)
}
