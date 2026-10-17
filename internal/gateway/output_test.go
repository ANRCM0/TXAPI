package gateway

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"minigateway/internal/access"
	"minigateway/internal/billing"
	"minigateway/internal/core"
	dbmodel "minigateway/internal/model"
	"minigateway/internal/protocol"
)

// A bounded, non-HTTP observer. It consumes the real coordinator and HTTP
// executor; it is not a worker, durable event store or WS implementation.
type observerOutput struct {
	metadata core.ResponseMetadata
	state    core.OutputState
	data     bytes.Buffer
	commits  int
	writes   int
	flushes  int
	failure  string
}

func (o *observerOutput) Commit(_ context.Context, metadata core.ResponseMetadata) error {
	o.commits++
	o.metadata = metadata
	if o.failure == "commit_before" {
		return io.ErrClosedPipe
	}
	o.state.Committed = true
	if o.failure == "commit_after" {
		return io.ErrClosedPipe
	}
	return nil
}
func (o *observerOutput) Write(ctx context.Context, data []byte) (int, error) {
	o.writes++
	if o.failure == "short_write" {
		return 1, nil
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if o.data.Len()+len(data) > 4096 {
		return 0, io.ErrShortBuffer
	}
	return o.data.Write(data)
}
func (o *observerOutput) Flush(context.Context) error {
	o.flushes++
	if o.failure == "flush" {
		return io.ErrClosedPipe
	}
	return nil
}
func (o *observerOutput) State() core.OutputState { return o.state }

func outputIdentity() context.Context {
	return access.WithPrincipal(context.Background(), access.Principal{UserID: 1, TokenID: 1, Policy: access.Policy{Mode: access.All}})
}

func TestCoordinatorIndependentOutputSharesOperationLifecycle(t *testing.T) {
	for _, operation := range []protocol.Operation{protocol.Chat, protocol.Embeddings} {
		t.Run(string(operation), func(t *testing.T) {
			var hits, requests, attempts, releases atomic.Int64
			g, err := New(Options{Channels: lifecycleChannels("http://upstream.test"), Admission: admissionFunc(func(_ context.Context, scope AdmissionScope) (func(), error) {
				if scope.Level == RequestAdmission {
					requests.Add(1)
				} else {
					attempts.Add(1)
				}
				return func() { releases.Add(1) }, nil
			}), Client: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				hits.Add(1)
				body := `{"choices":[],"usage":{"prompt_tokens":2,"completion_tokens":1,"total_tokens":3}}`
				if operation == protocol.Embeddings {
					if !strings.HasSuffix(r.URL.Path, "/embeddings") {
						t.Error("operation was coerced into Chat")
					}
					body = `{"data":[{"embedding":[0.1],"index":0}],"usage":{"prompt_tokens":2,"total_tokens":2}}`
				}
				return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
			})}})
			if err != nil {
				t.Fatal(err)
			}
			defer g.Close()
			output := &observerOutput{}
			result := g.ExecuteTo(outputIdentity(), output, Execution{Operation: operation, Body: []byte(`{"model":"m","input":"hello"}`)})
			if result.StatusCode != 200 || !result.Committed || result.HTTPStatus != 0 || !result.ProtocolComplete || len(result.Attempts) != 1 || hits.Load() != 1 {
				t.Fatalf("execution: %+v hits=%d", result, hits.Load())
			}
			if requests.Load() != 1 || attempts.Load() != 1 || releases.Load() != 2 || output.commits != 1 || output.metadata.ContentType != "application/json" || output.data.Len() == 0 {
				t.Fatalf("lifecycle or output diverged: requests=%d attempts=%d releases=%d output=%+v", requests.Load(), attempts.Load(), releases.Load(), output)
			}
		})
	}
}

func TestCoordinatorOutputFailureNeverRetriesOrPenalizesCredential(t *testing.T) {
	for _, failure := range []string{"commit_before", "commit_after", "short_write", "flush"} {
		t.Run(failure, func(t *testing.T) {
			var hits, leases, health atomic.Int64
			channels := lifecycleChannels("http://upstream.test")
			channels[0].Credentials = append(channels[0].Credentials, dbmodel.Credential{ID: 9, APIKey: "second-test-key", Weight: 1, Status: dbmodel.CredentialActive})
			g, err := New(Options{Channels: channels, RetryTimes: 1, Admission: admissionFunc(func(context.Context, AdmissionScope) (func(), error) {
				leases.Add(1)
				return func() { leases.Add(-1) }, nil
			}), HealthReporter: func(*dbmodel.Credential, int, string) { health.Add(1) }, Client: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				hits.Add(1)
				return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("data: {\"choices\":[{\"delta\":{\"content\":\"one\"},\"finish_reason\":null}]}\n\ndata: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"))}, nil
			})}})
			if err != nil {
				t.Fatal(err)
			}
			defer g.Close()
			output := &observerOutput{failure: failure}
			result := g.ExecuteTo(outputIdentity(), output, Execution{Operation: protocol.Chat, Body: []byte(`{"model":"m","stream":true}`)})
			if result.Termination != core.ClientWriteError || hits.Load() != 1 || len(result.Attempts) != 1 || leases.Load() != 0 || health.Load() != 0 || output.commits != 1 || strings.Contains(output.data.String(), "[DONE]") {
				t.Fatalf("output failure retried, leaked or fabricated success: result=%+v output=%+v hits=%d leases=%d health=%d", result, output, hits.Load(), leases.Load(), health.Load())
			}
			if result.Committed != (failure != "commit_before") || result.HTTPStatus != 0 {
				t.Fatalf("commit facts lost: %+v", result)
			}
		})
	}
}

func TestCoordinatorIndependentOutputRejectsBeforeExecution(t *testing.T) {
	var hits atomic.Int64
	g, err := New(Options{Channels: lifecycleChannels("http://upstream.test"), Client: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		hits.Add(1)
		panic("local refusal reached network")
	})}})
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	in := Execution{Operation: protocol.Chat, Body: []byte(`{"model":"m"}`)}
	for _, name := range []string{"missing_identity", "unauthorized_model", "unsupported_operation", "nil_output", "committed_output"} {
		t.Run(name, func(t *testing.T) {
			ctx, output, request, want := outputIdentity(), &observerOutput{}, in, 400
			switch name {
			case "missing_identity":
				ctx, want = context.Background(), 401
			case "unauthorized_model":
				ctx = access.WithPrincipal(context.Background(), access.Principal{UserID: 1, TokenID: 1, Policy: access.Policy{Mode: access.Allowlist}})
				want = 403
			case "unsupported_operation":
				request.Operation = protocol.Responses
			case "nil_output":
				output, want = nil, 500
			case "committed_output":
				output.state.Committed, want = true, 500
			}
			var destination core.Output
			if output != nil {
				destination = output
			}
			result := g.ExecuteTo(ctx, destination, request)
			if result.StatusCode != want || result.Phase != core.NotDispatched || hits.Load() != 0 || len(result.Attempts) != 0 {
				t.Fatalf("refusal: %+v", result)
			}
			if output != nil && name != "committed_output" && (!result.Committed || !strings.Contains(output.data.String(), `"error"`)) {
				t.Fatalf("refusal was not delivered: %+v %+v", result, output)
			}
		})
	}
}

func TestCoordinatorIndependentOutputCarriesRetryMetadata(t *testing.T) {
	g, err := New(Options{Admission: admissionFunc(func(context.Context, AdmissionScope) (func(), error) {
		return nil, &AdmissionError{StatusCode: 429, RetryAfterSeconds: 7, Message: "capacity reached"}
	})})
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	output := &observerOutput{}
	result := g.ExecuteTo(outputIdentity(), output, Execution{Operation: protocol.Chat, Body: []byte(`{"model":"m"}`)})
	if result.StatusCode != 429 || output.metadata.Values["Retry-After"] != "7" || !result.Committed || result.HTTPStatus != 0 {
		t.Fatalf("retry metadata lost: %+v %+v", result, output.metadata)
	}
}

func TestCoordinatorIndependentOutputRetriesOnlyBeforeCommit(t *testing.T) {
	channels := lifecycleChannels("http://upstream.test")
	channels[0].Credentials = append(channels[0].Credentials, dbmodel.Credential{ID: 9, APIKey: "second-test-key", Weight: 1, Status: dbmodel.CredentialActive})
	var hits atomic.Int64
	g, err := New(Options{Channels: channels, RetryTimes: 1, Client: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		if hits.Add(1) == 1 {
			return &http.Response{StatusCode: 503, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"error":{"message":"first attempt failed"}}`))}, nil
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"choices":[],"usage":{"prompt_tokens":2,"completion_tokens":1,"total_tokens":3}}`))}, nil
	})}})
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	output := &observerOutput{}
	result := g.ExecuteTo(outputIdentity(), output, Execution{Operation: protocol.Chat, Body: []byte(`{"model":"m"}`)})
	if result.StatusCode != 200 || !result.Committed || len(result.Attempts) != 2 || hits.Load() != 2 || output.commits != 1 || strings.Contains(output.data.String(), "failed") {
		t.Fatalf("uncommitted retry leaked output: result=%+v output=%+v hits=%d", result, output, hits.Load())
	}
}

func TestCoordinatorIndependentOutputPreservesStrictFinancialTruth(t *testing.T) {
	for _, name := range []string{"success", "commit_before", "short_write", "unproved_input"} {
		t.Run(name, func(t *testing.T) {
			fixture := newTestGateway(t, 0)
			st := fixture.store
			channels, client := verifiedChatChannels(t, st.Channels(), fixture.upstream.URL)
			engine := billing.New(st.DB(), billing.NewPriceTableWithHistory(st.PriceData, time.Hour, st.RecordPriceSnapshot))
			if err := engine.EnableStrict("20", time.Minute); err != nil {
				t.Fatal(err)
			}
			contract := verifiedChatContract(channels[0])
			if name == "unproved_input" {
				contract.InputBoundProfile = ""
				contract.TariffProfile, contract.Currency, contract.CredentialBindings = "", "", nil
			}
			if err := engine.ConfigureProviderCosts("1", []billing.ProviderCostContract{contract}); err != nil {
				t.Fatal(err)
			}
			g, err := New(Options{Channels: channels, Client: client, Billing: engine, BillingMode: billing.ModeStrict})
			if err != nil {
				t.Fatal(err)
			}
			defer g.Close()
			token, err := st.GetToken(1)
			if err != nil {
				t.Fatal(err)
			}
			ctx := access.WithPrincipal(context.Background(), access.Principal{UserID: token.UserID, TokenID: token.ID, Policy: access.Policy{Mode: access.All}})
			output := &observerOutput{failure: name}
			result := g.ExecuteTo(ctx, output, Execution{Operation: protocol.Chat, Body: []byte(`{"model":"gpt-4o-mini","max_completion_tokens":32,"service_tier":"default","messages":[{"role":"user","content":"hello"}]}`)})
			id := output.metadata.Values["X-TXAPI-Billing-ID"]
			if id == "" || output.metadata.Values["X-TXAPI-Price-Version"] == "" || result.HTTPStatus != 0 {
				t.Fatalf("financial metadata lost: %+v %+v", result, output.metadata)
			}
			detail, err := engine.ReservationDetail(context.Background(), id)
			if err != nil {
				t.Fatal(err)
			}
			var balances []dbmodel.BillingBalance
			if err := st.DB().Find(&balances).Error; err != nil || len(balances) != 2 {
				t.Fatalf("missing dual balances: %+v %v", balances, err)
			}
			wantHits := int64(1)
			switch name {
			case "success":
				if result.StatusCode != 200 || result.BillingState != billing.Settled || detail.Reservation.ChargeUnits == "0" || len(detail.Attempts) != 1 {
					t.Fatalf("success not settled: %+v %+v", result, detail)
				}
				for _, balance := range balances {
					if balance.ReservedUnits != 0 || balance.UsedUnits <= 0 {
						t.Fatalf("settlement lost: %+v", balance)
					}
				}
			case "unproved_input":
				wantHits = 0
				if result.StatusCode != 503 || detail.Reservation.Reason != "provider_input_unproven" || detail.Reservation.State != billing.Settled || len(detail.Attempts) != 0 {
					t.Fatalf("strict output bypassed admission: %+v %+v", result, detail)
				}
				for _, balance := range balances {
					if balance.ReservedUnits != 0 || balance.UsedUnits != 0 {
						t.Fatalf("unexecuted refusal charged: %+v", balance)
					}
				}
			default:
				if result.Termination != core.ClientWriteError || result.BillingState != billing.Pending || detail.Reservation.State != billing.Pending || len(detail.Attempts) != 1 {
					t.Fatalf("output failure was made free: %+v %+v", result, detail)
				}
				for _, balance := range balances {
					if balance.ReservedUnits <= 0 || balance.UsedUnits != 0 {
						t.Fatalf("unknown execution hold lost: %+v", balance)
					}
				}
			}
			if atomic.LoadInt64(fixture.upstreamHit) != wantHits {
				t.Fatalf("unexpected upstream executions: %d want %d", atomic.LoadInt64(fixture.upstreamHit), wantHits)
			}
		})
	}
}
