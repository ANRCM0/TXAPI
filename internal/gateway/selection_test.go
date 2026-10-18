package gateway

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"minigateway/internal/access"
	"minigateway/internal/billing"
	execution "minigateway/internal/foundation/contracts/execution/v1"
	"minigateway/internal/model"
)

func TestRouteDecisionExplainsPriorityFallback(t *testing.T) {
	g := testCoordinator(t)
	now := time.Now()
	primary := &channel{id: 1, priority: 9, credentials: []model.Credential{
		{ID: 1, APIKey: "tried", Status: model.CredentialActive},
		{ID: 2, APIKey: "disabled", Status: model.CredentialDisabled},
		{ID: 3, APIKey: "", Status: model.CredentialActive},
	}}
	backup := &channel{id: 2, priority: -2, credentials: []model.Credential{{ID: 4, APIKey: "backup", Status: model.CredentialActive}}}
	ch, credential, d := g.chooseTarget([]*channel{primary, backup}, map[uint]bool{1: true}, "", now)
	if ch != backup || credential == nil || credential.ID != 4 || !d.priorityFallback {
		t.Fatalf("expected usable backup after primary exhaustion: channel=%v credential=%v decision=%+v", ch, credential, d)
	}
	if d.candidateChannels != 2 || d.eligibleChannels != 1 || d.selectionPoolChannels != 1 ||
		d.totalCredentials != 4 || d.triedCredentials != 1 || d.unavailableCredentials != 2 || d.eligibleCredentials != 1 {
		t.Fatalf("decision does not explain current credential eligibility: %+v", d)
	}
	if d.outcome != "selected" || d.picker != "weighted" || d.selectedChannel != 2 || d.selectedCredential != 4 {
		t.Fatalf("decision does not describe the actual choice: %+v", d)
	}
}

func TestRouteDecisionRecordsActualPickerAndKeepsOwnedCredential(t *testing.T) {
	for _, accepted := range []bool{false, true} {
		t.Run(map[bool]string{false: "declined", true: "accepted"}[accepted], func(t *testing.T) {
			g := testCoordinator(t)
			ch := &channel{id: 3, priority: 1, credentials: []model.Credential{{ID: 7, APIKey: "owned-key", Status: model.CredentialActive}}}
			tried := map[uint]bool{8: true}
			calls := 0
			g.picker = func(credentials []model.Credential, excluded map[uint]bool, _ string, _ time.Time) (*model.Credential, bool) {
				calls++
				credentials[0].APIKey = "substituted-key"
				excluded[7] = true
				return &credentials[0], accepted
			}
			_, credential, d := g.chooseTarget([]*channel{ch}, tried, "session", time.Now())
			want := "weighted_fallback"
			if accepted {
				want = "extension"
			}
			if calls != 1 || credential == nil || credential.APIKey != "owned-key" || d.picker != want || d.outcome != "selected" {
				t.Fatalf("extension changed selection authority or was called twice: calls=%d credential=%v decision=%+v", calls, credential, d)
			}
			if tried[7] || ch.credentials[0].APIKey != "owned-key" {
				t.Fatal("extension mutated the retry exclusions or credential snapshot")
			}
		})
	}
}

func TestRouteSelectionObservesEmptyAndExhaustedPools(t *testing.T) {
	for _, empty := range []bool{true, false} {
		t.Run(map[bool]string{true: "empty", false: "exhausted"}[empty], func(t *testing.T) {
			g := testCoordinator(t)
			var candidates []*channel
			want := "no_candidates"
			if !empty {
				candidates = []*channel{{id: 1, credentials: []model.Credential{{ID: 1, APIKey: "used", Status: model.CredentialActive}}}}
				want = "no_usable_credentials"
			}
			ch, credential := g.selectTarget(candidates, map[uint]bool{1: true}, "", time.Now())
			var out strings.Builder
			if err := g.Metrics().Render(&out); err != nil {
				t.Fatal(err)
			}
			if ch != nil || credential != nil || !strings.Contains(out.String(), `gateway_route_selections_total{outcome="`+want+`",picker="none"} 1`) {
				t.Fatalf("selection rejection was not observed: %s", out.String())
			}
		})
	}
}

func TestRouteSelectionDoesNotCountAdmissionRejectionAsAttempt(t *testing.T) {
	hits := 0
	g, err := newRegisteredGateway(Options{Channels: lifecycleChannels("http://upstream.test"), TotalTimeout: time.Second,
		Admission: admissionFunc(func(_ context.Context, scope AdmissionScope) (func(), error) {
			if scope.Level == AttemptAdmission {
				return nil, &AdmissionError{StatusCode: 429, Message: "attempt capacity"}
			}
			return func() {}, nil
		}), Client: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			hits++
			return nil, context.Canceled
		})}})
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	result := g.ExecuteTo(outputIdentity(), &observerOutput{}, Execution{Operation: execution.Chat, Body: []byte(`{"model":"m"}`)})
	var out strings.Builder
	if err := g.Metrics().Render(&out); err != nil {
		t.Fatal(err)
	}
	if hits != 0 || result.StatusCode != 429 || len(result.Attempts) != 0 {
		t.Fatalf("selection caused execution despite admission rejection: hits=%d result=%+v", hits, result)
	}
	if !strings.Contains(out.String(), `gateway_route_selections_total{outcome="selected",picker="weighted"} 1`) ||
		strings.Contains(out.String(), "gateway_attempts_total{") {
		t.Fatalf("selection and executed-attempt counters were conflated: %s", out.String())
	}
}

func TestRouteSelectionReportsPreflightRejectionsWithoutDispatch(t *testing.T) {
	for _, tc := range []struct {
		name, channelType, body, outcome string
		operation                        execution.Operation
		status                           int
	}{
		{"model", "openai", `{"model":"missing"}`, "model_unavailable", execution.Chat, 503},
		{"capability", "anthropic", `{"model":"m","input":"hello"}`, "capability_unavailable", execution.Embeddings, 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			channels := lifecycleChannels("http://upstream.test")
			channels[0].Type = tc.channelType
			hits := 0
			g, err := newRegisteredGateway(Options{Channels: channels, BillingMode: billing.ModeDisabled, TotalTimeout: time.Second,
				Client: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
					hits++
					return nil, context.Canceled
				})}})
			if err != nil {
				t.Fatal(err)
			}
			defer g.Close()
			result := g.ExecuteTo(outputIdentity(), &observerOutput{}, Execution{Operation: tc.operation, Body: []byte(tc.body)})
			var out strings.Builder
			if err := g.Metrics().Render(&out); err != nil {
				t.Fatal(err)
			}
			if hits != 0 || result.StatusCode != tc.status || !strings.Contains(out.String(),
				`gateway_route_selections_total{outcome="`+tc.outcome+`",picker="none"} 1`) {
				t.Fatalf("preflight failure was missing or caused dispatch: hits=%d result=%+v metrics=%s", hits, result, out.String())
			}
		})
	}
}

func TestRouteSelectionDebugFactsDoNotExposeCredentialsOrSession(t *testing.T) {
	var log bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&log, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(previous) })
	g := testCoordinator(t)
	ctx := access.WithRequestID(context.Background(), "request-route-1")
	ch := &channel{id: 4, credentials: []model.Credential{{ID: 9, APIKey: "secret-upstream-key", Status: model.CredentialActive}}}
	g.selectTargetContext(ctx, []*channel{ch}, nil, "secret-session-text", time.Now())
	for _, want := range []string{`"request_id":"request-route-1"`, `"outcome":"selected"`, `"channel_id":4`, `"credential_id":9`} {
		if !strings.Contains(log.String(), want) {
			t.Fatalf("missing correlated decision fact %s: %s", want, log.String())
		}
	}
	if strings.Contains(log.String(), "secret-upstream-key") || strings.Contains(log.String(), "secret-session-text") {
		t.Fatalf("routing diagnostics exposed a secret: %s", log.String())
	}
}
