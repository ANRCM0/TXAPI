package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"minigateway/internal/access"
	"minigateway/internal/billing"
	"minigateway/internal/core"
	v1 "minigateway/internal/foundation/contracts/execution/v1"
	"minigateway/internal/model"
)

func shadowIntegrationChannels(count int) []model.Channel {
	channels := make([]model.Channel, count)
	for i := range channels {
		channels[i] = lifecycleChannels("http://shadow.test")[0]
		channels[i].ID, channels[i].Name = uint(i+1), fmt.Sprintf("shadow-%d", i+1)
		channels[i].Credentials = []model.Credential{{ID: uint(i + 1), APIKey: "private-key", Status: model.CredentialActive, Weight: 1}}
	}
	return channels
}

func newShadowIntegrationGateway(t *testing.T, opts Options) *Gateway {
	t.Helper()
	if opts.Channels == nil {
		opts.Channels = shadowIntegrationChannels(2)
	}
	if opts.Client == nil {
		opts.Client = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) { return routeTestResponse(200, routingSuccess), nil })}
	}
	opts.RouteDiagnostics, opts.ShadowRouteScoring = true, true
	if opts.BillingMode == "" {
		opts.BillingMode = billing.ModeDisabled
	}
	g, err := newRegisteredGateway(opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(g.Close)
	return g
}

func shadowIntegrationProbe(g *Gateway, now time.Time) shadowComparison {
	epoch := g.appliedEpoch.Load()
	return g.shadow.Compare(epoch, [8]shadowCandidate{{channelID: 1, epoch: epoch}, {channelID: 2, epoch: epoch}}, 2, false, 1, now)
}

func assertShadowIntegrationIdle(t *testing.T, g *Gateway, live, failed int, now time.Time) {
	t.Helper()
	comparison := shadowIntegrationProbe(g, now)
	if !comparison.compared {
		t.Fatal("attempt leaked retired/untracked/active accounting", comparison)
	}
	totalLive, totalFailed := 0, 0
	for _, snapshot := range comparison.candidates[:comparison.count] {
		if snapshot.active != 0 {
			t.Fatal("host attempt token leaked", snapshot)
		}
		totalLive += snapshot.live
		totalFailed += snapshot.errors
	}
	if totalLive != live || totalFailed != failed {
		t.Fatal("outcome classification changed", comparison, totalLive, totalFailed, live, failed)
	}
}

func shadowIntegrationScope(t *testing.T, g *Gateway, operation v1.Operation, body string, output core.Output) (*executionScope, v1.Request, *v1.Target) {
	t.Helper()
	request, err := g.operations[operation].Parse([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	result := newRequestResult(outputIdentity())
	result.Operation, result.ChannelID, result.ChannelName, result.selectedEpoch = operation, 1, "shadow-1", g.appliedEpoch.Load()
	scope := newOutputScope(outputIdentity(), output, result, "")
	scope.source = invocationForeground
	channel := g.reg.allChannels()[0]
	for _, candidate := range g.reg.allChannels() {
		if candidate.id == 1 {
			channel = candidate
		}
	}
	return scope, request, buildMeta(channel, &channel.credentials[0], request, "")
}

func TestShadowIntegrationRequiresBothFlagsAndDoesNotChangeWeightedDraws(t *testing.T) {
	for _, diagnostics := range []bool{false, true} {
		for _, enabled := range []bool{false, true} {
			g, err := newRegisteredGateway(Options{Channels: shadowIntegrationChannels(2), RouteDiagnostics: diagnostics, ShadowRouteScoring: enabled})
			if err != nil {
				t.Fatal(err)
			}
			if (g.shadow != nil) != (diagnostics && enabled) {
				t.Fatal("a single flag allocated shadow state", diagnostics, enabled)
			}
			g.Close()
		}
	}
	var baseline []string
	for _, enabled := range []bool{false, true} {
		g, err := newRegisteredGateway(Options{Channels: shadowIntegrationChannels(5), RouteDiagnostics: true, ShadowRouteScoring: enabled})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(g.Close)
		fixed := time.Unix(1700000000, 0)
		channels := g.reg.allChannels()
		for _, channel := range channels {
			channel.priority = 4
			channel.weight = channel.id
			if channel.id == 4 {
				channel.priority = 3
			}
			if channel.id == 5 {
				channel.credentials[0].Status = model.CredentialDisabled
			}
		}
		// Immutable sorting is unnecessary in production. The fixture fixes its
		// own order so identical seeded draws can compare the two configurations.
		for i := range channels {
			for j := i + 1; j < len(channels); j++ {
				if channels[j].id < channels[i].id {
					channels[i], channels[j] = channels[j], channels[i]
				}
			}
		}
		var trace []string
		g.picker = func(credentials []model.Credential, tried map[uint]bool, session string, now time.Time) (*model.Credential, bool) {
			trace = append(trace, fmt.Sprintf("picker:%d:%t:%s:%d", credentials[0].ID, tried[99], session, now.Unix()))
			return &credentials[0], true
		}
		rng := rand.New(rand.NewSource(4815))
		draw := func(bound int) int {
			value := rng.Intn(bound)
			trace = append(trace, fmt.Sprintf("draw:%d:%d", bound, value))
			return value
		}
		for i := 0; i < 24; i++ {
			selected, credential, decision := g.chooseTargetWithDraw(channels, map[uint]bool{99: true}, "stable-affinity", fixed, draw)
			if selected == nil || credential == nil || decision.selectionPoolChannels != 3 {
				t.Fatal("fixture eligibility changed", decision)
			}
			if enabled {
				if decision.shadowPool[0].channelID != 1 || decision.shadowPool[1].channelID != 2 || decision.shadowPool[2].channelID != 3 {
					t.Fatal("shadow pool reselected lower/disabled candidates", decision)
				}
				c := newOutputScope(outputIdentity(), &observerOutput{}, newRequestResult(outputIdentity()), "")
				c.source = invocationForeground
				g.compareShadowRoute(c, decision, selected, fixed)
			}
			trace = append(trace, fmt.Sprintf("choice:%d:%d", selected.id, credential.ID))
		}
		if !enabled {
			baseline = trace
		} else if !reflect.DeepEqual(baseline, trace) {
			t.Fatal("shadow comparison changed weighted RNG or extension/affinity calls", baseline, trace)
		}
	}
}

type shadowFailBody struct{}

func (shadowFailBody) Read([]byte) (int, error) { return 0, errors.New("private body failure") }
func (shadowFailBody) Close() error             { return nil }

type shadowObserverDecoder struct {
	panicObservation bool
	entered, release chan struct{}
}

func (*shadowObserverDecoder) Decode(body []byte) ([]byte, *v1.Usage, error) {
	return body, &v1.Usage{PromptTokens: 1, CompletionTokens: 1, TotalTokens: 2}, nil
}
func (*shadowObserverDecoder) NewStream() v1.StreamDecoder { return nil }
func (d *shadowObserverDecoder) Observation() v1.Observation {
	if d.panicObservation {
		panic("original observer panic")
	}
	if d.entered != nil {
		close(d.entered)
		<-d.release
	}
	return v1.Observation{}
}

type shadowStatePanicOutput struct{ observerOutput }

func (*shadowStatePanicOutput) State() core.OutputState { panic("original output state panic") }

func TestShadowIntegrationClassifiesOnlyActualCleanHTTPOutcomes(t *testing.T) {
	for _, kind := range []string{"clean", "http400", "http502", "http504", "failed502body", "failed200body", "transport", "cancel", "deadline", "decode", "native-failed", "invalid-usage", "commit", "short-write", "state-panic", "transport-panic", "observer-panic", "record-failure", "record-panic", "claimed", "unspecified"} {
		t.Run(kind, func(t *testing.T) {
			opts := Options{}
			status, body := 200, routingSuccess
			operation, requestBody := v1.Chat, `{"model":"m","messages":[{"role":"user","content":"hello"}]}`
			wantLive, wantErrors := 0, 0
			if kind == "clean" {
				wantLive = 1
			}
			if strings.HasPrefix(kind, "http") {
				_, _ = fmt.Sscanf(kind, "http%d", &status)
				body = `{"error":{"message":"explicit HTTP rejection"}}`
				wantLive, wantErrors = 1, 1
			}
			if kind == "native-failed" || kind == "invalid-usage" {
				operation, requestBody = v1.Responses, `{"model":"m","input":"hello"}`
				body = `{"id":"native-result","object":"response","model":"m","status":"failed","output":[]}`
				if kind == "invalid-usage" {
					body = `{"id":"native-result","object":"response","model":"m","status":"completed","output":[],"usage":{"input_tokens":-1,"output_tokens":0,"total_tokens":-1}}`
				}
			}
			if kind == "decode" {
				operation, requestBody = v1.Responses, `{"model":"m","input":"hello"}`
				body = `{"broken"`
			}
			if kind == "observer-panic" {
				opts.Protocols = []v1.ProtocolRegistration{{ID: "openai", Version: v1.Version, Adapter: factAdapter{decoder: &shadowObserverDecoder{panicObservation: true}}}}
			}
			var recorder *historyRecorderProbe
			if kind == "record-failure" || kind == "record-panic" {
				recorder = &historyRecorderProbe{fail: "observe"}
				if kind == "record-panic" {
					recorder.fail = ""
					recorder.panicAt = "observe"
				}
				opts.UsageHistory = recorder
			}
			opts.Client = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				switch kind {
				case "transport":
					return nil, errors.New("private network error")
				case "cancel":
					return nil, context.Canceled
				case "deadline":
					return nil, context.DeadlineExceeded
				case "transport-panic":
					panic("original transport panic")
				case "failed502body":
					return &http.Response{StatusCode: 502, Header: make(http.Header), Body: shadowFailBody{}}, nil
				case "failed200body":
					return &http.Response{StatusCode: 200, Header: make(http.Header), Body: shadowFailBody{}}, nil
				default:
					return routeTestResponse(status, body), nil
				}
			})}
			g := newShadowIntegrationGateway(t, opts)
			var output core.Output = &observerOutput{}
			if kind == "commit" {
				output = &observerOutput{failure: "commit_before"}
			}
			if kind == "short-write" {
				output = &observerOutput{failure: "short_write"}
			}
			if kind == "state-panic" {
				output = &shadowStatePanicOutput{}
			}
			c, req, target := shadowIntegrationScope(t, g, operation, requestBody, output)
			if kind == "claimed" {
				c.source = invocationClaimed
			}
			if kind == "unspecified" {
				c.source = invocationUnspecified
			}
			if recorder != nil {
				finish, err := g.beginUsageRecording(c, req)
				if err != nil {
					t.Fatal(err)
				}
				defer finish(false)
			}
			var recovered any
			func() { defer func() { recovered = recover() }(); g.doRelay(c, target, req, time.Second) }()
			if (kind == "state-panic" || kind == "transport-panic" || kind == "observer-panic") && recovered == nil {
				t.Fatal("fixture did not exercise panic", kind)
			}
			if kind == "record-panic" { /* Recorder panics are intentionally isolated. */
			} else if !strings.HasSuffix(kind, "panic") && recovered != nil {
				t.Fatal("unexpected panic", recovered)
			}
			if kind == "record-panic" && recovered != nil {
				t.Fatal("recorder panic escaped existing containment", recovered)
			}
			assertShadowIntegrationIdle(t, g, wantLive, wantErrors, time.Now())
		})
	}
}

func TestShadowIntegrationKeepsHostActiveThroughObserverAndDurableFinalization(t *testing.T) {
	for _, stage := range []string{"observer", "durable"} {
		t.Run(stage, func(t *testing.T) {
			entered, release := make(chan struct{}), make(chan struct{})
			var releaseOnce sync.Once
			unblock := func() { releaseOnce.Do(func() { close(release) }) }
			t.Cleanup(unblock)
			opts := Options{}
			if stage == "observer" {
				opts.Protocols = []v1.ProtocolRegistration{{ID: "openai", Version: v1.Version, Adapter: factAdapter{decoder: &shadowObserverDecoder{entered: entered, release: release}}}}
			} else {
				opts.UsageHistory = &historyRecorderProbe{hook: func(_ context.Context, event string) {
					if event == "observe" {
						close(entered)
						<-release
					}
				}}
			}
			g := newShadowIntegrationGateway(t, opts)
			c, req, target := shadowIntegrationScope(t, g, v1.Chat, `{"model":"m","messages":[]}`, &observerOutput{})
			if stage == "durable" {
				finish, err := g.beginUsageRecording(c, req)
				if err != nil {
					t.Fatal(err)
				}
				defer finish(true)
			}
			done := make(chan struct{})
			go func() { defer close(done); g.doRelay(c, target, req, time.Second) }()
			select {
			case <-entered:
			case <-time.After(3 * time.Second):
				t.Fatal("finalizer did not block")
			}
			comparison := shadowIntegrationProbe(g, time.Now())
			active := uint64(0)
			for _, candidate := range comparison.candidates[:comparison.count] {
				active += candidate.active
				if candidate.live != 0 {
					t.Fatal("outcome appended before finalization", candidate)
				}
			}
			if !comparison.compared || active != 1 {
				t.Fatal("host occupancy released before finalization", comparison)
			}
			unblock()
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				t.Fatal("attempt did not join finalization")
			}
			assertShadowIntegrationIdle(t, g, 1, 0, time.Now())
		})
	}
}

func TestShadowIntegrationEpochPublicationHealthAndOldCompletion(t *testing.T) {
	for _, observeRetired := range []bool{false, true} {
		t.Run(fmt.Sprintf("observe-retired=%t", observeRetired), func(t *testing.T) {
			entered, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			t.Cleanup(unblock)
			g := newShadowIntegrationGateway(t, Options{Client: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				close(entered)
				<-release
				return routeTestResponse(200, routingSuccess), nil
			})}})
			c, req, target := shadowIntegrationScope(t, g, v1.Chat, `{"model":"m","messages":[]}`, &observerOutput{})
			done := make(chan struct{})
			go func() { defer close(done); g.doRelay(c, target, req, time.Second) }()
			select {
			case <-entered:
			case <-time.After(3 * time.Second):
				t.Fatal("attempt did not acquire host token")
			}
			for i := 0; i < 3; i++ {
				if err := g.Publish(shadowIntegrationChannels(2)); err != nil {
					t.Fatal(err)
				}
			}
			if g.appliedEpoch.Load() != 4 {
				t.Fatal("successful publications did not advance epoch", g.appliedEpoch.Load())
			}
			if observeRetired {
				if comparison := shadowIntegrationProbe(g, time.Now()); comparison.reason != "retired_active" {
					t.Fatal("old occupancy was treated as current zero load", comparison)
				}
			}
			unblock()
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				t.Fatal("old attempt did not finish")
			}
			assertShadowIntegrationIdle(t, g, 0, 0, time.Now())
			valid := shadowIntegrationChannels(2)
			bad := shadowIntegrationChannels(2)
			bad[0].BaseURL = "not-a-valid-target"
			token := g.shadow.Begin(4, 1, time.Now())
			g.shadow.End(&token, shadowCleanHTTP200, time.Now())
			if err := g.Publish(bad); err == nil || g.appliedEpoch.Load() != 4 {
				t.Fatal("failed publication advanced epoch", err, g.appliedEpoch.Load())
			}
			g.RefreshHealth(valid)
			g.observeHealth(&valid[0].Credentials[0], 200, "")
			for _, channel := range g.reg.allChannels() {
				if channel.epoch != 4 {
					t.Fatal("health refresh changed publication identity", channel.epoch)
				}
			}
			assertShadowIntegrationIdle(t, g, 1, 0, time.Now())
			g.appliedEpoch.Store(^uint64(0))
			if err := g.Publish(valid); err != nil || g.appliedEpoch.Load() != 0 {
				t.Fatal("epoch overflow failed real publication or reused an epoch", err, g.appliedEpoch.Load())
			}
			if comparison := shadowIntegrationProbe(g, time.Now()); comparison.reason != "saturated" {
				t.Fatal("epoch overflow did not disable shadow", comparison)
			}
			if err := g.Publish(valid); err != nil || g.appliedEpoch.Load() != 0 {
				t.Fatal("publication cleared overflow disablement", err, g.appliedEpoch.Load())
			}
		})
	}
}

func TestShadowIntegrationPublicationAndHealthDoNotNestSamplerLocks(t *testing.T) {
	var sends atomic.Int32
	g := newShadowIntegrationGateway(t, Options{Client: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		sends.Add(1)
		return routeTestResponse(200, routingSuccess), nil
	})}})
	ctx, cancel := context.WithTimeout(outputIdentity(), 8*time.Second)
	defer cancel()
	var group sync.WaitGroup
	for worker := 0; worker < 4; worker++ {
		group.Add(1)
		go func() {
			defer group.Done()
			for i := 0; i < 32; i++ {
				out := g.ExecuteTo(ctx, &observerOutput{}, Execution{Operation: v1.Chat, Body: []byte(`{"model":"m","messages":[]}`)})
				if out.StatusCode != 200 {
					t.Errorf("shadow publication changed execution: %+v", out)
					return
				}
			}
		}()
	}
	group.Add(2)
	go func() {
		defer group.Done()
		for i := 0; i < 64; i++ {
			if err := g.Publish(shadowIntegrationChannels(2)); err != nil {
				t.Error(err)
				return
			}
		}
	}()
	go func() {
		defer group.Done()
		for i := 0; i < 64; i++ {
			g.RefreshHealth(shadowIntegrationChannels(2))
		}
	}()
	done := make(chan struct{})
	go func() { group.Wait(); close(done) }()
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("publication/health/completion lock ordering deadlocked")
	}
	if sends.Load() != 128 {
		t.Fatal("shadow publication changed upstream send count", sends.Load())
	}
	comparison := shadowIntegrationProbe(g, time.Now())
	if !comparison.compared {
		t.Fatal("completed stress retained unknown activity", comparison)
	}
	for _, candidate := range comparison.candidates[:comparison.count] {
		if candidate.active != 0 {
			t.Fatal("stress leaked activity", candidate)
		}
	}
}

func TestShadowIntegrationDebugIsBoundedPrivateAndUsesFixedClock(t *testing.T) {
	var output bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&output, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(previous) })
	g := newShadowIntegrationGateway(t, Options{})
	fixed := time.Unix(1700000000, 0)
	selected, _, decision := g.chooseTarget(g.reg.allChannels(), nil, "private-session", fixed)
	c := newOutputScope(access.WithRequestID(outputIdentity(), "private-request"), &observerOutput{}, newRequestResult(outputIdentity()), "")
	c.source = invocationForeground
	for i := 0; i < 5; i++ {
		g.compareShadowRoute(c, decision, selected, fixed)
	}
	for i := 0; i < 4; i++ {
		token := g.shadow.Begin(g.appliedEpoch.Load(), 1, fixed)
		g.shadow.End(&token, shadowCleanHTTP200, fixed)
	}
	g.compareShadowRoute(c, decision, selected, fixed.Add(time.Second))
	lines := strings.Split(strings.TrimSpace(output.String()), "\n")
	if len(lines) != 2 {
		t.Fatal("debug comparison was not limited to one per second", output.String())
	}
	for _, line := range lines {
		var record map[string]json.RawMessage
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatal(err)
		}
		if string(record["msg"]) != `"shadow route comparison"` {
			t.Fatal("debug message changed", line)
		}
		var encoded string
		if err := json.Unmarshal(record["candidates"], &encoded); err != nil {
			t.Fatal("TextHandler-compatible candidate JSON string missing", line)
		}
		var candidates []shadowDebugCandidate
		if err := json.Unmarshal([]byte(encoded), &candidates); err != nil || len(candidates) != 2 {
			t.Fatal("bounded candidate details lost", encoded, err)
		}
		for _, name := range []string{"actual_channel_id", "recommended_channel_id", "pool_size", "epoch"} {
			var number uint64
			if err := json.Unmarshal(record[name], &number); err != nil {
				t.Fatal("debug numeric field changed", name, line)
			}
		}
		for _, secret := range []string{"private-session", "private-request", "private-key", "shadow.test", "credential_id", "model"} {
			if strings.Contains(line, secret) {
				t.Fatal("shadow log leaked private request/target facts", secret, line)
			}
		}
	}
	if !strings.Contains(lines[1], `\"live\":4`) {
		t.Fatal("debug did not expose known bounded history", lines[1])
	}
	oldEpoch := g.appliedEpoch.Load()
	token := g.shadow.Begin(oldEpoch, 1, fixed)
	if err := g.Publish(shadowIntegrationChannels(2)); err != nil {
		t.Fatal(err)
	}
	selected, _, decision = g.chooseTarget(g.reg.allChannels(), nil, "private-session", fixed.Add(2*time.Second))
	g.compareShadowRoute(c, decision, selected, fixed.Add(2*time.Second))
	lines = strings.Split(strings.TrimSpace(output.String()), "\n")
	if len(lines) != 3 {
		t.Fatal("retired comparison log missing", output.String())
	}
	var skipped struct {
		Reason     string
		Candidates string
		Compared   bool
	}
	if err := json.Unmarshal([]byte(lines[2]), &skipped); err != nil || skipped.Reason != "retired_active" || skipped.Compared || skipped.Candidates != "[]" {
		t.Fatal("unknown candidate activity was logged as zero", lines[2], err)
	}
	g.shadow.End(&token, shadowIgnored, fixed.Add(2*time.Second))
}

func TestShadowIntegrationRealStrictBillingAndWireParity(t *testing.T) {
	for _, failEnd := range []bool{false, true} {
		t.Run(fmt.Sprintf("failed-receipt=%t", failEnd), func(t *testing.T) {
			type balance struct {
				Owner      string
				Used, Held int64
			}
			type observation struct {
				Wire                                  string
				HTTP, Status                          int
				State, Charge, Original, ProviderHeld string
				Calls                                 []string
				Balances                              []balance
			}
			var baseline observation
			for _, enabled := range []bool{false, true} {
				fixture := newTestGateway(t, 0)
				channels, client := verifiedChatChannels(t, fixture.store.Channels(), fixture.upstream.URL)
				engine := billing.New(fixture.store.DB(), billing.NewPriceTableWithHistory(fixture.store.PriceData, time.Hour, fixture.store.RecordPriceSnapshot))
				if err := engine.EnableStrict("20", time.Minute); err != nil {
					t.Fatal(err)
				}
				configureVerifiedChatCosts(t, engine, channels)
				port := &strictBillingProbe{StrictBilling: engine, failEnd: failEnd}
				var sent [][]byte
				transport := client.Transport
				client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
					wire, err := io.ReadAll(r.Body)
					r.Body.Close()
					r.Body = io.NopCloser(bytes.NewReader(wire))
					if err != nil {
						return nil, err
					}
					sent = append(sent, wire)
					if !bytes.Equal(wire, port.wireBody) || r.Header.Get("Authorization") != port.wireHeader.Get("Authorization") {
						t.Error("shadow altered admitted upstream bytes or credential")
					}
					return transport.RoundTrip(r)
				})
				g, err := newRegisteredGateway(Options{Channels: channels, Client: client, StrictBilling: port, BillingMode: billing.ModeStrict, RouteDiagnostics: true, ShadowRouteScoring: enabled})
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(g.Close)
				token, err := fixture.store.GetToken(1)
				if err != nil {
					t.Fatal(err)
				}
				ctx := access.WithPrincipal(context.Background(), access.Principal{UserID: token.UserID, TokenID: token.ID, Policy: access.Policy{Mode: access.All}})
				recorder := httptest.NewRecorder()
				out := g.ExecuteTo(ctx, core.NewHTTPOutput(recorder), Execution{Operation: v1.Chat, Body: []byte(`{"model":"gpt-4o-mini","max_completion_tokens":32,"service_tier":"default","messages":[{"role":"user","content":"hello"}]}`)})
				if len(sent) != 1 {
					t.Fatal("shadow changed actual upstream send count", len(sent))
				}
				detail, err := engine.ReservationDetail(context.Background(), out.BillingID)
				if err != nil {
					t.Fatal(err)
				}
				var rows []model.BillingBalance
				if err := fixture.store.DB().Order("owner_type").Find(&rows).Error; err != nil {
					t.Fatal(err)
				}
				observed := observation{Wire: recorder.Body.String(), HTTP: recorder.Code, Status: out.StatusCode, State: detail.Reservation.State, Charge: detail.Reservation.ChargeUnits,
					Original: detail.Reservation.ReservedUnits, ProviderHeld: detail.Reservation.ProviderHeldUnits, Calls: port.calls}
				for _, row := range rows {
					observed.Balances = append(observed.Balances, balance{row.OwnerType, row.UsedUnits, row.ReservedUnits})
				}
				if !enabled {
					baseline = observed
				} else {
					if !reflect.DeepEqual(baseline, observed) {
						t.Fatal("shadow changed committed financial authority, send bytes or host output", baseline, observed)
					}
					want := 1
					if failEnd {
						want = 0
					}
					assertShadowIntegrationIdle(t, g, want, 0, time.Now())
				}
				if len(rows) != 2 {
					t.Fatal("real strict lifecycle lost dual balances", rows)
				}
				if failEnd {
					if out.StatusCode != 503 || observed.State != billing.Pending || observed.ProviderHeld == "0" {
						t.Fatal("local billing failure lost pending execution facts", observed)
					}
				} else if out.StatusCode != 200 || observed.Charge != "5850000" || observed.State != billing.Settled {
					t.Fatal("real settlement changed", observed)
				}
			}
		})
	}
}

func TestShadowIntegrationForegroundJSONAndSSEByteParity(t *testing.T) {
	for _, operation := range []v1.Operation{v1.Chat, v1.Responses} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%t", operation, stream), func(t *testing.T) {
				var baseline []byte
				var sentBaseline string
				for _, enabled := range []bool{false, true} {
					hits := 0
					var sent string
					body := routingSuccess
					requestBody := fmt.Sprintf(`{"model":"m","messages":[],"stream":%t}`, stream)
					if operation == v1.Responses {
						body = `{"id":"native-parity","object":"response","model":"m","status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`
						requestBody = fmt.Sprintf(`{"model":"m","input":"hello","stream":%t}`, stream)
					}
					if stream {
						if operation == v1.Chat {
							body = "data: " + `{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}` + "\n\ndata: " + body + "\n\ndata: [DONE]\n\n"
						} else {
							body = "data: " + `{"type":"response.completed","response":` + body + "}\n\n"
						}
					}
					g, err := newRegisteredGateway(Options{Channels: shadowIntegrationChannels(2), BillingMode: billing.ModeDisabled, RouteDiagnostics: true, ShadowRouteScoring: enabled,
						Client: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
							hits++
							wire, _ := io.ReadAll(r.Body)
							sent = string(wire)
							response := routeTestResponse(200, body)
							if stream {
								response.Header.Set("Content-Type", "text/event-stream")
							}
							return response, nil
						})}})
					if err != nil {
						t.Fatal(err)
					}
					t.Cleanup(g.Close)
					recorder := httptest.NewRecorder()
					out := g.ExecuteTo(outputIdentity(), core.NewHTTPOutput(recorder), Execution{Operation: operation, Body: []byte(requestBody)})
					if hits != 1 || out.StatusCode != 200 || !out.ProtocolComplete {
						t.Fatal("shadow changed real foreground execution", out, hits, recorder.Body.String())
					}
					if !enabled {
						baseline = bytes.Clone(recorder.Body.Bytes())
						sentBaseline = sent
					} else {
						if !bytes.Equal(baseline, recorder.Body.Bytes()) || sentBaseline != sent {
							t.Fatal("shadow changed upstream/downstream bytes", sentBaseline, sent, string(baseline), recorder.Body.String())
						}
						assertShadowIntegrationIdle(t, g, 1, 0, time.Now())
					}
				}
			})
		}
	}
}

type shadowPrepareRefusal struct{ factAdapter }

func (shadowPrepareRefusal) Prepare(v1.Request, v1.Target) (v1.Input, *v1.Error) {
	return v1.Input{}, &v1.Error{StatusCode: 400, Error: v1.ErrorDetail{Type: "prepare_refusal", Message: "unsupported fixture shape"}}
}

func TestShadowIntegrationPreflightHasNoTokenAndPinnedPoolNeverCompares(t *testing.T) {
	for _, kind := range []string{"prepare", "admission", "validation"} {
		t.Run(kind, func(t *testing.T) {
			hits := 0
			opts := Options{Client: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				hits++
				return routeTestResponse(200, routingSuccess), nil
			})}}
			if kind == "prepare" {
				opts.Protocols = []v1.ProtocolRegistration{{ID: "openai", Version: v1.Version, Adapter: shadowPrepareRefusal{}}}
			}
			if kind == "admission" {
				opts.Admission = admissionFunc(func(context.Context, AdmissionScope) (func(), error) {
					return nil, &AdmissionError{StatusCode: 429, Message: "bounded fixture rejection"}
				})
			}
			body := `{"model":"m","messages":[]}`
			if kind == "validation" {
				body = `{"model":"m",`
			}
			g := newShadowIntegrationGateway(t, opts)
			out := g.ExecuteTo(outputIdentity(), &observerOutput{}, Execution{Operation: v1.Chat, Body: []byte(body)})
			if hits != 0 || out.StatusCode < 400 {
				t.Fatal("preflight unexpectedly dispatched", kind, hits, out)
			}
			assertShadowIntegrationIdle(t, g, 0, 0, time.Now())
		})
	}
	g := newShadowIntegrationGateway(t, Options{})
	c := newOutputScope(outputIdentity(), &observerOutput{}, newRequestResult(outputIdentity()), "")
	c.source, c.responseTarget = invocationForeground, &ResponseTarget{}
	g.selectTargetScope(c, g.reg.allChannels(), nil, "", time.Now())
	var metrics strings.Builder
	if err := g.metrics.Render(&metrics); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(metrics.String(), `gateway_shadow_comparisons_total{reason="pinned"} 1`) {
		t.Fatal("pinned foreground comparison was not skipped", metrics.String())
	}
}
