package gateway

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"minigateway/internal/billing"
	"minigateway/internal/compat/httpoutput"
	"minigateway/internal/core"
	v1 "minigateway/internal/foundation/contracts/execution/v1"
	v2 "minigateway/internal/foundation/contracts/execution/v2"
)

func TestFactsExecutorPreservesNativeDeliveryAndUsageCompleteness(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, tc := range []struct {
			name, usage string
			want        v2.Completeness
		}{
			{"complete", `,"usage":{"input_tokens":2,"output_tokens":1,"total_tokens":3}`, v2.Complete},
			{"zero", `,"usage":{"input_tokens":0,"output_tokens":0,"total_tokens":0}`, v2.Complete},
			{"partial", `,"usage":{"input_tokens":0}`, v2.Partial},
			{"unknown", "", v2.Unknown},
		} {
			t.Run(fmt.Sprintf("%s/stream=%v", tc.name, stream), func(t *testing.T) {
				document := `{"id":"native-one","object":"response","status":"completed","output":[],"future_extension":true` + tc.usage + `}`
				wire := document
				if stream {
					wire = "event: response.completed\ndata: " + `{"type":"response.completed","sequence_number":0,"response":` + document + "}\n\n"
				}
				hits := 0
				g, err := newRegisteredGateway(Options{Channels: lifecycleChannels("http://upstream.test"), BillingMode: billing.ModeDisabled,
					Client: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
						hits++
						return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(wire))}, nil
					})}})
				if err != nil {
					t.Fatal(err)
				}
				defer g.Close()
				recorder := httptest.NewRecorder()
				out := NewFactsExecutor(g).Execute(outputIdentity(), httpoutput.New(recorder), v1.Invocation{Operation: v1.Responses,
					Body: []byte(fmt.Sprintf(`{"model":"m","input":"hello","stream":%v}`, stream))})
				if hits != 1 || recorder.Body.String() != wire || len(out.Attempts) != 1 || len(out.Legacy.Attempts) != 1 || out.Legacy.StatusCode != 200 {
					t.Fatalf("native delivery was duplicated or changed: hits=%d outcome=%+v wire=%s", hits, out, recorder.Body.String())
				}
				facts := out.Attempts[0]
				if facts.Number != 1 || facts.Dispatch != v1.ResponseReceived || !facts.ProtocolComplete || !facts.DeliveryCommitted ||
					facts.BytesWritten != int64(len(wire)) || facts.Native.ID != "native-one" || facts.Native.Status != "completed" || facts.Usage.Completeness != tc.want {
					t.Fatalf("execution facts were not independent observations: %+v", facts)
				}
				if (out.Legacy.Usage != nil) != (tc.want == v2.Complete) {
					t.Fatalf("partial or absent usage fabricated a legacy count: %+v", out)
				}
				if tc.want == v2.Unknown {
					if facts.Usage.Source != v2.SourceUnknown || facts.Usage.Profile != "" || facts.Usage.Counts.InputTokens != nil {
						t.Fatalf("unknown usage acquired provider provenance: %+v", facts.Usage)
					}
				} else if facts.Usage.Source != v2.ProviderReported || facts.Usage.Profile != "responses_native_tokens_v1" || facts.Usage.Counts.InputTokens == nil {
					t.Fatalf("explicit zero or provenance was lost: %+v", facts.Usage)
				}
			})
		}
	}
}

func TestFactsKeepProtocolTerminalWhenDownstreamWriteFails(t *testing.T) {
	for _, tc := range []struct {
		name, failure string
		stream        bool
	}{{"json short write", "short_write", false}, {"SSE short write", "short_write", true}, {"SSE flush", "flush", true}} {
		t.Run(tc.name, func(t *testing.T) {
			stream := tc.stream
			document := `{"id":"native","object":"response","status":"completed","usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`
			wire := document
			if stream {
				wire = `data: {"type":"response.completed","response":` + document + "}\n\n"
			}
			g, err := newRegisteredGateway(Options{Channels: lifecycleChannels("http://upstream.test"), Client: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(wire))}, nil
			})}})
			if err != nil {
				t.Fatal(err)
			}
			defer g.Close()
			output := &observerOutput{failure: tc.failure}
			out := NewFactsExecutor(g).Execute(outputIdentity(), publicTestOutput{output}, v1.Invocation{Operation: v1.Responses,
				Body: []byte(fmt.Sprintf(`{"model":"m","input":"x","stream":%v}`, stream))})
			bytesWritten := int64(1)
			if tc.failure == "flush" {
				bytesWritten = int64(len(wire))
			}
			if len(out.Attempts) != 1 || out.Legacy.Termination != v1.ClientWriteError || out.Attempts[0].Native.Status != "completed" ||
				!out.Attempts[0].DeliveryCommitted || out.Attempts[0].BytesWritten != bytesWritten || out.Attempts[0].Usage.Completeness != v2.Complete {
				t.Fatalf("delivery failure erased protocol facts or retried: %+v", out)
			}
			if !stream && !out.Attempts[0].ProtocolComplete {
				t.Fatal("validated JSON terminal was erased by a partial downstream write")
			}
			if stream && out.Attempts[0].ProtocolComplete {
				t.Fatal("failed SSE delivery bypassed the core End verification")
			}
		})
	}
}

func TestFactsDoNotInventProvenanceForLegacyDecoder(t *testing.T) {
	g, err := newRegisteredGateway(Options{Channels: lifecycleChannels("http://upstream.test"), Client: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"choices":[],"usage":{"prompt_tokens":2,"completion_tokens":1,"total_tokens":3}}`))}, nil
	})}})
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	out := NewFactsExecutor(g).Execute(outputIdentity(), httpoutput.New(httptest.NewRecorder()), v1.Invocation{Operation: v1.Chat, Body: []byte(`{"model":"m"}`)})
	if out.Legacy.StatusCode != 200 || out.Legacy.Usage == nil || out.Legacy.Usage.TotalTokens != 3 || len(out.Attempts) != 1 || out.Attempts[0].Usage.Completeness != v2.Unknown || out.Attempts[0].Usage.Source != v2.SourceUnknown || out.Attempts[0].Usage.Counts.TotalTokens != nil {
		t.Fatalf("v1 usage was lost or promoted to an unproved v2 profile: %+v", out)
	}
}

type publicTestOutput struct{ target *observerOutput }

func (o publicTestOutput) Commit(ctx context.Context, m v1.ResponseMetadata) error {
	return o.target.Commit(ctx, core.ResponseMetadata{StatusCode: m.StatusCode, ContentType: m.ContentType, Values: m.Values})
}
func (o publicTestOutput) Write(ctx context.Context, data []byte) (int, error) {
	return o.target.Write(ctx, data)
}
func (o publicTestOutput) Flush(ctx context.Context) error { return o.target.Flush(ctx) }
func (o publicTestOutput) State() v1.OutputState {
	state := o.target.State()
	return v1.OutputState{Committed: state.Committed, HTTPStatus: state.HTTPStatus}
}

func TestFactsDoNotInventAttemptsForAdmissionOrAuthorizationRejection(t *testing.T) {
	var hits atomic.Int64
	g, err := newRegisteredGateway(Options{Channels: lifecycleChannels("http://upstream.test"), Admission: admissionFunc(func(_ context.Context, scope AdmissionScope) (func(), error) {
		if scope.Level == AttemptAdmission {
			return nil, &AdmissionError{StatusCode: 429, Message: "full"}
		}
		return func() {}, nil
	}), Client: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		hits.Add(1)
		return nil, context.Canceled
	})}})
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	for _, ctx := range []context.Context{context.Background(), outputIdentity()} {
		out := NewFactsExecutor(g).Execute(ctx, httpoutput.New(httptest.NewRecorder()), v1.Invocation{Operation: v1.Responses, Body: []byte(`{"model":"m","input":"x"}`)})
		if out.Legacy.StatusCode < 400 || len(out.Attempts) != 0 || len(out.Legacy.Attempts) != 0 || hits.Load() != 0 {
			t.Fatalf("rejected request acquired attempt facts: %+v", out)
		}
	}
}

type factAdapter struct{ decoder v1.Decoder }

func (a factAdapter) Name() string                               { return "openai" }
func (a factAdapter) Supports(v1.Request) bool                   { return true }
func (a factAdapter) CheckOutputLimit([]byte, int, string) error { return nil }
func (a factAdapter) Prepare(r v1.Request, target v1.Target) (v1.Input, *v1.Error) {
	return v1.Input{URL: target.BaseURL, Decoder: a.decoder, Stream: r.Stream(), Replayable: true}, nil
}

type factDecoder struct {
	envelope     v2.Envelope
	legacy, rich int
}

func (d *factDecoder) Decode([]byte) ([]byte, *v1.Usage, error) {
	d.legacy++
	panic("legacy decoder called after explicit v2 capability")
}
func (d *factDecoder) NewStream() v1.StreamDecoder { panic("unexpected legacy stream") }
func (d *factDecoder) DecodeEnvelope([]byte) (v2.Envelope, error) {
	d.rich++
	return d.envelope, nil
}
func (d *factDecoder) NewEnvelopeStream() v2.StreamDecoder { return nil }

func TestFactsCapabilityIsSelectedOnceAndCopiesCountsAcrossRetries(t *testing.T) {
	input, output, total := int64(1), int64(1), int64(2)
	decoder := &factDecoder{envelope: v2.Envelope{Data: []byte(`{"ok":true}`), ProtocolTerminal: true,
		Usage: &v2.UsageObservation{Counts: v2.TokenCounts{InputTokens: &input, OutputTokens: &output, TotalTokens: &total}, Completeness: v2.Complete, Source: v2.ProviderReported, Profile: "test-v1"}}}
	hits := 0
	g, err := newRegisteredGateway(Options{Channels: lifecycleChannels("http://upstream.test"), RetryTimes: 1, TotalTimeout: time.Second,
		Protocols: []v1.ProtocolRegistration{{ID: "openai", Version: v1.Version, Adapter: factAdapter{decoder}}},
		Client: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			hits++
			status, body := 200, `{}`
			if hits == 1 {
				status, body = 503, `{"error":{"message":"retry"}}`
			}
			return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
		})}})
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	recorder := httptest.NewRecorder()
	out := NewFactsExecutor(g).Execute(outputIdentity(), httpoutput.New(recorder), v1.Invocation{Operation: v1.Chat, Body: []byte(`{"model":"m"}`)})
	input, output, total = 50, 50, 100
	if hits != 2 || decoder.rich != 1 || decoder.legacy != 0 || recorder.Body.String() != `{"ok":true}` || len(out.Attempts) != 2 ||
		out.Attempts[0].Usage.Completeness != v2.Unknown || out.Attempts[1].Number != 2 || *out.Attempts[1].Usage.Counts.TotalTokens != 2 || out.Legacy.Usage.TotalTokens != 2 {
		t.Fatalf("fallback, count aliasing or attempt contamination: hits=%d decoder=%+v outcome=%+v", hits, decoder, out)
	}
}

func TestFactsBridgeRejectsInvalidOrRegressingSnapshots(t *testing.T) {
	ptr := func(n int64) *int64 { return &n }
	for _, tc := range []struct {
		name         string
		observations []v2.Envelope
	}{
		{"inconsistent", []v2.Envelope{{Usage: &v2.UsageObservation{Counts: v2.TokenCounts{InputTokens: ptr(1), OutputTokens: ptr(1), TotalTokens: ptr(3)}, Completeness: v2.Complete, Source: v2.ProviderReported, Profile: "test"}}}},
		{"negative partial", []v2.Envelope{{Usage: &v2.UsageObservation{Counts: v2.TokenCounts{InputTokens: ptr(-1)}, Completeness: v2.Partial, Source: v2.ProviderReported, Profile: "test"}}}},
		{"unknown with count", []v2.Envelope{{Usage: &v2.UsageObservation{Counts: v2.TokenCounts{InputTokens: ptr(0)}, Completeness: v2.Unknown, Source: v2.SourceUnknown}}}},
		{"decreasing", []v2.Envelope{{Usage: &v2.UsageObservation{Counts: v2.TokenCounts{InputTokens: ptr(2)}, Completeness: v2.Partial, Source: v2.ProviderReported, Profile: "test"}}, {Usage: &v2.UsageObservation{Counts: v2.TokenCounts{InputTokens: ptr(1)}, Completeness: v2.Partial, Source: v2.ProviderReported, Profile: "test"}}}},
		{"duplicate terminal", []v2.Envelope{{ProtocolTerminal: true}, {ProtocolTerminal: true}}},
		{"nonterminal failure", []v2.Envelope{{Failed: true}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			facts := protocolFacts{usage: unknownUsageFact()}
			var err error
			for _, envelope := range tc.observations {
				_, err = facts.accept(envelope)
				if err != nil {
					break
				}
			}
			if err == nil {
				t.Fatal("invalid protocol facts accepted")
			}
		})
	}
}

func TestFactsPanicKeepsDispatchConservative(t *testing.T) {
	g, err := newRegisteredGateway(Options{Channels: lifecycleChannels("http://upstream.test"), Client: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		panic("transport failed after dispatch began")
	})}})
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	ctx := outputIdentity()
	result := newRequestResult(ctx)
	scope := newOutputScope(ctx, &observerOutput{}, result, "")
	request, err := g.ParseRequest(v1.Chat, []byte(`{"model":"m"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if recover() == nil {
			t.Fatal("panic was hidden")
		}
		if result.Phase != core.Dispatching || len(result.protocolFacts) != 1 || result.protocolFacts[0].Dispatch != v1.Dispatching {
			t.Fatalf("panic was misrepresented as non-dispatch: %+v", result)
		}
	}()
	g.doRelay(scope, &v1.Target{ChannelType: "openai", ChannelID: 1, BaseURL: "http://upstream.test", APIKey: "test", OriginModel: "m", ActualModel: "m", Operation: v1.Chat}, request, time.Second)
}
