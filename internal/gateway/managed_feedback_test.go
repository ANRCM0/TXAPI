package gateway

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"minigateway/internal/access"
	"minigateway/internal/billing"
	auth "minigateway/internal/foundation/contracts/upstreamauth/v1"
	authv2 "minigateway/internal/foundation/contracts/upstreamauth/v2"
	authv3 "minigateway/internal/foundation/contracts/upstreamauth/v3"
	"minigateway/internal/protocol"
	"minigateway/internal/protocol/openaiauth"
)

type managedEligibilityFixture struct {
	mu                   sync.Mutex
	epoch                int64
	blocked, recordFails bool
	issues               int
	writes               []authv3.FailureWrite
}

func (f *managedEligibilityFixture) AcquireAccess(_ context.Context, in authv2.AccessRequest) (authv2.AccessCredential, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.issues++
	if f.blocked {
		return authv2.AccessCredential{}, authv3.ErrBlocked
	}
	return authv2.AccessCredential{AccountID: in.AccountID, Revision: in.ExpectedRevision, CredentialRevision: in.ExpectedCredentialRevision, GateEpoch: f.epoch, AccessToken: auth.NewSecret("fixture-access-token"), ExpiresAt: time.Now().Add(time.Hour)}, nil
}
func (f *managedEligibilityFixture) RecordFailure(_ context.Context, write authv3.FailureWrite) (authv3.RecordResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.writes = append(f.writes, write)
	if f.recordFails {
		return authv3.RecordResult{}, errors.New("synthetic commit unavailable")
	}
	if write.GateEpoch != f.epoch {
		return authv3.RecordResult{}, nil
	}
	f.blocked = true
	return authv3.RecordResult{Applied: true}, nil
}
func (f *managedEligibilityFixture) resume() {
	f.mu.Lock()
	f.epoch++
	f.blocked = false
	f.mu.Unlock()
}

func feedbackGateway(t *testing.T, state *managedEligibilityFixture, directory *managedDirectoryFixture, send func(*http.Request) (*http.Response, error)) *Gateway {
	t.Helper()
	g, err := newRegisteredGateway(Options{BillingMode: billing.ModeDisabled, RetryTimes: 3, Channels: managedTestChannels(),
		ManagedInference: &ManagedInferenceOptions{Enabled: true, Credentials: state, DirectoryCaller: directory, ValidateModel: openaiauth.ValidateVisibleModel, FailureRecorder: state, ClassifyFailure: openaiauth.ClassifyInferenceFailure},
		Client:           &http.Client{Transport: roundTripFunc(send)}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(g.Close)
	return g
}
func feedbackRequest(g *Gateway) RequestResult {
	return g.ExecuteTo(outputIdentity(), &observerOutput{}, Execution{Operation: protocol.Responses, Body: []byte(managedTestBody)})
}
func feedbackHTTP(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}
}

func TestManagedHTTPFeedbackBlocksNextRequestAndResumeInvalidatesDirectory(t *testing.T) {
	for _, tc := range []struct {
		status int
		body   string
		reason authv3.Reason
		scope  authv3.Scope
	}{
		{401, `{"error":{"code":"private-provider-code","message":"private-refresh-token"}}`, authv3.AuthorizationReview, authv3.Grant},
		{403, `{"error":{"message":"private-refresh-token"}}`, authv3.AuthorizationReview, authv3.Grant},
		{429, `{"error":{"code":"subscription_sharing_usage_limit_exceeded"}}`, authv3.UsageLimited, authv3.Grant},
		{429, `{"error":{"code":"other","message":"private-refresh-token"}}`, authv3.RateLimitUnknown, authv3.Model},
	} {
		t.Run(string(tc.reason)+http.StatusText(tc.status), func(t *testing.T) {
			state, directory := &managedEligibilityFixture{epoch: 1}, &managedDirectoryFixture{}
			hits := 0
			g := feedbackGateway(t, state, directory, func(*http.Request) (*http.Response, error) {
				hits++
				if hits == 1 {
					return feedbackHTTP(tc.status, tc.body), nil
				}
				return feedbackHTTP(200, managedTestTerminal("completed")), nil
			})
			first := feedbackRequest(g)
			if first.Error == nil || len(state.writes) != 1 || hits != 1 {
				t.Fatalf("missing feedback: %+v writes=%d hits=%d", first, len(state.writes), hits)
			}
			write := state.writes[0]
			if write.GateEpoch != 1 || write.Signal.Reason != tc.reason || write.Signal.Scope != tc.scope || write.Signal.Source != authv3.ResponsesHTTP || write.Signal.HTTPStatus != tc.status || write.Issued.CredentialID != 90 || write.Issued.ExpectedBindingRevision != 2 || write.Issued.ExpectedRevision != 3 || write.Issued.ExpectedCredentialRevision != 4 || write.Issued.OwnerID != 1 || write.Issued.TokenID != 1 || write.Issued.Model != "m" {
				t.Fatalf("unbound feedback: %+v", write)
			}
			second := feedbackRequest(g)
			if second.Error == nil || hits != 1 || len(directory.requests) != 1 {
				t.Fatal("blocked grant sent a new request")
			}
			state.resume()
			third := feedbackRequest(g)
			if third.Error != nil || hits != 2 || len(directory.requests) != 2 {
				t.Fatalf("resume did not revalidate directory: %+v catalogs=%d hits=%d", third, len(directory.requests), hits)
			}
			if len(state.writes) != 1 || state.blocked {
				t.Fatal("success modified gate feedback")
			}
		})
	}
}

func TestManagedDirectoryFeedbackUsesIssuedEpochAndNeverSendsInference(t *testing.T) {
	for _, status := range []int{401, 403, 429} {
		state := &managedEligibilityFixture{epoch: 7}
		directory := &managedDirectoryFixture{call: func(context.Context, auth.Request) auth.Response {
			return auth.Response{Complete: true, Status: status, Body: auth.NewSecret(`{"error":{"code":"subscription_sharing_usage_limit_exceeded"}}`)}
		}}
		g := feedbackGateway(t, state, directory, func(*http.Request) (*http.Response, error) {
			t.Fatal("failed directory sent inference")
			return nil, nil
		})
		if result := feedbackRequest(g); result.Error == nil || len(state.writes) != 1 || state.writes[0].GateEpoch != 7 || state.writes[0].Signal.Source != authv3.ModelsHTTP {
			t.Fatalf("directory feedback missing: %+v", result)
		}
		if result := feedbackRequest(g); result.Error == nil || len(directory.requests) != 1 {
			t.Fatal("blocked directory reissued")
		}
	}
}

func TestManagedSSETerminalFeedbackSurvivesRichDecoderAndSanitization(t *testing.T) {
	state, directory := &managedEligibilityFixture{epoch: 3}, &managedDirectoryFixture{}
	hits := 0
	g := feedbackGateway(t, state, directory, func(*http.Request) (*http.Response, error) {
		hits++
		return feedbackHTTP(200, "event: response.failed\ndata: {\"type\":\"response.failed\",\"response\":{\"id\":\"native\",\"object\":\"response\",\"status\":\"failed\",\"output\":[],\"error\":{\"code\":\"subscription_sharing_usage_limit_exceeded\",\"message\":\"private-refresh-token\"}}}\n\n"), nil
	})
	output := &observerOutput{}
	result := g.ExecuteTo(outputIdentity(), output, Execution{Operation: protocol.Responses, Body: []byte(managedTestBody)})
	if result.Error == nil || len(state.writes) != 1 || state.writes[0].Signal.Source != authv3.ResponsesEvent || state.writes[0].Signal.Reason != authv3.UsageLimited || state.writes[0].GateEpoch != 3 {
		t.Fatalf("rich decoder lost failure: %+v writes=%+v", result, state.writes)
	}
	if strings.Contains(output.data.String(), "private-refresh-token") || strings.Contains(output.data.String(), "subscription_sharing_usage_limit_exceeded") {
		t.Fatal("eligibility evidence leaked into client event")
	}
	if result := feedbackRequest(g); result.Error == nil || hits != 1 {
		t.Fatal("SSE blocked grant resent")
	}
}

func TestManagedFeedbackCommitFailureRepairsBeforeAnyNewIssuance(t *testing.T) {
	state, directory := &managedEligibilityFixture{epoch: 1, recordFails: true}, &managedDirectoryFixture{}
	hits := 0
	g := feedbackGateway(t, state, directory, func(*http.Request) (*http.Response, error) {
		hits++
		return feedbackHTTP(403, `{"error":{"message":"private-refresh-token"}}`), nil
	})
	first := feedbackRequest(g)
	if first.StatusCode != 403 || first.Error == nil || hits != 1 || len(g.managed.pending) != 1 {
		t.Fatal("feedback write changed original failure or lost pending")
	}
	issues := state.issues
	if result := feedbackRequest(g); result.Error == nil || state.issues != issues || hits != 1 {
		t.Fatal("pending feedback allowed new credential issuance")
	}
	state.recordFails = false
	if result := feedbackRequest(g); result.Error == nil || len(g.managed.pending) != 0 || !state.blocked || hits != 1 {
		t.Fatal("pending repair did not block issued grant")
	}
	state.resume()
	// A late duplicate of the old epoch is a stale no-op and cannot punish
	// the manually resumed generation, even when credential revision is equal.
	g.managed.feedback(state.writes[0])
	if state.blocked {
		t.Fatal("late response blocked resumed epoch")
	}
}

func TestManaged503BackoffOnlyAffectsNextNewManagedRequest(t *testing.T) {
	state, directory := &managedEligibilityFixture{epoch: 1}, &managedDirectoryFixture{}
	hits := 0
	g := feedbackGateway(t, state, directory, func(r *http.Request) (*http.Response, error) {
		hits++
		if r.URL.Host == "static.test" {
			return feedbackHTTP(200, managedTestTerminal("completed")), nil
		}
		if hits == 1 {
			return feedbackHTTP(503, `{"error":{"message":"unavailable"}}`), nil
		}
		return feedbackHTTP(200, managedTestTerminal("completed")), nil
	})
	if result := feedbackRequest(g); result.StatusCode != 503 || hits != 1 || len(state.writes) != 0 {
		t.Fatal("503 retried or persisted a gate")
	}
	until := g.managed.backoffUntil
	if remaining := time.Until(until); remaining < 4*time.Second || remaining > 5*time.Second {
		t.Fatal("503 backoff is not bounded to five seconds")
	}
	if result := feedbackRequest(g); result.Error == nil || hits != 1 {
		t.Fatal("backoff request dispatched")
	}
	other := access.WithPrincipal(context.Background(), access.Principal{UserID: 2, TokenID: 2, Policy: access.Policy{Mode: access.All}})
	if result := g.ExecuteTo(other, &observerOutput{}, Execution{Operation: protocol.Responses, Body: []byte(managedTestBody)}); result.Error != nil || hits != 2 {
		t.Fatal("managed backoff affected static route")
	}
	g.managed.mu.Lock()
	g.managed.backoffUntil = time.Now().Add(-time.Second)
	g.managed.mu.Unlock()
	if result := feedbackRequest(g); result.Error != nil || hits != 3 || len(state.writes) != 0 {
		t.Fatal("next new request after backoff did not use normal admission")
	}
}

func TestManagedNonEligibilityFailuresNeverPunishCredentials(t *testing.T) {
	for _, mode := range []string{"parameter", "truncated event", "unknown event code", "cancel", "client write"} {
		t.Run(mode, func(t *testing.T) {
			state, directory := &managedEligibilityFixture{epoch: 1}, &managedDirectoryFixture{}
			g := feedbackGateway(t, state, directory, func(*http.Request) (*http.Response, error) {
				if mode == "parameter" {
					return feedbackHTTP(400, `{"error":{"code":"subscription_sharing_usage_limit_exceeded"}}`), nil
				}
				if mode == "cancel" {
					return nil, context.Canceled
				}
				if mode == "truncated event" {
					return feedbackHTTP(200, "event: error\ndata: {\"type\":\"error\",\"code\":\"subscription_sharing_usage_limit_exceeded\"}\n"), nil
				}
				if mode == "client write" {
					return feedbackHTTP(200, "event: error\ndata: {\"type\":\"error\",\"code\":\"subscription_sharing_usage_limit_exceeded\"}\n\n"), nil
				}
				return feedbackHTTP(200, "event: error\ndata: {\"type\":\"error\",\"code\":\"other\",\"message\":\"subscription_sharing_usage_limit_exceeded\"}\n\n"), nil
			})
			if mode == "client write" {
				g.ExecuteTo(outputIdentity(), &observerOutput{failure: "flush"}, Execution{Operation: protocol.Responses, Body: []byte(managedTestBody)})
			} else {
				feedbackRequest(g)
			}
			if len(state.writes) != 0 || state.blocked {
				t.Fatal("local/protocol/parameter failure punished credentials")
			}
		})
	}
}

type feedbackReadProbe struct {
	reader        io.Reader
	reads, closes int
}

func (b *feedbackReadProbe) Read(p []byte) (int, error) { b.reads++; return b.reader.Read(p) }
func (b *feedbackReadProbe) Close() error               { b.closes++; return nil }

func TestManagedErrorObservationNeverPreReadsAndBoundsCapture(t *testing.T) {
	for _, size := range []int{17, 64 << 10, 64<<10 + 1} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			body := bytes.Repeat([]byte("x"), size)
			probe := &feedbackReadProbe{reader: bytes.NewReader(body)}
			observation := &managedWireObservation{}
			transport := managedTransport{base: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: 429, Header: http.Header{}, Body: probe}, nil
			})}
			ctx := context.WithValue(context.WithValue(context.Background(), managedWireKey{}, true), managedObservationKey{}, observation)
			r, _ := http.NewRequestWithContext(ctx, http.MethodPost, managedOrigin+"/responses", nil)
			response, err := transport.RoundTrip(r)
			if err != nil || probe.reads != 0 {
				t.Fatal("transport changed first-byte timing")
			}
			first := make([]byte, 1)
			if n, err := response.Body.Read(first); n != 1 || err != nil || probe.reads != 1 || observation.complete {
				t.Fatal("body observer read ahead")
			}
			rest, err := io.ReadAll(response.Body)
			if err != nil || !bytes.Equal(append(first, rest...), body) {
				t.Fatal("body observer changed downstream bytes")
			}
			if size <= 64<<10 {
				if !observation.complete || observation.overflow || !bytes.Equal(observation.body, body) {
					t.Fatal("bounded complete body not observed")
				}
			} else if observation.complete || !observation.overflow || len(observation.body) != 0 {
				t.Fatal("oversize body retained or classified as complete")
			}
			if err := response.Body.Close(); err != nil || probe.closes != 1 {
				t.Fatal("body close ownership changed")
			}
		})
	}
	probe := &feedbackReadProbe{reader: strings.NewReader("unchanged")}
	upstream := &http.Response{StatusCode: 403, Header: http.Header{}, Body: probe}
	transport := managedTransport{base: roundTripFunc(func(*http.Request) (*http.Response, error) { return upstream, nil })}
	r, _ := http.NewRequest(http.MethodPost, "https://static.test", nil)
	response, err := transport.RoundTrip(r)
	if err != nil || response != upstream || response.Body != probe || probe.reads != 0 {
		t.Fatal("static response body was wrapped")
	}
}

func TestManagedPendingEvidenceIsBoundedDeduplicatedAndFailsClosedAtCapacity(t *testing.T) {
	state := &managedEligibilityFixture{epoch: 1, recordFails: true}
	m, err := newManagedInference(&ManagedInferenceOptions{Enabled: true, Credentials: state, DirectoryCaller: &managedDirectoryFixture{}, ValidateModel: openaiauth.ValidateVisibleModel, FailureRecorder: state, ClassifyFailure: openaiauth.ClassifyInferenceFailure}, billing.ModeDisabled)
	if err != nil {
		t.Fatal(err)
	}
	write := authv3.FailureWrite{GateEpoch: 1, Issued: authv2.AccessRequest{CredentialID: 90, AccountID: "account", ExpectedBindingRevision: 2, ExpectedRevision: 3, ExpectedCredentialRevision: 4, OwnerID: 1, TokenID: 1, Model: "m"}, Signal: authv3.FailureSignal{Reason: authv3.RateLimitUnknown, Scope: authv3.Model, HTTPStatus: 429, Source: authv3.ResponsesHTTP}}
	for range 100 {
		m.feedback(write)
	}
	if len(m.pending) != 1 {
		t.Fatal("duplicate feedback grew pending metadata")
	}
	write.Signal = authv3.FailureSignal{Reason: authv3.AuthorizationReview, Scope: authv3.Grant, HTTPStatus: 403, Source: authv3.ResponsesHTTP}
	m.feedback(write)
	if len(m.pending) != 1 || m.pending[0].Signal.Reason != authv3.AuthorizationReview {
		t.Fatal("pending classification lost wider fixed scope")
	}
	for epoch := int64(2); epoch <= managedPendingLimit+1; epoch++ {
		write.GateEpoch = epoch
		m.feedback(write)
	}
	if len(m.pending) != managedPendingLimit || !m.feedbackOverflow {
		t.Fatal("pending feedback did not fail closed at fixed capacity")
	}
	state.recordFails = false
	if _, err := m.issue(context.Background(), write.Issued); !errors.Is(err, authv3.ErrBlocked) || state.issues != 0 {
		t.Fatal("overflow lost evidence then reopened issuance")
	}
}

func TestManagedLateConcurrentSuccessCannotClearFailureGate(t *testing.T) {
	state, directory := &managedEligibilityFixture{epoch: 1}, &managedDirectoryFixture{}
	entered, release := make(chan struct{}), make(chan struct{})
	var mu sync.Mutex
	hits := 0
	g := feedbackGateway(t, state, directory, func(*http.Request) (*http.Response, error) {
		mu.Lock()
		hits++
		number := hits
		mu.Unlock()
		if number == 1 {
			close(entered)
			<-release
			return feedbackHTTP(200, managedTestTerminal("completed")), nil
		}
		return feedbackHTTP(429, `{"error":{"code":"subscription_sharing_usage_limit_exceeded"}}`), nil
	})
	first := make(chan RequestResult, 1)
	go func() { first <- feedbackRequest(g) }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("first managed request never sent")
	}
	second := feedbackRequest(g)
	if second.Error == nil {
		close(release)
		t.Fatal("second request unexpectedly completed")
	}
	close(release)
	select {
	case result := <-first:
		if result.Error != nil {
			t.Fatal("authorized in-flight success changed")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("in-flight success did not complete")
	}
	state.mu.Lock()
	blocked, writes := state.blocked, len(state.writes)
	state.mu.Unlock()
	if !blocked || writes != 1 {
		t.Fatal("late success cleared recorded gate")
	}
	third := feedbackRequest(g)
	mu.Lock()
	sent := hits
	mu.Unlock()
	if third.Error == nil || sent != 2 {
		t.Fatal("success reopened a blocked grant")
	}
}

type blockingFeedbackRecorder struct {
	state            *managedEligibilityFixture
	entered, release chan struct{}
}

func (r *blockingFeedbackRecorder) RecordFailure(ctx context.Context, write authv3.FailureWrite) (authv3.RecordResult, error) {
	close(r.entered)
	select {
	case <-r.release:
		return r.state.RecordFailure(ctx, write)
	case <-ctx.Done():
		return authv3.RecordResult{}, ctx.Err()
	}
}

func TestManagedCanceledIssuanceDoesNotWaitForFeedbackDatabase(t *testing.T) {
	state, directory := &managedEligibilityFixture{epoch: 1}, &managedDirectoryFixture{}
	g := feedbackGateway(t, state, directory, func(*http.Request) (*http.Response, error) {
		t.Fatal("canceled issuance sent inference")
		return nil, nil
	})
	recorder := &blockingFeedbackRecorder{state: state, entered: make(chan struct{}), release: make(chan struct{})}
	g.managed.recorder = recorder
	write := issuedManaged(&managedTestChannels()[1].Credentials[0], authv2.AccessCredential{Revision: 3, CredentialRevision: 4, GateEpoch: 1})
	write.Signal = authv3.FailureSignal{Reason: authv3.AuthorizationReview, Scope: authv3.Grant, HTTPStatus: 403, Source: authv3.ResponsesHTTP}
	feedbackDone := make(chan struct{})
	go func() { g.managed.feedback(write); close(feedbackDone) }()
	select {
	case <-recorder.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("recorder never entered")
	}
	ctx, cancel := context.WithCancel(context.Background())
	issueDone := make(chan error, 1)
	go func() { _, err := g.managed.issue(ctx, write.Issued); issueDone <- err }()
	cancel()
	select {
	case err := <-issueDone:
		if err == nil {
			t.Fatal("canceled issuance succeeded")
		}
	case <-time.After(250 * time.Millisecond):
		close(recorder.release)
		<-feedbackDone
		t.Fatal("canceled issuance waited for feedback database")
	}
	if state.issues != 0 {
		t.Fatal("canceled waiter acquired credential")
	}
	close(recorder.release)
	select {
	case <-feedbackDone:
	case <-time.After(5 * time.Second):
		t.Fatal("feedback failed to join")
	}
	state.resume()
	credential, err := g.managed.issue(context.Background(), write.Issued)
	if err != nil || credential.GateEpoch != 2 || len(g.managed.pending) != 0 {
		t.Fatal("serial gate did not recover after canceled waiter", err)
	}
}
