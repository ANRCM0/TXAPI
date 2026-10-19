package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"minigateway/internal/access"
	"minigateway/internal/billing"
	"minigateway/internal/core"
	execution "minigateway/internal/foundation/contracts/execution/v1"
	routes "minigateway/internal/foundation/contracts/routes/v1"
	dbmodel "minigateway/internal/model"
)

const routingSuccess = `{"choices":[],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`

func routeTestGateway(t *testing.T, opts Options) *Gateway {
	t.Helper()
	opts.BillingMode = billing.ModeDisabled
	g, err := newRegisteredGateway(opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(g.Close)
	return g
}

func routeTestResponse(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body))}
}

func routeHTTPExecute(g *Gateway, ctx context.Context, output core.Output, in Execution) RequestResult {
	return g.executeTo(ctx, output, in, nil, true)
}

func TestRouteDiagnosticsRetryFactsAndRedactedCopies(t *testing.T) {
	var hits atomic.Int64
	g := routeTestGateway(t, Options{
		RouteDiagnostics: true, RetryTimes: 2, Channels: lifecycleChannels("http://private-provider.example"),
		Client: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			if hits.Add(1) == 1 {
				return routeTestResponse(503, `{"error":{"message":"private-upstream-error"}}`), nil
			}
			return routeTestResponse(200, routingSuccess), nil
		})},
	})
	ctx := access.WithRequestID(outputIdentity(), "shared-request-id")
	result := routeHTTPExecute(g, ctx, &observerOutput{}, Execution{Operation: execution.Chat, Body: []byte(`{"model":"m","messages":[{"role":"user","content":"private-routing-input"}]}`)})
	if result.StatusCode != 200 || hits.Load() != 2 || len(result.Attempts) != 2 {
		t.Fatalf("observation changed retry execution: %+v hits=%d", result, hits.Load())
	}
	reader := g.Diagnostics()
	admin := routes.Scope{UserID: 9, Administrator: true}
	page, err := reader.List(context.Background(), admin, routes.Query{RequestID: "shared-request-id"})
	if err != nil || len(page.Data) != 1 || !page.Enabled {
		t.Fatal(page, err)
	}
	trace := page.Data[0]
	if trace.Reason != "completed" || len(trace.Selections) != 2 || len(trace.Attempts) != 2 ||
		trace.Attempts[0].Status != 503 || trace.Attempts[1].Status != 200 || trace.Interrupted ||
		trace.Selections[0].CredentialID == trace.Selections[1].CredentialID {
		t.Fatalf("selection/attempt facts lost: %+v", trace)
	}
	own, err := reader.Read(context.Background(), routes.Scope{UserID: 1}, trace.ID)
	if err != nil || own.Selections[0].CredentialID != 0 || own.Attempts[0].ChannelID != 0 || own.Attempts[0].ActualModel != "" {
		t.Fatal("owner acquired target metadata", own, err)
	}
	own.Selections[0].Outcome = "mutated"
	page.Data[0].Attempts[0].ActualModel = "mutated"
	again, err := reader.Read(context.Background(), admin, trace.ID)
	if err != nil || again.Selections[0].Outcome != "selected" || again.Attempts[0].ActualModel != "m" {
		t.Fatal("returned values mutated retained observation", again, err)
	}
	if _, err := reader.Read(context.Background(), routes.Scope{UserID: 2}, trace.ID); !errors.Is(err, routes.ErrNotFound) {
		t.Fatal("cross-owner trace lookup", err)
	}
	encoded, _ := json.Marshal(again)
	for _, secret := range []string{"private-routing-input", "private-upstream-error", "private-provider.example", "Authorization"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("private input retained in diagnostics: %s", secret)
		}
	}
}

func TestRouteDiagnosticsPreflightAndAdmissionAreNotNetworkAttempts(t *testing.T) {
	for _, tc := range []struct {
		name, body, reason string
		deny               AdmissionLevel
		status, selections int
	}{
		{"malformed", "{", "request_rejected", "", 400, 0},
		{"missing model target", `{"model":"missing"}`, "target_unavailable", "", 503, 1},
		{"request capacity", `{"model":"m"}`, "admission_refused", RequestAdmission, 429, 0},
		{"attempt capacity", `{"model":"m"}`, "admission_refused", AttemptAdmission, 429, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hits := 0
			g := routeTestGateway(t, Options{
				RouteDiagnostics: true, Channels: lifecycleChannels("http://upstream.test"),
				Admission: admissionFunc(func(_ context.Context, scope AdmissionScope) (func(), error) {
					if scope.Level == tc.deny {
						return nil, &AdmissionError{StatusCode: 429, Message: "capacity"}
					}
					return func() {}, nil
				}),
				Client: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
					hits++
					return routeTestResponse(200, routingSuccess), nil
				})},
			})
			result := routeHTTPExecute(g, outputIdentity(), &observerOutput{}, Execution{Operation: execution.Chat, Body: []byte(tc.body)})
			page, err := g.Diagnostics().List(context.Background(), routes.Scope{UserID: 1}, routes.Query{})
			if err != nil || len(page.Data) != 1 {
				t.Fatal(page, err)
			}
			trace := page.Data[0]
			if hits != 0 || result.StatusCode != tc.status || trace.Status != tc.status ||
				trace.Reason != tc.reason || len(trace.Attempts) != 0 || len(trace.Selections) != tc.selections {
				t.Fatalf("selection was mistaken for dispatch: hits=%d result=%+v trace=%+v", hits, result, trace)
			}
		})
	}
}

func TestRouteDiagnosticsStrictRefusalKeepsBudgetAndNetworkUnstarted(t *testing.T) {
	g, err := newRegisteredGateway(Options{RouteDiagnostics: true, BillingMode: billing.ModeStrict, Channels: lifecycleChannels("http://upstream.test")})
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	result := routeHTTPExecute(g, outputIdentity(), &observerOutput{}, Execution{Operation: execution.Chat, Body: []byte(`{"model":"m","max_tokens":4}`)})
	page, err := g.Diagnostics().List(context.Background(), routes.Scope{UserID: 1}, routes.Query{})
	if err != nil || len(page.Data) != 1 || result.StatusCode != 503 || result.BillingID != "" ||
		page.Data[0].Reason != "billing_refused" || len(page.Data[0].Attempts) != 0 || len(page.Data[0].Selections) != 0 {
		t.Fatal(result, page, err)
	}
}

func TestRouteDiagnosticsConcurrentEqualRequestIDsNeverShareScope(t *testing.T) {
	g := routeTestGateway(t, Options{RouteDiagnostics: true, Channels: lifecycleChannels("http://upstream.test"),
		Client: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return routeTestResponse(200, routingSuccess), nil
		})}})
	var group sync.WaitGroup
	results := make(chan RequestResult, 24)
	for i := 0; i < 24; i++ {
		group.Add(1)
		go func(owner uint) {
			defer group.Done()
			ctx := access.WithPrincipal(context.Background(), access.Principal{UserID: owner, TokenID: owner * 10, Policy: access.Policy{Mode: access.All}})
			ctx = access.WithRequestID(ctx, "shared")
			results <- routeHTTPExecute(g, ctx, &observerOutput{}, Execution{Operation: execution.Chat, Body: []byte(`{"model":"m"}`)})
		}(uint(i%2 + 1))
	}
	group.Wait()
	close(results)
	for result := range results {
		if result.StatusCode != 200 {
			t.Fatal(result)
		}
	}
	ids := map[string]bool{}
	for owner := uint(1); owner <= 2; owner++ {
		page, err := g.Diagnostics().List(context.Background(), routes.Scope{UserID: owner}, routes.Query{RequestID: "shared"})
		if err != nil || len(page.Data) != 12 {
			t.Fatal(page, err)
		}
		for _, trace := range page.Data {
			if ids[trace.ID] || trace.UserID != owner || len(trace.Selections) != 1 || len(trace.Attempts) != 1 {
				t.Fatal("cross-invocation state", trace)
			}
			ids[trace.ID] = true
		}
	}
	page, err := g.Diagnostics().List(context.Background(), routes.Scope{UserID: 9, Administrator: true}, routes.Query{OwnerID: 2, TokenID: 20, Limit: 5, Offset: 5})
	if err != nil || len(page.Data) != 5 || !page.HasMore || len(ids) != 24 {
		t.Fatal(page, err)
	}
}

type routeNestedOutput struct {
	observerOutput
	run  func(context.Context)
	once bool
}

func (o *routeNestedOutput) Commit(ctx context.Context, metadata core.ResponseMetadata) error {
	if !o.once {
		o.once = true
		o.run(ctx)
	}
	return o.observerOutput.Commit(ctx, metadata)
}

func TestRouteDiagnosticsNestedConsumerCannotAppendToParentCapture(t *testing.T) {
	for _, nestedEnabled := range []bool{false, true} {
		t.Run(map[bool]string{false: "disabled", true: "enabled"}[nestedEnabled], func(t *testing.T) {
			opts := Options{RouteDiagnostics: true, Channels: lifecycleChannels("http://upstream.test"),
				Client: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
					return routeTestResponse(200, routingSuccess), nil
				})}}
			outer := routeTestGateway(t, opts)
			opts.RouteDiagnostics = nestedEnabled
			nested := routeTestGateway(t, opts)
			output := &routeNestedOutput{run: func(ctx context.Context) {
				result := routeHTTPExecute(nested, ctx, &observerOutput{}, Execution{Operation: execution.Chat, Body: []byte(`{"model":"m"}`)})
				if result.StatusCode != 200 {
					t.Error(result)
				}
			}}
			result := routeHTTPExecute(outer, outputIdentity(), output, Execution{Operation: execution.Chat, Body: []byte(`{"model":"m"}`)})
			page, err := outer.Diagnostics().List(context.Background(), routes.Scope{UserID: 1}, routes.Query{})
			if result.StatusCode != 200 || err != nil || len(page.Data) != 1 || len(page.Data[0].Selections) != 1 || len(page.Data[0].Attempts) != 1 {
				t.Fatal("nested scope contaminated its parent", result, page, err)
			}
			child, err := nested.Diagnostics().List(context.Background(), routes.Scope{UserID: 1}, routes.Query{})
			if err != nil || child.Enabled != nestedEnabled || len(child.Data) != map[bool]int{false: 0, true: 1}[nestedEnabled] {
				t.Fatal(child, err)
			}
		})
	}
}

func TestRouteDiagnosticsDoesNotRecordNonHTTPConsumers(t *testing.T) {
	g := routeTestGateway(t, Options{RouteDiagnostics: true, Channels: lifecycleChannels("http://upstream.test"),
		Client: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return routeTestResponse(200, routingSuccess), nil
		})}})
	result := g.ExecuteTo(outputIdentity(), &observerOutput{}, Execution{Operation: execution.Chat, Body: []byte(`{"model":"m"}`)})
	page, err := g.Diagnostics().List(context.Background(), routes.Scope{UserID: 1}, routes.Query{})
	if result.StatusCode != 200 || err != nil || !page.Enabled || len(page.Data) != 0 {
		t.Fatal("non-HTTP consumer expanded the observation scope", result, page, err)
	}
}

func TestRouteDiagnosticsCommittedSSEFailureAndScopePanicStayDistinct(t *testing.T) {
	for _, interrupted := range []bool{false, true} {
		t.Run(map[bool]string{false: "truncated SSE", true: "post-response panic"}[interrupted], func(t *testing.T) {
			hits := 0
			opts := Options{RouteDiagnostics: true, RetryTimes: 2, Channels: lifecycleChannels("http://upstream.test"),
				Client: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
					hits++
					if interrupted {
						return routeTestResponse(200, routingSuccess), nil
					}
					return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}},
						Body: io.NopCloser(strings.NewReader("data: {\"choices\":[{\"delta\":{\"content\":\"one\"}}]}\n\n"))}, nil
				})}}
			if interrupted {
				opts.LogRecorder = func(*dbmodel.RequestLog) { panic("private-finalization-error") }
			}
			g := routeTestGateway(t, opts)
			rec := httptest.NewRecorder()
			panicked := false
			func() {
				defer func() { panicked = recover() != nil }()
				routeHTTPExecute(g, outputIdentity(), core.NewHTTPOutput(rec), Execution{Operation: execution.Chat,
					Body: []byte(fmt.Sprintf(`{"model":"m","stream":%t}`, !interrupted))})
			}()
			page, err := g.Diagnostics().List(context.Background(), routes.Scope{UserID: 1}, routes.Query{})
			if err != nil || len(page.Data) != 1 || hits != 1 || rec.Code != 200 || panicked != interrupted {
				t.Fatal("observation changed committed execution", page, err, hits, panicked)
			}
			trace := page.Data[0]
			if trace.HTTPStatus != 200 || !trace.Committed || trace.Interrupted != interrupted || len(trace.Attempts) != 1 {
				t.Fatal(trace)
			}
			if interrupted {
				if trace.Phase != "unknown" || trace.Reason != "scope_interrupted" {
					t.Fatal("panic manufactured a dispatch/success fact", trace)
				}
			} else if trace.Status != 502 || trace.ProtocolComplete {
				t.Fatal("committed stream failure became successful protocol completion", trace)
			}
			encoded, _ := json.Marshal(trace)
			if strings.Contains(string(encoded), "private-finalization-error") {
				t.Fatal("panic error escaped")
			}
		})
	}
}

func TestRouteDiagnosticsRetentionEvictionBoundsAndCancellation(t *testing.T) {
	now := time.Now()
	history := &routeHistory{now: func() time.Time { return now }}
	reader := routeReader{history}
	scope := routes.Scope{UserID: 1, Administrator: true}
	result := &RequestResult{Identity: Identity{UserID: 1, TokenID: 1, RequestID: "shared"},
		Operation: execution.Chat, Result: core.Result{StatusCode: 200, Termination: core.Success}}
	capture := &routeCapture{started: now, model: "m"}
	history.record(result, capture, true)
	first, err := reader.List(context.Background(), scope, routes.Query{})
	if err != nil || len(first.Data) != 1 {
		t.Fatal(first, err)
	}
	for i := 0; i < routeCapacity; i++ {
		history.record(result, capture, true)
	}
	page, err := reader.List(context.Background(), scope, routes.Query{Limit: 100, Offset: 500})
	if err != nil || len(page.Data) != 12 || page.HasMore {
		t.Fatal("unbounded or missing retained rows", page, err)
	}
	if _, err := reader.Read(context.Background(), scope, first.Data[0].ID); !errors.Is(err, routes.ErrNotFound) {
		t.Fatal("oldest entry not evicted", err)
	}
	now = now.Add(routeRetention)
	page, err = reader.List(context.Background(), scope, routes.Query{})
	if err != nil || len(page.Data) != 0 || !page.Enabled {
		t.Fatal("expired observations retained", page, err)
	}
	for _, entry := range history.records {
		if entry.trace.ID != "" || entry.trace.Selections != nil {
			t.Fatal("expired metadata references not pruned")
		}
	}
	result.Identity.RequestID = strings.Repeat("sensitive-unbounded-id", 20)
	result.Operation = execution.Operation(strings.Repeat("o", 500))
	result.Error = &core.Error{Error: core.ErrorDetail{Message: "private-error-body", Type: "untrusted-error-type"}}
	result.Attempts = make([]AttemptResult, 20)
	for i := range result.Attempts {
		result.Attempts[i].Model = strings.Repeat("\u732b", 100)
	}
	capture = &routeCapture{started: now, model: strings.Repeat("\u732b", 100)}
	for i := 0; i < 20; i++ {
		capture.selectTarget(routeDecision{outcome: "selected", picker: "weighted"}, 0)
	}
	history.record(result, capture, false)
	page, err = reader.List(context.Background(), scope, routes.Query{})
	if err != nil || len(page.Data) != 1 {
		t.Fatal(page, err)
	}
	trace := page.Data[0]
	if !trace.Truncated || !trace.Interrupted || trace.Phase != "unknown" || trace.Reason != "scope_interrupted" ||
		trace.RequestID != "" || len(trace.Operation) != 64 || len(trace.Model) > 128 || !utf8.ValidString(trace.Model) ||
		len(trace.Selections) != 16 || len(trace.Attempts) != 11 || len(trace.Attempts[0].ActualModel) > 128 {
		t.Fatal("unsafe observation bounds or interrupted facts", trace)
	}
	encoded, _ := json.Marshal(trace)
	if strings.Contains(string(encoded), "private-error-body") || strings.Contains(string(encoded), "untrusted-error-type") {
		t.Fatal("retained raw errors")
	}
	for _, query := range []routes.Query{{Limit: 101}, {Offset: -1}, {Offset: 10001}, {RequestID: "\n"}} {
		if _, err := reader.List(context.Background(), scope, query); !errors.Is(err, routes.ErrInvalid) {
			t.Fatal(query, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := reader.Read(ctx, scope, trace.ID); !errors.Is(err, context.Canceled) {
		t.Fatal("canceled query returned data", err)
	}
}
