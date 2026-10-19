package gateway

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"minigateway/internal/access"
	"minigateway/internal/billing"
	"minigateway/internal/compat/httpoutput"
	"minigateway/internal/core"
	v1 "minigateway/internal/foundation/contracts/execution/v1"
	v2 "minigateway/internal/foundation/contracts/execution/v2"
	q "minigateway/internal/foundation/contracts/quotation/v1"
	history "minigateway/internal/foundation/contracts/usagehistory/v1"
	dbmodel "minigateway/internal/model"
)

type historyRecorderProbe struct {
	mu            sync.Mutex
	starts        []history.Start
	targets       []history.Target
	receipts      []history.Receipt
	finishes      []history.Completion
	events        []string
	fail, panicAt string
	failNumber    int
	hook          func(context.Context, string)
}

func (p *historyRecorderProbe) call(ctx context.Context, event string, number int) error {
	deadline, ok := ctx.Deadline()
	principal, trusted := access.PrincipalFromContext(ctx)
	if !ok || time.Until(deadline) > 2*time.Second || !trusted || principal.UserID != 1 || principal.TokenID != 1 {
		panic("recording context lost bound or trusted owner")
	}
	p.mu.Lock()
	p.events = append(p.events, event)
	p.mu.Unlock()
	if p.hook != nil {
		p.hook(ctx, event)
	}
	if event == p.panicAt {
		panic("private recorder panic")
	}
	if event == p.fail && (p.failNumber == 0 || p.failNumber == number) {
		return errors.New("private recorder database secret")
	}
	return nil
}
func (p *historyRecorderProbe) Begin(ctx context.Context, in history.Start) error {
	p.mu.Lock()
	p.starts = append(p.starts, in)
	p.mu.Unlock()
	return p.call(ctx, "begin", 0)
}
func (p *historyRecorderProbe) Prepare(ctx context.Context, in history.Target) error {
	p.mu.Lock()
	p.targets = append(p.targets, in)
	p.mu.Unlock()
	return p.call(ctx, "prepare", in.Number)
}
func (p *historyRecorderProbe) MayDispatch(ctx context.Context, _ string, number int) error {
	return p.call(ctx, "dispatch", number)
}
func (p *historyRecorderProbe) Observe(ctx context.Context, in history.Receipt) error {
	p.mu.Lock()
	p.receipts = append(p.receipts, in)
	p.mu.Unlock()
	return p.call(ctx, "observe", in.Number)
}
func (p *historyRecorderProbe) Finish(ctx context.Context, in history.Completion) error {
	p.mu.Lock()
	p.finishes = append(p.finishes, in)
	p.mu.Unlock()
	return p.call(ctx, "finish", 0)
}

type historyQuotationProbe struct {
	version string
	calls   int
	err     error
}

func (p *historyQuotationProbe) Current(context.Context) (q.Current, error) {
	p.calls++
	return q.Current{Enabled: true, Version: p.version}, p.err
}
func (*historyQuotationProbe) Calculate(context.Context, q.Input) (q.Quote, error) {
	panic("recording must not calculate a charge")
}

const historyDocument = `{"id":"native-history","object":"response","status":"completed","output":[],"usage":{"input_tokens":10,"output_tokens":4,"total_tokens":14,"input_tokens_details":{"cached_tokens":2,"cache_write_tokens":1},"output_tokens_details":{"reasoning_tokens":3}}}`

func historyGateway(t *testing.T, probe *historyRecorderProbe, options Options, response func(*http.Request) (*http.Response, error)) *Gateway {
	t.Helper()
	options.UsageHistory = probe
	options.BillingMode = billing.ModeDisabled
	if options.Channels == nil {
		options.Channels = lifecycleChannels("http://upstream.test")
	}
	if response == nil {
		response = func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(historyDocument))}, nil
		}
	}
	options.Client = &http.Client{Transport: roundTripFunc(response)}
	g, err := newRegisteredGateway(options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(g.Close)
	return g
}

func historyInvocation(stream bool) Execution {
	return Execution{Operation: v1.Responses, Body: []byte(fmt.Sprintf(`{"model":"m","input":"hello","stream":%t}`, stream))}
}

func TestUsageRecordingPreservesNativeSnapshotsAndPinsOneQuotationVersion(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, usage := range []string{"complete", "zero", "unknown"} {
			t.Run(fmt.Sprintf("%s/stream=%t", usage, stream), func(t *testing.T) {
				probe := &historyRecorderProbe{}
				quote := &historyQuotationProbe{version: strings.Repeat("a", 64)}
				document := historyDocument
				if usage == "zero" {
					document = `{"id":"native-history","object":"response","status":"completed","usage":{"input_tokens":0,"output_tokens":0,"total_tokens":0,"input_tokens_details":{"cached_tokens":0,"cache_write_tokens":0},"output_tokens_details":{"reasoning_tokens":0}}}`
				}
				if usage == "unknown" {
					document = `{"id":"native-history","object":"response","status":"completed"}`
				}
				wire := document
				if stream {
					wire = "data: " + `{"type":"response.completed","response":` + document + "}\n\n"
				}
				hits := 0
				g := historyGateway(t, probe, Options{UsageQuotation: quote, RetryTimes: 2}, func(*http.Request) (*http.Response, error) {
					hits++
					quote.version = strings.Repeat("b", 64)
					return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(wire))}, nil
				})
				ctx := access.WithRequestID(outputIdentity(), "same-client-correlation")
				recorder := httptest.NewRecorder()
				out := g.ExecuteTo(ctx, core.NewHTTPOutput(recorder), historyInvocation(stream))
				if out.StatusCode != 200 || out.RecordingState != "sealed" || hits != 1 || recorder.Body.String() != wire || recorder.Header().Get("X-TXAPI-Usage-ID") != out.UsageRecordID {
					t.Fatalf("recording changed delivery: %+v hits=%d wire=%s", out, hits, recorder.Body.String())
				}
				if !regexp.MustCompile(`^usage_[0-9a-f]{32}$`).MatchString(out.UsageRecordID) || len(probe.starts) != 1 || probe.starts[0].ID == "same-client-correlation" || probe.starts[0].RequestID != "same-client-correlation" || probe.starts[0].MaxAttempts != 3 || probe.starts[0].QuotationVersion != strings.Repeat("a", 64) || quote.calls != 1 {
					t.Fatal("recording identity/version binding changed", probe.starts, quote.calls)
				}
				if len(probe.receipts) != 1 || !probe.receipts[0].Host.ProtocolComplete || !probe.receipts[0].Host.DeliveryCommitted || probe.receipts[0].Host.BytesWritten != int64(len(wire)) || len(probe.finishes) != 1 || probe.finishes[0].Interrupted {
					t.Fatal("host completion was not observed", probe.receipts, probe.finishes)
				}
				observed := probe.receipts[0].Usage
				if usage == "unknown" && (observed.Completeness != v2.Unknown || observed.Counts.InputTokens != nil || observed.Breakdown.CachedInputTokens != nil) {
					t.Fatal("legacy estimation polluted raw observations", observed)
				}
				if usage == "zero" && (observed.Counts.InputTokens == nil || *observed.Counts.InputTokens != 0 || observed.Breakdown.CachedInputTokens == nil || *observed.Breakdown.CachedInputTokens != 0) {
					t.Fatal("zero became unknown", observed)
				}
				if usage == "complete" && (observed.Breakdown.ReasoningOutputTokens == nil || *observed.Breakdown.ReasoningOutputTokens != 3) {
					t.Fatal("native details were lost", observed)
				}
				second := g.ExecuteTo(ctx, core.NewHTTPOutput(httptest.NewRecorder()), historyInvocation(stream))
				if hits != 2 || second.UsageRecordID == out.UsageRecordID {
					t.Fatal("client correlation deduplicated generation", second)
				}
			})
		}
	}
}

func TestUsageRecordingRejectsUnpersistedAttemptsWithFixedErrors(t *testing.T) {
	for _, step := range []string{"begin", "prepare", "dispatch"} {
		t.Run(step, func(t *testing.T) {
			p := &historyRecorderProbe{fail: step}
			hits := 0
			g := historyGateway(t, p, Options{}, func(*http.Request) (*http.Response, error) { hits++; panic("unpersisted attempt dispatched") })
			recorder := httptest.NewRecorder()
			out := g.ExecuteTo(outputIdentity(), core.NewHTTPOutput(recorder), historyInvocation(false))
			if hits != 0 || out.StatusCode != 503 || out.Error == nil || out.Error.Error.Type != "usage_recording_unavailable" || strings.Contains(recorder.Body.String(), "private") {
				t.Fatal("unpersisted attempt was sent or leaked storage error", out, hits, recorder.Body.String())
			}
			if step == "begin" && (out.UsageRecordID != "" || recorder.Header().Get("X-TXAPI-Usage-ID") != "" || len(p.finishes) != 0) {
				t.Fatal("failed Begin advertised a record", out)
			}
			if step != "begin" && (len(p.finishes) != 1 || !p.finishes[0].Interrupted || out.RecordingState == "sealed") {
				t.Fatal("failed attempt registration claimed completeness", out, p.finishes)
			}
			if step != "begin" && p.finishes[0].Host.BytesWritten != int64(recorder.Body.Len()) {
				t.Fatal("invocation omitted coordinator error output bytes", p.finishes[0].Host, recorder.Body.Len())
			}
			if step == "dispatch" && (len(p.receipts) != 1 || p.receipts[0].Host.Phase != v1.NotDispatched) {
				t.Fatal("dispatch rejection invented execution", p.receipts)
			}
		})
	}
}

func TestUsageRecordingPostExecutionFailuresPreserveWireAndOutcome(t *testing.T) {
	for _, step := range []string{"observe", "finish", "panic-observe", "panic-finish"} {
		t.Run(step, func(t *testing.T) {
			p := &historyRecorderProbe{fail: step}
			if name, ok := strings.CutPrefix(step, "panic-"); ok {
				p.fail = ""
				p.panicAt = name
			}
			g := historyGateway(t, p, Options{}, nil)
			recorder := httptest.NewRecorder()
			out := g.ExecuteTo(outputIdentity(), core.NewHTTPOutput(recorder), historyInvocation(false))
			if out.StatusCode != 200 || out.Termination != core.Success || recorder.Body.String() != historyDocument || out.RecordingState == "sealed" || len(out.Attempts) != 1 || len(p.finishes) != 1 {
				t.Fatal("post-execution recording failure replaced actual execution", out, recorder.Body.String())
			}
		})
	}
	probe := &historyRecorderProbe{fail: "observe"}
	hits := 0
	g := historyGateway(t, probe, Options{RetryTimes: 2}, func(*http.Request) (*http.Response, error) {
		hits++
		return &http.Response{StatusCode: 503, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"error":{"message":"original upstream failure","type":"upstream_error"}}`))}, nil
	})
	out := g.ExecuteTo(outputIdentity(), core.NewHTTPOutput(httptest.NewRecorder()), Execution{Operation: v1.Chat, Body: []byte(`{"model":"m"}`)})
	if hits != 1 || len(out.Attempts) != 1 || out.StatusCode != 503 || out.Error == nil || out.Error.Error.Message != "original upstream failure" || out.RecordingState == "sealed" {
		t.Fatal("failed Observe retried upstream or replaced its result", hits, out)
	}
}

func TestUsageRecordingBindsActualRetryTargetsAndKeepsPriorAttempts(t *testing.T) {
	for _, failStep := range []string{"", "prepare", "dispatch"} {
		t.Run("second-"+failStep, func(t *testing.T) {
			p := &historyRecorderProbe{fail: failStep, failNumber: 2}
			channels := lifecycleChannels("http://first.test")
			channels[0].Priority = 10
			channels[0].GroupName = "first-group"
			channels[0].ModelMapping = dbmodel.StringMap{"m": "first-model"}
			channels[0].Credentials = channels[0].Credentials[:1]
			second := channels[0]
			second.ID = 2
			second.Name = "second"
			second.Priority = 0
			second.BaseURL = "http://second.test"
			second.GroupName = "second-group"
			second.ModelMapping = dbmodel.StringMap{"m": "second-model"}
			second.Credentials = []dbmodel.Credential{{ID: 2, APIKey: "two", Status: dbmodel.CredentialActive, Weight: 1}}
			channels = append(channels, second)
			hits := 0
			g := historyGateway(t, p, Options{Channels: channels, RetryTimes: 1}, func(*http.Request) (*http.Response, error) {
				hits++
				status, body := 503, `{"error":{"message":"retry"}}`
				if hits == 2 {
					status = 200
					body = `{"choices":[],"usage":{"prompt_tokens":1,"completion_tokens":0,"total_tokens":1}}`
				}
				return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
			})
			out := g.ExecuteTo(outputIdentity(), core.NewHTTPOutput(httptest.NewRecorder()), Execution{Operation: v1.Chat, Body: []byte(`{"model":"m"}`)})
			if len(p.targets) != 2 || p.targets[0].ActualModel != "first-model" || p.targets[0].Group != "first-group" || p.targets[1].ActualModel != "second-model" || p.targets[1].Group != "second-group" || p.targets[0].Number != 1 || p.targets[1].Number != 2 {
				t.Fatal("retry target binding used final request state", p.targets)
			}
			if failStep == "" {
				if hits != 2 || out.StatusCode != 200 || len(p.receipts) != 2 {
					t.Fatal("recording broke retry", hits, out)
				}
			} else if hits != 1 || out.StatusCode != 503 || len(out.Attempts) != 2 || out.Attempts[0].Phase != core.ResponseReceived || out.Attempts[1].Phase != core.NotDispatched || out.Error.Error.Type != "usage_recording_unavailable" {
				t.Fatal("second-hop registration erased prior execution", hits, out)
			}
			if p.receipts[0].Usage.Completeness != v2.Unknown || p.receipts[0].Usage.Counts.TotalTokens != nil {
				t.Fatal("old decoder acquired enriched observations", p.receipts)
			}
		})
	}
}

func waitHistoryClosing(t *testing.T, g *Gateway) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		g.recordingMu.Lock()
		closed := g.recordingClosed
		g.recordingMu.Unlock()
		if closed {
			return
		}
		runtime.Gosched()
	}
	t.Fatal("Close did not close recording admission")
}

func TestUsageRecordingCloseJoinsWritesAndRejectsNewScopes(t *testing.T) {
	for _, step := range []string{"begin", "observe", "finish"} {
		t.Run(step, func(t *testing.T) {
			started, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			p := &historyRecorderProbe{hook: func(_ context.Context, event string) {
				if event == step {
					close(started)
					<-release
				}
			}}
			if step == "begin" {
				p.fail = "begin"
			}
			g := historyGateway(t, p, Options{}, nil)
			t.Cleanup(unblock)
			submitted := make(chan RequestResult, 1)
			go func() {
				submitted <- g.ExecuteTo(outputIdentity(), core.NewHTTPOutput(httptest.NewRecorder()), historyInvocation(false))
			}()
			select {
			case <-started:
			case <-time.After(time.Second):
				t.Fatal("recording write did not start")
			}
			closed := make(chan struct{})
			go func() { g.Close(); close(closed) }()
			waitHistoryClosing(t, g)
			select {
			case <-closed:
				t.Fatal("Close returned before admitted write and seal")
			default:
			}
			later := g.ExecuteTo(outputIdentity(), core.NewHTTPOutput(httptest.NewRecorder()), historyInvocation(false))
			if later.StatusCode != 503 || later.UsageRecordID != "" || later.Error.Error.Type != "usage_recording_unavailable" {
				t.Fatal("closed recording fell back to unrecorded execution", later)
			}
			unblock()
			select {
			case <-closed:
			case <-time.After(time.Second):
				t.Fatal("Close did not join final recording write")
			}
			select {
			case <-submitted:
			case <-time.After(time.Second):
				t.Fatal("recorded execution did not return")
			}
			p.mu.Lock()
			count := len(p.starts)
			p.mu.Unlock()
			if count != 1 {
				t.Fatal("closing admitted another Begin", count)
			}
		})
	}
}

func TestUsageRecordingCancellationAndPanicRetainBindingAndFinalize(t *testing.T) {
	ctx, cancel := context.WithCancel(outputIdentity())
	probe := &historyRecorderProbe{hook: func(ctx context.Context, event string) {
		if event == "observe" || event == "finish" {
			if ctx.Err() != nil {
				panic("post-execution persistence retained client cancellation")
			}
		}
	}}
	g := historyGateway(t, probe, Options{}, func(*http.Request) (*http.Response, error) { cancel(); return nil, context.Canceled })
	out := g.ExecuteTo(ctx, core.NewHTTPOutput(httptest.NewRecorder()), historyInvocation(false))
	if out.Termination != core.Canceled || len(probe.receipts) != 1 || len(probe.finishes) != 1 || out.RecordingState != "sealed" {
		t.Fatal("cancellation lost durable binding/finalization", out, probe.events)
	}
	panicProbe := &historyRecorderProbe{}
	panicking := historyGateway(t, panicProbe, Options{}, func(*http.Request) (*http.Response, error) { panic("original transport panic") })
	var recovered any
	func() {
		defer func() { recovered = recover() }()
		panicking.ExecuteTo(outputIdentity(), core.NewHTTPOutput(httptest.NewRecorder()), historyInvocation(false))
	}()
	if recovered != "original transport panic" || len(panicProbe.receipts) != 1 || panicProbe.receipts[0].Host.Phase != v1.Dispatching || len(panicProbe.finishes) != 1 || !panicProbe.finishes[0].Interrupted {
		t.Fatal("panic invented nonexecution or lost final recording", recovered, panicProbe.receipts, panicProbe.finishes)
	}
	if host := panicProbe.finishes[0].Host; host.Status != 0 || host.UpstreamStatus != 0 || host.Termination != "" || host.ProtocolComplete || host != panicProbe.receipts[0].Host {
		t.Fatal("panic completion invented terminal facts", host, panicProbe.receipts)
	}
	panicking.Close()
}

func TestUsageRecordingRetryPanicCompletionUsesCurrentAttempt(t *testing.T) {
	probe := &historyRecorderProbe{}
	channels := lifecycleChannels("http://first.test")
	channels[0].Credentials = append(channels[0].Credentials[:1], dbmodel.Credential{ID: 22, APIKey: "retry", Status: dbmodel.CredentialActive, Weight: 1})
	hits := 0
	g := historyGateway(t, probe, Options{Channels: channels, RetryTimes: 1}, func(*http.Request) (*http.Response, error) {
		hits++
		if hits == 2 {
			panic("retry transport panic")
		}
		return &http.Response{StatusCode: 503, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"error":{"message":"retry"}}`))}, nil
	})
	var recovered any
	func() {
		defer func() { recovered = recover() }()
		g.ExecuteTo(outputIdentity(), core.NewHTTPOutput(httptest.NewRecorder()), Execution{Operation: v1.Chat, Body: []byte(`{"model":"m"}`)})
	}()
	if recovered != "retry transport panic" || hits != 2 || len(probe.receipts) != 2 || len(probe.finishes) != 1 {
		t.Fatal("retry panic did not finalize", recovered, hits, probe.receipts, probe.finishes)
	}
	first, last, completion := probe.receipts[0].Host, probe.receipts[1].Host, probe.finishes[0]
	if first.Status != 503 || last.Phase != v1.Dispatching || !completion.Interrupted || completion.Host != last || completion.Host.Status != 0 || completion.Host.UpstreamStatus != 0 || completion.Host.Termination != "" || completion.Host.ProtocolComplete {
		t.Fatal("retry panic retained the previous terminal outcome", first, last, completion)
	}
}

func TestUsageRecordingFinishRequestPanicRetainsReturnedFacts(t *testing.T) {
	probe := &historyRecorderProbe{}
	g := historyGateway(t, probe, Options{LogRecorder: func(*dbmodel.RequestLog) { panic("finish request panic") }}, nil)
	var recovered any
	func() {
		defer func() { recovered = recover() }()
		g.ExecuteTo(outputIdentity(), core.NewHTTPOutput(httptest.NewRecorder()), historyInvocation(false))
	}()
	if recovered != "finish request panic" || len(probe.receipts) != 1 || len(probe.finishes) != 1 {
		t.Fatal("finish request panic did not finalize", recovered, probe.receipts, probe.finishes)
	}
	host := probe.finishes[0].Host
	if !probe.finishes[0].Interrupted || host != probe.receipts[0].Host || host.Status != 200 || !host.ProtocolComplete || host.Termination != v1.Success || !host.DeliveryCommitted || host.BytesWritten == 0 {
		t.Fatal("post-execution panic erased returned facts", host, probe.receipts)
	}
}

func TestUsageRecordingForegroundSourcesAndConstructionBoundary(t *testing.T) {
	probe := &historyRecorderProbe{}
	g := historyGateway(t, probe, Options{}, nil)
	for _, source := range []invocationSource{invocationUnspecified, invocationClaimed} {
		out := g.executeFrom(outputIdentity(), core.NewHTTPOutput(httptest.NewRecorder()), historyInvocation(false), nil, false, source)
		if out.StatusCode != 200 || out.UsageRecordID != "" {
			t.Fatal("excluded invocation source entered recording", source, out)
		}
	}
	for _, invoke := range []func(){
		func() {
			NewPublicExecutor(g).Execute(outputIdentity(), httpoutput.New(httptest.NewRecorder()), v1.Invocation{Operation: v1.Responses, Body: historyInvocation(false).Body})
		},
		func() {
			NewHTTPExecutor(g).Execute(outputIdentity(), httpoutput.New(httptest.NewRecorder()), v1.Invocation{Operation: v1.Responses, Body: historyInvocation(false).Body})
		},
		func() {
			NewFactsExecutor(g).Execute(outputIdentity(), httpoutput.New(httptest.NewRecorder()), v1.Invocation{Operation: v1.Responses, Body: historyInvocation(false).Body})
		},
		func() {
			NewUsageFactsExecutor(g).Execute(outputIdentity(), httpoutput.New(httptest.NewRecorder()), v1.Invocation{Operation: v1.Responses, Body: historyInvocation(false).Body})
		},
	} {
		invoke()
	}
	if len(probe.starts) != 4 || len(probe.finishes) != 4 {
		t.Fatal("shared foreground entry bypassed recording", probe.events)
	}
	var typedNil *historyRecorderProbe
	for _, options := range []Options{{BillingMode: billing.ModeLegacy, UsageHistory: probe}, {BillingMode: billing.ModeStrict, UsageHistory: probe}, {BillingMode: billing.ModeDisabled, UsageHistory: typedNil}} {
		if accepted, err := newRegisteredGateway(options); err == nil {
			accepted.Close()
			t.Fatal("invalid recording configuration accepted")
		}
	}
	var nilQuote *historyQuotationProbe
	if accepted, err := newRegisteredGateway(Options{BillingMode: billing.ModeDisabled, UsageQuotation: nilQuote}); err == nil {
		accepted.Close()
		t.Fatal("typed nil quotation reader accepted")
	}
	var calls atomic.Int64
	denied := historyGateway(t, &historyRecorderProbe{}, Options{}, func(*http.Request) (*http.Response, error) { calls.Add(1); panic("unauthorized execution") })
	deniedProbe := denied.usageHistory.(*historyRecorderProbe)
	result := denied.ExecuteTo(context.Background(), core.NewHTTPOutput(httptest.NewRecorder()), historyInvocation(false))
	if result.StatusCode != 401 || calls.Load() != 0 || len(deniedProbe.starts) != 0 {
		t.Fatal("unauthorized invocation reached recording", result)
	}
}

func TestUsageRecordingFiltersCorrelationAndKeepsUnavailableQuotationUnknown(t *testing.T) {
	for _, correlation := range []string{strings.Repeat("x", 65), "unsafe\nclient", "client\x00id"} {
		probe := &historyRecorderProbe{}
		quote := &historyQuotationProbe{version: strings.Repeat("a", 64), err: errors.New("private quotation failure")}
		g := historyGateway(t, probe, Options{UsageQuotation: quote}, nil)
		ctx := access.WithRequestID(outputIdentity(), correlation)
		out := g.ExecuteTo(ctx, core.NewHTTPOutput(httptest.NewRecorder()), historyInvocation(false))
		if out.StatusCode != 200 || out.RecordingState != "sealed" || len(probe.starts) != 1 || probe.starts[0].RequestID != "" || probe.starts[0].QuotationVersion != "" || quote.calls != 1 {
			t.Fatal("untrusted correlation or missing quotation changed generation", out, probe.starts)
		}
	}
}

func TestUsageRecordingPreservesShortWriteHostFactsWithoutPretendingSuccess(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, failure := range []string{"short_write", "commit_before", "commit_after"} {
			probe := &historyRecorderProbe{}
			wire := historyDocument
			if stream {
				wire = "data: " + `{"type":"response.completed","response":` + historyDocument + "}\n\n"
			}
			g := historyGateway(t, probe, Options{}, func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(wire))}, nil
			})
			out := g.ExecuteTo(outputIdentity(), &observerOutput{failure: failure}, historyInvocation(stream))
			var written int64
			if failure == "short_write" {
				written = 1
			}
			committed := failure != "commit_before"
			if out.Termination != core.ClientWriteError || len(probe.receipts) != 1 || probe.receipts[0].Host.Termination != v1.ClientWriteError || probe.receipts[0].Host.BytesWritten != written || probe.receipts[0].Host.DeliveryCommitted != committed || out.RecordingState != "sealed" || len(probe.finishes) != 1 || probe.finishes[0].Host.BytesWritten != written {
				t.Fatal("recording hid delivery failure or dropped observed quantities", out, probe.receipts)
			}
			if probe.receipts[0].Host.ProtocolComplete == stream {
				t.Fatal("recording rewrote existing host protocol-completion boundary", probe.receipts[0].Host)
			}
		}
	}
}

type historyMetadataPanicOutput struct {
	observerOutput
	calls int
}

func (o *historyMetadataPanicOutput) State() core.OutputState {
	o.calls++
	if o.calls == 2 {
		panic("original metadata state panic")
	}
	return o.observerOutput.State()
}

func TestUsageRecordingBeginSuccessStillFinalizesWhenMetadataOutputPanics(t *testing.T) {
	probe := &historyRecorderProbe{}
	hits := 0
	g := historyGateway(t, probe, Options{}, func(*http.Request) (*http.Response, error) { hits++; panic("metadata failure dispatched upstream") })
	var recovered any
	func() {
		defer func() { recovered = recover() }()
		g.ExecuteTo(outputIdentity(), &historyMetadataPanicOutput{}, historyInvocation(false))
	}()
	if recovered != "original metadata state panic" || hits != 0 || len(probe.starts) != 1 || len(probe.finishes) != 1 || !probe.finishes[0].Interrupted || len(probe.targets) != 0 {
		t.Fatal("successful Begin escaped finalization before handoff", recovered, hits, probe.events)
	}
	if host := probe.finishes[0].Host; host.Phase != v1.NotDispatched || host.Status != 0 || host.UpstreamStatus != 0 || host.Termination != "" || host.ProtocolComplete {
		t.Fatal("handoff panic invented terminal facts", host)
	}
	g.Close()
}

type historyPrepareAdapter struct {
	factAdapter
	panics bool
}

func (a historyPrepareAdapter) Prepare(v1.Request, v1.Target) (v1.Input, *v1.Error) {
	if a.panics {
		panic("adapter prepare panic")
	}
	return v1.Input{}, &v1.Error{StatusCode: 400, Error: v1.ErrorDetail{Type: "prepare_failure", Message: "prepare failed"}}
}

func TestUsageRecordingPreparePanicAndReturnedErrorsKeepDistinctFacts(t *testing.T) {
	for _, scenario := range []string{"prepare-panic", "prepare-error", "no-channel"} {
		t.Run(scenario, func(t *testing.T) {
			probe := &historyRecorderProbe{}
			opts := Options{}
			if scenario == "no-channel" {
				opts.Channels = []dbmodel.Channel{}
			} else {
				opts.Protocols = []v1.ProtocolRegistration{{ID: "openai", Version: v1.Version, Adapter: historyPrepareAdapter{panics: scenario == "prepare-panic"}}}
			}
			g := historyGateway(t, probe, opts, func(*http.Request) (*http.Response, error) { t.Fatal("prepare/no-channel dispatched"); return nil, nil })
			var recovered any
			var outcome RequestResult
			func() {
				defer func() { recovered = recover() }()
				outcome = g.ExecuteTo(outputIdentity(), core.NewHTTPOutput(httptest.NewRecorder()), Execution{Operation: v1.Chat, Body: []byte(`{"model":"m"}`)})
			}()
			if len(probe.finishes) != 1 {
				t.Fatal("missing completion", probe.events)
			}
			completion := probe.finishes[0]
			if scenario == "prepare-panic" {
				if recovered != "adapter prepare panic" || len(probe.receipts) != 1 || !completion.Interrupted || completion.Host != probe.receipts[0].Host || completion.Host.Phase != v1.NotDispatched || completion.Host.Status != 0 || completion.Host.UpstreamStatus != 0 || completion.Host.Termination != "" {
					t.Fatal("prepare panic invented terminal facts", recovered, completion, probe.receipts)
				}
			} else {
				want := 400
				if scenario == "no-channel" {
					want = 503
				}
				if recovered != nil || completion.Interrupted || completion.Host.Status != want || outcome.StatusCode != want {
					t.Fatal("normal return lost real rejection", recovered, outcome, completion)
				}
			}
		})
	}
}
