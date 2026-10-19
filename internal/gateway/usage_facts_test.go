package gateway

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"minigateway/internal/compat/httpoutput"
	"minigateway/internal/core"
	v1 "minigateway/internal/foundation/contracts/execution/v1"
	v2 "minigateway/internal/foundation/contracts/execution/v2"
	v3 "minigateway/internal/foundation/contracts/execution/v3"
)

func usageCount(n int64) *int64 { return &n }

// The fixture models an explicit codec projection, never a host profile mapping.
func usageEnvelope(usage v3.UsageObservation) v3.Envelope {
	base := unknownUsageFact()
	base.Counts = usage.Counts
	known := 0
	for _, count := range []*int64{base.Counts.InputTokens, base.Counts.OutputTokens, base.Counts.TotalTokens} {
		if count != nil {
			known++
		}
	}
	if known != 0 {
		base.Completeness, base.Source, base.Profile = v2.Partial, v2.ProviderReported, "fixture-base-v1"
		if known == 3 {
			base.Completeness = v2.Complete
		}
	}
	return v3.Envelope{Execution: v2.Envelope{Usage: &base}, Usage: &usage}
}

func completeUsageEnvelope() v3.Envelope {
	return usageEnvelope(v3.UsageObservation{Counts: v2.TokenCounts{InputTokens: usageCount(10), OutputTokens: usageCount(4), TotalTokens: usageCount(14)},
		Breakdown:    v3.TokenBreakdown{CachedInputTokens: usageCount(2), CacheWriteInputTokens: usageCount(1), ReasoningOutputTokens: usageCount(3)},
		Completeness: v2.Complete, Source: v2.ProviderReported, Profile: "fixture-enriched-v2"})
}

func TestUsageFactsKeepSixHighWaterMarksAcrossUnknownSnapshots(t *testing.T) {
	for index := range 6 {
		t.Run(fmt.Sprint(index), func(t *testing.T) {
			first := v3.UsageObservation{Completeness: v2.Partial, Source: v2.ProviderReported, Profile: "fixture-enriched-v2"}
			fields := []**int64{&first.Counts.InputTokens, &first.Counts.OutputTokens, &first.Counts.TotalTokens,
				&first.Breakdown.CachedInputTokens, &first.Breakdown.CacheWriteInputTokens, &first.Breakdown.ReasoningOutputTokens}
			*fields[index] = usageCount(5)
			facts, usage := &protocolFacts{usage: unknownUsageFact()}, &usageFacts{usage: unknownDetailedUsageFact()}
			if _, err := usage.accept(facts, usageEnvelope(first)); err != nil {
				t.Fatal(err)
			}
			if _, err := usage.accept(facts, usageEnvelope(unknownDetailedUsageFact())); err != nil {
				t.Fatal(err)
			}
			if usage.usage.Completeness != v2.Unknown || usageCounts(usage.usage)[index] != nil {
				t.Fatal("unknown snapshot retained a stale observed count")
			}
			*fields[index] = usageCount(4)
			if _, err := usage.accept(facts, usageEnvelope(first)); err == nil {
				t.Fatal("count regressed across an unknown snapshot")
			}
			if usage.usage.Completeness != v2.Unknown || facts.usage.Completeness != v2.Unknown {
				t.Fatal("rejected observation changed the accepted snapshot")
			}
		})
	}
}

func TestUsageFactsDoNotPromoteStaleBreakdownsAndKeepProfiles(t *testing.T) {
	for _, changeBase := range []bool{false, true} {
		t.Run(fmt.Sprint(changeBase), func(t *testing.T) {
			facts, usage := &protocolFacts{usage: unknownUsageFact()}, &usageFacts{usage: unknownDetailedUsageFact()}
			first := completeUsageEnvelope()
			if _, err := usage.accept(facts, first); err != nil {
				t.Fatal(err)
			}
			next := completeUsageEnvelope()
			next.Usage.Breakdown = v3.TokenBreakdown{}
			if _, err := usage.accept(facts, next); err != nil || usage.usage.Breakdown.CachedInputTokens != nil || usage.usage.Completeness != v2.Complete {
				t.Fatal("missing breakdown was carried forward or downgraded complete totals", err)
			}
			if _, err := usage.accept(facts, usageEnvelope(unknownDetailedUsageFact())); err != nil {
				t.Fatal(err)
			}
			changed := completeUsageEnvelope()
			if changeBase {
				changed.Execution.Usage.Profile = "another-base"
			} else {
				changed.Usage.Profile = "another-enriched"
			}
			if _, err := usage.accept(facts, changed); err == nil {
				t.Fatal("profile changed across unknown usage")
			}
		})
	}
}

func TestUsageFactsHistoricalChildrenBoundLaterExplicitParents(t *testing.T) {
	for _, tc := range []struct {
		name         string
		first, later v3.UsageObservation
	}{
		{"cached", v3.UsageObservation{Breakdown: v3.TokenBreakdown{CachedInputTokens: usageCount(5)}}, v3.UsageObservation{Counts: v2.TokenCounts{InputTokens: usageCount(4)}}},
		{"cache sum", v3.UsageObservation{Breakdown: v3.TokenBreakdown{CachedInputTokens: usageCount(3), CacheWriteInputTokens: usageCount(3)}}, v3.UsageObservation{Counts: v2.TokenCounts{InputTokens: usageCount(5)}}},
		{"reasoning", v3.UsageObservation{Breakdown: v3.TokenBreakdown{ReasoningOutputTokens: usageCount(5)}}, v3.UsageObservation{Counts: v2.TokenCounts{OutputTokens: usageCount(4)}}},
		{"total", v3.UsageObservation{Counts: v2.TokenCounts{InputTokens: usageCount(5), OutputTokens: usageCount(4)}}, v3.UsageObservation{Counts: v2.TokenCounts{TotalTokens: usageCount(8)}}},
	} {
		for _, terminal := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/terminal=%t", tc.name, terminal), func(t *testing.T) {
				facts, usage := &protocolFacts{usage: unknownUsageFact()}, &usageFacts{usage: unknownDetailedUsageFact()}
				for _, observation := range []*v3.UsageObservation{&tc.first, &tc.later} {
					observation.Completeness, observation.Source, observation.Profile = v2.Partial, v2.ProviderReported, "fixture-enriched-v2"
				}
				if _, err := usage.accept(facts, usageEnvelope(tc.first)); err != nil {
					t.Fatal(err)
				}
				if _, err := usage.accept(facts, usageEnvelope(unknownDetailedUsageFact())); err != nil {
					t.Fatal(err)
				}
				later := usageEnvelope(tc.later)
				later.Execution.ProtocolTerminal = terminal
				if _, err := usage.accept(facts, later); err == nil || facts.terminal || usage.usage.Completeness != v2.Unknown {
					t.Fatal("later explicit parent contradicted cumulative child lower bounds", err)
				}
			})
		}
	}
	// An old parent is only a lower bound: it cannot reject a later child when
	// the current parent is unknown and may itself have increased.
	facts, usage := &protocolFacts{usage: unknownUsageFact()}, &usageFacts{usage: unknownDetailedUsageFact()}
	first := usageEnvelope(v3.UsageObservation{Counts: v2.TokenCounts{InputTokens: usageCount(4)}, Completeness: v2.Partial, Source: v2.ProviderReported, Profile: "fixture-enriched-v2"})
	later := usageEnvelope(v3.UsageObservation{Breakdown: v3.TokenBreakdown{CachedInputTokens: usageCount(5)}, Completeness: v2.Partial, Source: v2.ProviderReported, Profile: "fixture-enriched-v2"})
	later.Execution.ProtocolTerminal = true
	if _, err := usage.accept(facts, first); err != nil {
		t.Fatal(err)
	}
	frame, err := usage.accept(facts, later)
	if err != nil || frame.Usage != nil || usage.usage.Counts.InputTokens != nil || *usage.usage.Breakdown.CachedInputTokens != 5 {
		t.Fatal("old parent was treated as current upper bound or inferred into accounting", err)
	}
}

func TestUsageFactsHistoricalBreakdownsBoundLaterTotalTransitively(t *testing.T) {
	for _, tc := range []struct {
		name    string
		prior   []v3.UsageObservation
		minimum int64
	}{
		{"cached", []v3.UsageObservation{{Breakdown: v3.TokenBreakdown{CachedInputTokens: usageCount(5)}}}, 5},
		{"reasoning", []v3.UsageObservation{{Breakdown: v3.TokenBreakdown{ReasoningOutputTokens: usageCount(5)}}}, 5},
		{"split cache and reasoning", []v3.UsageObservation{
			{Breakdown: v3.TokenBreakdown{CachedInputTokens: usageCount(3)}},
			{Breakdown: v3.TokenBreakdown{CacheWriteInputTokens: usageCount(4)}},
			{Breakdown: v3.TokenBreakdown{ReasoningOutputTokens: usageCount(2)}},
		}, 9},
		{"parent counts dominate subsets", []v3.UsageObservation{
			{Counts: v2.TokenCounts{InputTokens: usageCount(10)}},
			{Breakdown: v3.TokenBreakdown{CachedInputTokens: usageCount(2)}},
			{Breakdown: v3.TokenBreakdown{ReasoningOutputTokens: usageCount(3)}},
			{Counts: v2.TokenCounts{OutputTokens: usageCount(4)}},
		}, 14},
		{"old total is not an upper bound", []v3.UsageObservation{
			{Counts: v2.TokenCounts{TotalTokens: usageCount(4)}},
			{Breakdown: v3.TokenBreakdown{CachedInputTokens: usageCount(5)}},
		}, 5},
	} {
		for _, total := range []int64{tc.minimum - 1, tc.minimum} {
			t.Run(fmt.Sprintf("%s/total=%d", tc.name, total), func(t *testing.T) {
				facts, usage := &protocolFacts{usage: unknownUsageFact()}, &usageFacts{usage: unknownDetailedUsageFact()}
				for _, observation := range tc.prior {
					observation.Completeness, observation.Source, observation.Profile = v2.Partial, v2.ProviderReported, "fixture-enriched-v2"
					if _, err := usage.accept(facts, usageEnvelope(observation)); err != nil {
						t.Fatal(err)
					}
					if _, err := usage.accept(facts, usageEnvelope(unknownDetailedUsageFact())); err != nil {
						t.Fatal(err)
					}
				}
				later := usageEnvelope(v3.UsageObservation{Counts: v2.TokenCounts{TotalTokens: usageCount(total)},
					Completeness: v2.Partial, Source: v2.ProviderReported, Profile: "fixture-enriched-v2"})
				later.Execution.ProtocolTerminal = true
				frame, err := usage.accept(facts, later)
				if total < tc.minimum {
					if err == nil || facts.terminal || usage.usage.Completeness != v2.Unknown {
						t.Fatal("transitive historical lower bound exceeded total", err)
					}
				} else if err != nil || frame.Usage != nil || usage.usage.Counts.InputTokens != nil || usage.usage.Counts.OutputTokens != nil || usage.usage.Breakdown != (v3.TokenBreakdown{}) {
					t.Fatal("lower bounds were double-counted or promoted into the current snapshot", err)
				}
			})
		}
	}
	// Individually valid observations can still prove that no int64 total exists.
	facts, usage := &protocolFacts{usage: unknownUsageFact()}, &usageFacts{usage: unknownDetailedUsageFact()}
	first := usageEnvelope(v3.UsageObservation{Breakdown: v3.TokenBreakdown{CachedInputTokens: usageCount(math.MaxInt64)},
		Completeness: v2.Partial, Source: v2.ProviderReported, Profile: "fixture-enriched-v2"})
	if _, err := usage.accept(facts, first); err != nil {
		t.Fatal(err)
	}
	if _, err := usage.accept(facts, usageEnvelope(unknownDetailedUsageFact())); err != nil {
		t.Fatal(err)
	}
	later := usageEnvelope(v3.UsageObservation{Breakdown: v3.TokenBreakdown{ReasoningOutputTokens: usageCount(1)},
		Completeness: v2.Partial, Source: v2.ProviderReported, Profile: "fixture-enriched-v2"})
	if _, err := usage.accept(facts, later); err == nil {
		t.Fatal("historical transitive sum overflow accepted")
	}
}

func TestUsageFactsRejectProjectionMismatchWithoutAcceptingTerminal(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*v3.Envelope)
	}{
		{"missing enriched", func(e *v3.Envelope) { e.Usage = nil }},
		{"missing legacy", func(e *v3.Envelope) { e.Execution.Usage = nil }},
		{"unequal count", func(e *v3.Envelope) { e.Execution.Usage.Counts.InputTokens = usageCount(9) }},
		{"unequal nil", func(e *v3.Envelope) { e.Execution.Usage.Counts.TotalTokens = nil }},
		{"legacy completeness", func(e *v3.Envelope) { e.Execution.Usage.Completeness = v2.Partial }},
		{"legacy source", func(e *v3.Envelope) { e.Execution.Usage.Source = v2.SourceUnknown }},
		{"enriched parent bound", func(e *v3.Envelope) { e.Usage.Breakdown.CachedInputTokens = usageCount(11) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			facts, usage := &protocolFacts{usage: unknownUsageFact()}, &usageFacts{usage: unknownDetailedUsageFact()}
			envelope := completeUsageEnvelope()
			envelope.Execution.ProtocolTerminal = true
			tc.edit(&envelope)
			if _, err := usage.accept(facts, envelope); err == nil || facts.terminal || usage.usage.Completeness != v2.Unknown {
				t.Fatal("inconsistent projections accepted or changed facts", err)
			}
		})
	}
}

func TestUsageFactsOnlyCompleteTerminalLegacyCountsFeedAccounting(t *testing.T) {
	for _, terminal := range []bool{false, true} {
		facts, usage := &protocolFacts{usage: unknownUsageFact()}, &usageFacts{usage: unknownDetailedUsageFact()}
		details := usageEnvelope(v3.UsageObservation{Breakdown: v3.TokenBreakdown{CachedInputTokens: usageCount(0)},
			Completeness: v2.Partial, Source: v2.ProviderReported, Profile: "fixture-enriched-v2"})
		details.Execution.ProtocolTerminal = terminal
		frame, err := usage.accept(facts, details)
		if err != nil || frame.Usage != nil || usage.usage.Breakdown.CachedInputTokens == nil || facts.usage.Completeness != v2.Unknown {
			t.Fatal("breakdown-only usage manufactured accounting counts", frame, err)
		}
		facts, usage = &protocolFacts{usage: unknownUsageFact()}, &usageFacts{usage: unknownDetailedUsageFact()}
		complete := completeUsageEnvelope()
		complete.Execution.ProtocolTerminal = terminal
		frame, err = usage.accept(facts, complete)
		if err != nil || (frame.Usage != nil) != terminal {
			t.Fatal("nonterminal usage entered accounting or complete terminal counts lost", frame, err)
		}
	}
}

type usageStubDecoder struct {
	*factDecoder
	envelopes []v3.Envelope
	err       error
	calls     int
}

func (d *usageStubDecoder) DecodeUsageEnvelope([]byte) (v3.Envelope, error) {
	d.calls++
	return d.envelopes[0], d.err
}
func (d *usageStubDecoder) NewUsageEnvelopeStream() v3.StreamDecoder {
	d.calls++
	return &usageStubStream{envelopes: d.envelopes, err: d.err}
}

type usageStubStream struct {
	envelopes []v3.Envelope
	err       error
}

func (s *usageStubStream) DecodeUsageEnvelopes([]byte) ([]v3.Envelope, error) {
	result := s.envelopes
	s.envelopes = nil
	return result, s.err
}
func (s *usageStubStream) EndUsageEnvelopes() ([]v3.Envelope, error) { return nil, nil }

func usageStubGateway(t *testing.T, decoder v1.Decoder, options Options) *Gateway {
	t.Helper()
	options.Channels = lifecycleChannels("http://upstream.test")
	options.Protocols = []v1.ProtocolRegistration{{ID: "openai", Version: v1.Version, Adapter: factAdapter{decoder}}}
	g, err := newRegisteredGateway(options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(g.Close)
	return g
}

func TestUsageFactsExecutorPrefersOneCapabilityAndOwnsBothProjections(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprint(stream), func(t *testing.T) {
			envelope := completeUsageEnvelope()
			envelope.Execution.Data = []byte(`{"once":true}`)
			envelope.Execution.ProtocolTerminal = true
			envelope.Execution.Native = &v1.NativeCompletion{ID: "native", Status: "completed"}
			decoder := &usageStubDecoder{factDecoder: &factDecoder{}, envelopes: []v3.Envelope{envelope}}
			hits := 0
			g := usageStubGateway(t, decoder, Options{Client: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				hits++
				return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("data: fixture\n\n"))}, nil
			})}})
			recorder := httptest.NewRecorder()
			out := NewUsageFactsExecutor(g).Execute(outputIdentity(), httpoutput.New(recorder), v1.Invocation{Operation: v1.Chat,
				Body: []byte(fmt.Sprintf(`{"model":"m","stream":%t}`, stream))})
			if hits != 1 || decoder.calls != 1 || decoder.rich != 0 || decoder.legacy != 0 || recorder.Body.String() != `{"once":true}` || len(out.Execution.Attempts) != 1 || len(out.Usage) != 1 {
				t.Fatalf("v3 selection or execution duplicated: %+v decoder=%+v hits=%d body=%s", out, decoder, hits, recorder.Body.String())
			}
			if out.Usage[0].Number != out.Execution.Attempts[0].Number || out.Usage[0].Usage.Profile != "fixture-enriched-v2" || out.Execution.Attempts[0].Usage.Profile != "fixture-base-v1" || out.Execution.Legacy.Usage.TotalTokens != 14 {
				t.Fatal("profiles, attempt identity or accounting projection lost", out)
			}
			*envelope.Usage.Breakdown.CachedInputTokens = 90
			*envelope.Usage.Counts.InputTokens = 90
			if *out.Usage[0].Usage.Breakdown.CachedInputTokens != 2 || *out.Usage[0].Usage.Counts.InputTokens != 10 || *out.Execution.Attempts[0].Usage.Counts.InputTokens != 10 {
				t.Fatal("decoder counts aliased into outcome")
			}
			*out.Usage[0].Usage.Counts.InputTokens = 80
			if *out.Execution.Attempts[0].Usage.Counts.InputTokens != 10 {
				t.Fatal("enriched and legacy outcomes share count pointers")
			}
		})
	}
}

func TestUsageFactsDecoderFailureNeverFallsBackOrInventsCounts(t *testing.T) {
	decoder := &usageStubDecoder{factDecoder: &factDecoder{}, envelopes: []v3.Envelope{{}}, err: errors.New("invalid v3 usage")}
	hits := 0
	g := usageStubGateway(t, decoder, Options{Client: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		hits++
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("{}"))}, nil
	})}})
	out := NewUsageFactsExecutor(g).Execute(outputIdentity(), httpoutput.New(httptest.NewRecorder()), v1.Invocation{Operation: v1.Chat, Body: []byte(`{"model":"m"}`)})
	if hits != 1 || decoder.calls != 1 || decoder.rich != 0 || decoder.legacy != 0 || out.Execution.Legacy.StatusCode < 400 || len(out.Usage) != 1 || out.Usage[0].Usage.Completeness != v2.Unknown {
		t.Fatal("v3 failure fell back or manufactured usage", out, decoder)
	}
}

type usageLegacyDecoder struct{ calls int }

func (d *usageLegacyDecoder) Decode([]byte) ([]byte, *v1.Usage, error) {
	d.calls++
	return []byte(`{"legacy":true}`), &v1.Usage{PromptTokens: 10, CompletionTokens: 4, TotalTokens: 14}, nil
}
func (d *usageLegacyDecoder) NewStream() v1.StreamDecoder { return nil }

func TestUsageFactsFallbackDoesNotInventEnrichedObservations(t *testing.T) {
	base := completeUsageEnvelope().Execution
	base.Data, base.ProtocolTerminal = []byte(`{"base":true}`), true
	for _, decoder := range []v1.Decoder{&factDecoder{envelope: base}, &usageLegacyDecoder{}} {
		g := usageStubGateway(t, decoder, Options{Client: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("{}"))}, nil
		})}})
		out := NewUsageFactsExecutor(g).Execute(outputIdentity(), httpoutput.New(httptest.NewRecorder()), v1.Invocation{Operation: v1.Chat, Body: []byte(`{"model":"m"}`)})
		if out.Execution.Legacy.StatusCode != 200 || out.Execution.Legacy.Usage.TotalTokens != 14 || len(out.Usage) != 1 || out.Usage[0].Usage.Completeness != v2.Unknown || out.Usage[0].Usage.Source != v2.SourceUnknown || out.Usage[0].Usage.Counts.TotalTokens != nil {
			t.Fatal("legacy decoder promoted base usage into an enriched profile", out)
		}
	}
}

func TestUsageFactsCancellationKeepsAttemptWithoutInventingUsage(t *testing.T) {
	decoder := &usageStubDecoder{factDecoder: &factDecoder{}}
	ctx, cancel := context.WithCancel(outputIdentity())
	defer cancel()
	g := usageStubGateway(t, decoder, Options{Client: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		cancel()
		return nil, context.Canceled
	})}})
	out := NewUsageFactsExecutor(g).Execute(ctx, httpoutput.New(httptest.NewRecorder()), v1.Invocation{Operation: v1.Chat, Body: []byte(`{"model":"m"}`)})
	if decoder.calls != 0 || len(out.Usage) != 1 || out.Usage[0].Usage.Completeness != v2.Unknown || out.Execution.Attempts[0].Dispatch == v1.NotDispatched || out.Execution.Legacy.Termination != v1.Canceled {
		t.Fatal("canceled dispatch was erased or acquired usage", out)
	}
}

func TestUsageFactsRetryNumbersIncludeUnknownAttemptsAndPreserveLegacyFacade(t *testing.T) {
	envelope := completeUsageEnvelope()
	envelope.Execution.Data, envelope.Execution.ProtocolTerminal = []byte(`{"once":true}`), true
	decoder := &usageStubDecoder{factDecoder: &factDecoder{}, envelopes: []v3.Envelope{envelope}}
	hits := 0
	g := usageStubGateway(t, decoder, Options{RetryTimes: 1, TotalTimeout: time.Second, Client: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		hits++
		status, body := 200, "{}"
		if hits == 1 {
			status, body = 503, `{"error":{"message":"retry"}}`
		}
		return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})}})
	out := NewUsageFactsExecutor(g).Execute(outputIdentity(), httpoutput.New(httptest.NewRecorder()), v1.Invocation{Operation: v1.Chat, Body: []byte(`{"model":"m"}`)})
	if hits != 2 || len(out.Usage) != 2 || out.Usage[0].Usage.Completeness != v2.Unknown || out.Usage[0].Number != 1 || out.Usage[1].Number != 2 || out.Execution.Attempts[1].Number != 2 {
		t.Fatal("retry facts contaminated or misnumbered", out)
	}
	legacy := NewFactsExecutor(g).Execute(outputIdentity(), httpoutput.New(httptest.NewRecorder()), v1.Invocation{Operation: v1.Chat, Body: []byte(`{"model":"m"}`)})
	if legacy.Legacy.StatusCode != 200 || len(legacy.Attempts) != 1 || legacy.Attempts[0].Usage.Profile != "fixture-base-v1" || *legacy.Attempts[0].Usage.Counts.TotalTokens != 14 {
		t.Fatal("old v2 facade no longer returns explicit base projection", legacy)
	}
}

func TestUsageFactsKeepSnapshotsOnShortWriteAndUnknownDispatchOnPanic(t *testing.T) {
	envelope := completeUsageEnvelope()
	envelope.Execution.Data, envelope.Execution.ProtocolTerminal = []byte(`{"once":true}`), true
	decoder := &usageStubDecoder{factDecoder: &factDecoder{}, envelopes: []v3.Envelope{envelope}}
	g := usageStubGateway(t, decoder, Options{Client: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("{}"))}, nil
	})}})
	out := NewUsageFactsExecutor(g).Execute(outputIdentity(), publicTestOutput{&observerOutput{failure: "short_write"}}, v1.Invocation{Operation: v1.Chat, Body: []byte(`{"model":"m"}`)})
	if out.Execution.Legacy.Termination != v1.ClientWriteError || !out.Execution.Attempts[0].ProtocolComplete || out.Execution.Attempts[0].BytesWritten != 1 || *out.Usage[0].Usage.Breakdown.CachedInputTokens != 2 {
		t.Fatal("short write erased observed usage or terminal facts", out)
	}
	panicGateway := usageStubGateway(t, decoder, Options{Client: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		panic("transport panicked")
	})}})
	ctx := outputIdentity()
	result := newRequestResult(ctx)
	scope := newOutputScope(ctx, &observerOutput{}, result, "")
	request, err := panicGateway.ParseRequest(v1.Chat, []byte(`{"model":"m"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if recover() == nil || result.Phase != core.Dispatching || len(result.usageFacts) != 1 || result.usageFacts[0].Usage.Completeness != v2.Unknown || result.protocolFacts[0].Dispatch != v1.Dispatching {
			t.Fatal("panic manufactured non-dispatch or usage", result)
		}
	}()
	panicGateway.doRelay(scope, &v1.Target{ChannelType: "openai", ChannelID: 1, BaseURL: "http://upstream.test", APIKey: "fixture", OriginModel: "m", ActualModel: "m", Operation: v1.Chat}, request, time.Second)
}
