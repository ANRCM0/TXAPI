package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	execv1 "minigateway/internal/foundation/contracts/execution/v1"
	"net/http"
	"strings"
	"testing"

	"minigateway/internal/access"
	"minigateway/internal/billing"
	"minigateway/internal/core"
	dbmodel "minigateway/internal/model"
	"minigateway/internal/protocol"
)

func TestResponsesSharesCoordinatorAndPreservesNativeTerminalFacts(t *testing.T) {
	for _, status := range []string{"completed", "incomplete", "failed", "cancelled"} {
		t.Run(status, func(t *testing.T) {
			var requests, attempts, releases, hits int
			g, err := newRegisteredGateway(Options{BillingMode: billing.ModeDisabled, RetryTimes: 2, Channels: lifecycleChannels("http://upstream.test"),
				Admission: admissionFunc(func(_ context.Context, scope AdmissionScope) (func(), error) {
					if scope.Operation != protocol.Responses {
						t.Error("operation lost at admission")
					}
					if scope.Level == RequestAdmission {
						requests++
					} else {
						attempts++
					}
					return func() { releases++ }, nil
				}),
				Client: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
					hits++
					if r.URL.Path != "/v1/responses" {
						t.Error("Responses was routed through Chat")
					}
					return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"id":"resp_upstream","object":"response","model":"m","status":"` + status + `","output":[],"usage":{"input_tokens":2,"output_tokens":1,"total_tokens":3}}`))}, nil
				})}})
			if err != nil {
				t.Fatal(err)
			}
			defer g.Close()
			out := &observerOutput{}
			result := g.ExecuteTo(outputIdentity(), out, Execution{Operation: protocol.Responses, ResponseID: "resp_local", Body: []byte(`{"model":"m","input":"hello","store":false}`)})
			want := 200
			if status == "failed" || status == "cancelled" {
				want = 502
			}
			if result.StatusCode != want || result.ResponseStatus != status || result.UpstreamResponseID != "resp_upstream" || !result.ProtocolComplete || result.Replayable || result.Usage == nil || result.Usage.TotalTokens != 3 {
				t.Fatalf("native terminal facts lost: %+v", result)
			}
			if requests != 1 || attempts != 1 || releases != 2 || hits != 1 {
				t.Fatalf("duplicate lifecycle: requests=%d attempts=%d releases=%d hits=%d", requests, attempts, releases, hits)
			}
			if !strings.Contains(out.data.String(), "resp_local") || strings.Contains(out.data.String(), "resp_upstream") {
				t.Fatal("upstream response ID escaped to native output")
			}
		})
	}
}

func TestResponsesStrictAndClaimedRefuseBeforeAdmissionOrBilling(t *testing.T) {
	g, err := newRegisteredGateway(Options{BillingMode: billing.ModeStrict, StrictBilling: forbiddenStrictBilling{}, Admission: admissionFunc(func(context.Context, AdmissionScope) (func(), error) {
		t.Fatal("unproved Responses reached admission")
		return nil, nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	in := Execution{Operation: protocol.Responses, Body: []byte(`{"model":"m","input":"hello"}`)}
	for _, claimed := range []bool{false, true} {
		out := &observerOutput{}
		var result RequestResult
		if claimed {
			result = g.ExecuteClaimedTo(outputIdentity(), out, in, dbmodel.GenerationClaim{})
		} else {
			result = g.ExecuteTo(outputIdentity(), out, in)
		}
		if result.StatusCode != 400 || result.Phase != core.NotDispatched || result.Error == nil || result.Error.Error.Type != "unsupported_billing_dimension" {
			t.Fatalf("unsupported strict operation was not refused: %+v", result)
		}
	}
}

func TestResponsesUsesIndependentTokenEstimates(t *testing.T) {
	req, err := protocol.ParseRequest(protocol.Responses, []byte(`{"model":"m","instructions":"instruction-marker","input":[{"role":"user","content":[{"type":"input_text","text":"input-marker"}]}],"tools":[{"type":"function","name":"tool-marker","description":"tool-description","parameters":{"type":"object","properties":{}}}],"max_output_tokens":321,"store":false}`))
	if err != nil {
		t.Fatal(err)
	}
	text := requestPromptText(req)
	for _, marker := range []string{"instruction-marker", "input-marker", "tool-marker", "tool-description"} {
		if !strings.Contains(text, marker) {
			t.Errorf("Responses estimate lost %s", marker)
		}
	}
	if requestMaxTokens(req) != 321 {
		t.Fatal("Responses output limit used Chat max_tokens")
	}
	if got := requestCompletionText(req, []byte(`{"output":[{"type":"message","content":[{"type":"output_text","text":"answer-marker"}]},{"type":"function_call","arguments":"arguments-marker"}]}`)); !strings.Contains(got, "answer-marker") || !strings.Contains(got, "arguments-marker") {
		t.Fatal("Responses output items were not estimated independently")
	}
}

func TestResponsesSSEPreservesNativeFramesAndDoesNotInventDone(t *testing.T) {
	for _, status := range []string{"completed", "incomplete", "failed", "cancelled"} {
		t.Run(status, func(t *testing.T) {
			wire := "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"sequence_number\":0,\"delta\":\"hello\"}\n\n" +
				"event: response." + status + "\ndata: {\"type\":\"response." + status + "\",\"sequence_number\":1,\"response\":{\"id\":\"resp_upstream\",\"object\":\"response\",\"status\":\"" + status + "\",\"output\":[],\"usage\":{\"input_tokens\":2,\"output_tokens\":1,\"total_tokens\":3}}}\n\n"
			g, err := newRegisteredGateway(Options{BillingMode: billing.ModeDisabled, Channels: lifecycleChannels("http://upstream.test"), Client: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(wire))}, nil
			})}})
			if err != nil {
				t.Fatal(err)
			}
			defer g.Close()
			out := &observerOutput{}
			result := g.ExecuteTo(outputIdentity(), out, Execution{Operation: protocol.Responses, ResponseID: "resp_local", Body: []byte(`{"model":"m","input":"hello","stream":true,"store":false}`)})
			if result.ResponseStatus != status || !result.ProtocolComplete || result.UpstreamResponseID != "resp_upstream" || result.Usage == nil || result.Usage.TotalTokens != 3 {
				t.Fatalf("SSE terminal facts lost: %+v", result)
			}
			if !strings.Contains(out.data.String(), "event: response."+status) || !strings.Contains(out.data.String(), "resp_local") || strings.Contains(out.data.String(), "[DONE]") {
				t.Fatal("native SSE framing or public identity changed")
			}
		})
	}
}

type responseLegacyProbe struct {
	legacyBillingProbe
	max   int
	usage execv1.Usage
}

func (p *responseLegacyProbe) PreRequest(user, key uint, max int, model string) (*billing.PreDeduct, error) {
	p.max = max
	return p.legacyBillingProbe.PreRequest(user, key, max, model)
}
func (p *responseLegacyProbe) PriceOfSnapshot(_ *billing.PreDeduct, usage *execv1.Usage, _, _, _ string) (float64, bool) {
	p.usage = *usage
	return 0, true
}

func TestResponsesLegacyAndTPMUseNativeInputAndMissingUsageEstimate(t *testing.T) {
	probe := &responseLegacyProbe{}
	limiter := &generationLimiter{adjustments: map[string]int{}}
	g, err := newRegisteredGateway(Options{BillingMode: billing.ModeLegacy, Billing: probe, Limiter: limiter, Channels: lifecycleChannels("http://upstream.test"), Client: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"id":"resp_upstream","object":"response","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"a nonempty native answer"}]}]}`))}, nil
	})}})
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	ctx := access.WithPrincipal(context.Background(), access.Principal{UserID: 1, TokenID: 1, Policy: access.Policy{Mode: access.All}, RPMLimit: 10, TPMLimit: 1000})
	result := g.ExecuteTo(ctx, &observerOutput{}, Execution{Operation: protocol.Responses, ResponseID: "resp_local", Body: []byte(`{"model":"m","input":"a nonempty native input","instructions":"instructions counted separately","max_output_tokens":321,"store":false}`)})
	if result.StatusCode != 200 || probe.max != 321 || probe.usage.PromptTokens <= 0 || probe.usage.CompletionTokens <= 0 || limiter.rpm.Load() != 1 || limiter.tpm.Load() != 1 {
		t.Fatalf("native legacy/rate projection lost: result=%+v max=%d usage=%+v rpm=%d tpm=%d", result, probe.max, probe.usage, limiter.rpm.Load(), limiter.tpm.Load())
	}
}

func TestResponsesNonNativeErrorsAreSanitizedBeforeOutputLogsAndHealth(t *testing.T) {
	for _, transportFailure := range []bool{false, true} {
		t.Run(map[bool]string{false: "http_error", true: "transport_error"}[transportFailure], func(t *testing.T) {
			secret := "private-fixture-api-key-791"
			url := "https://private-upstream.invalid/request?api_key=" + secret
			var hits int
			var logged, health string
			g, err := newRegisteredGateway(Options{BillingMode: billing.ModeDisabled, RetryTimes: 2, Channels: lifecycleChannels("http://upstream.test"),
				Client: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
					hits++
					if transportFailure {
						return nil, errors.New(url)
					}
					body, _ := json.Marshal(map[string]any{"error": map[string]any{"message": url, "type": secret, "code": map[string]string{"private": secret}}})
					return &http.Response{StatusCode: 429, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(body)))}, nil
				})},
				LogRecorder:    func(row *dbmodel.RequestLog) { logged = row.ErrorMessage },
				HealthReporter: func(_ *dbmodel.Credential, _ int, message string) { health = message },
			})
			if err != nil {
				t.Fatal(err)
			}
			defer g.Close()
			out := &observerOutput{}
			result := g.ExecuteTo(outputIdentity(), out, Execution{Operation: protocol.Responses, ResponseID: "resp_local", Body: []byte(`{"model":"m","input":"hello","store":false}`)})
			wantStatus, wantPhase := 429, core.ResponseReceived
			if transportFailure {
				wantStatus, wantPhase = 502, core.Dispatching
			}
			if hits != 1 || result.StatusCode != wantStatus || result.Phase != wantPhase || result.Replayable || result.Error == nil || result.Error.Error.Code != nil || result.Error.Error.Type != "upstream_response_error" {
				t.Fatalf("sanitized failure lost local facts: %+v hits=%d", result, hits)
			}
			serialized, _ := json.Marshal(result)
			for _, value := range []string{out.data.String(), string(serialized), logged, health} {
				if strings.Contains(value, secret) || strings.Contains(value, "private-upstream.invalid") {
					t.Fatal("provider error detail escaped the Responses boundary")
				}
			}
			if logged == "" || out.data.Len() == 0 {
				t.Fatal("error sanitization bypassed normal log or output delivery")
			}
		})
	}
}
