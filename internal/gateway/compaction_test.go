package gateway

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"minigateway/internal/access"
	"minigateway/internal/billing"
	"minigateway/internal/core"
	"minigateway/internal/protocol"
)

const compactTriggerBody = `{"model":"m","store":false,"input":[{"role":"user","content":"before"},{"type":"compaction_trigger"}]}`
const compactOpaqueBody = `{"model":"m","store":false,"input":[{"id":"cmp_fixture","type":"compaction","encrypted_content":"opaque-fixture"},{"role":"user","content":"after"}]}`
const compactResult = `{"id":"resp_remote","object":"response","status":"completed","output":[{"id":"cmp_fixture","type":"compaction","encrypted_content":"opaque-fixture","future":{"response_id":"business"}}],"usage":{"input_tokens":7}}`

func TestCompactionBillingScopeBeforeAdmission(t *testing.T) {
	for _, mode := range []billing.Mode{billing.ModeDisabled, billing.ModeLegacy, billing.ModeStrict} {
		for _, body := range []string{compactTriggerBody, compactOpaqueBody} {
			t.Run(string(mode)+body[40:48], func(t *testing.T) {
				hits, admissions, releases := 0, 0, 0
				g, err := newRegisteredGateway(Options{BillingMode: mode, Channels: lifecycleChannels("http://fixture.test"), RetryTimes: 2,
					Admission: admissionFunc(func(context.Context, AdmissionScope) (func(), error) {
						admissions++
						return func() { releases++ }, nil
					}), Client: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
						hits++
						wire, _ := io.ReadAll(r.Body)
						if r.URL.Path != "/v1/responses" || !strings.Contains(string(wire), "compaction") || r.Header.Get("X-Openai-Internal-Codex-Responses-Lite") != "" {
							t.Error("compact request semantics changed", r.URL, string(wire))
						}
						return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(compactResult))}, nil
					})}})
				if err != nil {
					t.Fatal(err)
				}
				defer g.Close()
				output := &observerOutput{}
				result := g.ExecuteTo(outputIdentity(), output, Execution{Operation: protocol.Responses, ResponseID: "resp_public", Body: []byte(body)})
				if mode == billing.ModeDisabled {
					if result.StatusCode != 200 || !result.ProtocolComplete || result.Replayable || hits != 1 || admissions != 2 || releases != 2 || result.Usage != nil {
						t.Fatalf("compact lifecycle or partial usage changed: %+v hits=%d admission=%d release=%d", result, hits, admissions, releases)
					}
					if !strings.Contains(output.data.String(), "opaque-fixture") || strings.Contains(output.data.String(), "resp_remote") || !strings.Contains(output.data.String(), "business") {
						t.Fatal("opaque output, envelope identity or metadata changed", output.data.String())
					}
				} else if result.StatusCode != 400 || result.Phase != core.NotDispatched || result.Error == nil || result.Error.Error.Type != "unsupported_billing_dimension" || len(result.Attempts) != 0 || hits != 0 || admissions != 0 {
					t.Fatalf("compact reached unproved billing: %+v hits=%d admissions=%d", result, hits, admissions)
				}
			})
		}
	}
}

func TestCompactionDoesNotExpandManagedOrCrossProtocol(t *testing.T) {
	for _, body := range []string{compactTriggerBody, compactOpaqueBody} {
		t.Run(body[40:48], func(t *testing.T) {
			a, d := &managedAccessFixture{}, &managedDirectoryFixture{}
			g := newManagedTestGateway(t, a, d, func(*http.Request) (*http.Response, error) {
				t.Fatal("compact acquired a managed grant or fell back to static")
				return nil, nil
			})
			streaming := strings.TrimSuffix(body, "}") + `,"stream":true}`
			result := g.ExecuteTo(outputIdentity(), &observerOutput{}, Execution{Operation: protocol.Responses, Body: []byte(streaming)})
			if result.StatusCode != 400 || result.Phase != core.NotDispatched || len(a.requests) != 0 || len(d.requests) != 0 || len(result.Attempts) != 0 {
				t.Fatalf("managed compact boundary expanded: %+v", result)
			}
		})
	}
	for _, provider := range []string{"anthropic", "gemini"} {
		channels := lifecycleChannels("http://fixture.test")
		channels[0].Type = provider
		g, err := newRegisteredGateway(Options{BillingMode: billing.ModeDisabled, Channels: channels,
			Client: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				t.Fatal("compact converted to another protocol")
				return nil, nil
			})}})
		if err != nil {
			t.Fatal(err)
		}
		result := g.ExecuteTo(outputIdentity(), &observerOutput{}, Execution{Operation: protocol.Responses, Body: []byte(compactTriggerBody)})
		g.Close()
		if result.StatusCode != 400 || len(result.Attempts) != 0 {
			t.Fatalf("cross-protocol compact: %+v", result)
		}
	}
}

func TestCompactionAuthorizationAndFailureNeverReplay(t *testing.T) {
	for _, status := range []int{200, 429, 500} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			hits := 0
			g, err := newRegisteredGateway(Options{BillingMode: billing.ModeDisabled, RetryTimes: 2, Channels: lifecycleChannels("http://fixture.test"),
				Client: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
					hits++
					wire := `{"error":{"message":"private-provider-key"}}`
					if status == 200 {
						wire = `{"id":"resp_remote","object":"response","status":"completed","output":[]}`
					}
					return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(wire))}, nil
				})}})
			if err != nil {
				t.Fatal(err)
			}
			defer g.Close()
			denied := access.WithPrincipal(context.Background(), access.Principal{UserID: 1, TokenID: 1, Policy: access.Policy{Mode: access.Allowlist, Models: []string{"other"}}})
			result := g.ExecuteTo(denied, &observerOutput{}, Execution{Operation: protocol.Responses, Body: []byte(compactTriggerBody)})
			if result.StatusCode != 403 || hits != 0 || len(result.Attempts) != 0 {
				t.Fatal("model authorization bypassed", result)
			}
			output := &observerOutput{}
			result = g.ExecuteTo(outputIdentity(), output, Execution{Operation: protocol.Responses, Body: []byte(compactTriggerBody)})
			if result.Error == nil || result.Replayable || hits != 1 || len(result.Attempts) != 1 || strings.Contains(output.data.String(), "private-provider-key") || result.ProtocolComplete {
				t.Fatalf("false compaction success or replay: %+v hits=%d body=%s", result, hits, output.data.String())
			}
		})
	}
}

func TestCompactionCancellationReleasesRealSocketAndCapacity(t *testing.T) {
	started, stopped := make(chan struct{}), make(chan struct{})
	var hits, acquired, released atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "event: response.compaction.compacting\ndata: {\"type\":\"response.compaction.compacting\"}\n\n")
		w.(http.Flusher).Flush()
		close(started)
		<-r.Context().Done()
		close(stopped)
	}))
	defer server.Close()
	g, err := newRegisteredGateway(Options{BillingMode: billing.ModeDisabled, RetryTimes: 2, Channels: lifecycleChannels(server.URL),
		Admission: admissionFunc(func(context.Context, AdmissionScope) (func(), error) {
			acquired.Add(1)
			return func() { released.Add(1) }, nil
		})})
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	ctx, cancel := context.WithCancel(outputIdentity())
	defer cancel()
	done := make(chan RequestResult, 1)
	go func() {
		done <- g.ExecuteTo(ctx, &observerOutput{}, Execution{Operation: protocol.Responses, Body: []byte(strings.TrimSuffix(compactTriggerBody, "}") + `,"stream":true}`)})
	}()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("upstream did not start")
	}
	cancel()
	select {
	case result := <-done:
		if (result.Termination != core.Canceled && result.Termination != core.ClientWriteError) || result.ProtocolComplete || result.Replayable || len(result.Attempts) != 1 || result.Attempts[0].Termination != core.Canceled {
			t.Fatalf("cancel fabricated compaction: %+v", result)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("canceled compact did not join")
	}
	select {
	case <-stopped:
	case <-time.After(3 * time.Second):
		t.Fatal("upstream socket was not canceled")
	}
	if hits.Load() != 1 || acquired.Load() != 2 || released.Load() != 2 {
		t.Fatalf("capacity/replay: hits=%d acquired=%d released=%d", hits.Load(), acquired.Load(), released.Load())
	}
}

func TestCompactionClassificationUsesValidatedInputOnly(t *testing.T) {
	r, err := protocol.ParseRequest(protocol.Responses, []byte(`{"model":"m","input":"x","metadata":{"type":"compaction_trigger"}}`))
	if err != nil || hasCompaction(r) {
		t.Fatal("metadata became an execution instruction", err)
	}
	var raw map[string]json.RawMessage
	_ = json.Unmarshal(r.Body(), &raw)
	if string(raw["metadata"]) != `{"type":"compaction_trigger"}` {
		t.Fatal("metadata was stripped")
	}
}
