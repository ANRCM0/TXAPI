package facts_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"minigateway/internal/access"
	"minigateway/internal/billing"
	"minigateway/internal/compat/httpoutput"
	"minigateway/internal/config"
	"minigateway/internal/examples/facts"
	v1 "minigateway/internal/foundation/contracts/execution/v1"
	v2 "minigateway/internal/foundation/contracts/execution/v2"
	v3 "minigateway/internal/foundation/contracts/execution/v3"
	identity "minigateway/internal/foundation/contracts/identity/v1"
	q "minigateway/internal/foundation/contracts/quotation/v1"
	identityimpl "minigateway/internal/foundation/identity"
	"minigateway/internal/gateway"
	"minigateway/internal/model"
	"minigateway/internal/protocol"
	"minigateway/internal/store"
)

func exampleQuotationConfig(newVersion bool) billing.QuotationConfig {
	price := billing.QuotationPrice{Model: "provider-model", UncachedInputUnitsPer1K: "1000", CachedInputUnitsPer1K: "500", CacheWriteInputUnitsPer1K: "2000", OutputUnitsPer1K: "3000"}
	if newVersion {
		price.UncachedInputUnitsPer1K = "10000"
	}
	return billing.QuotationConfig{Currency: "TEST", Prices: []billing.QuotationPrice{price}, GroupRateUnits: map[string]string{"actual-sale": "2000000000"}}
}

type exampleFinancialState struct {
	Balances            []model.BillingBalance
	Reservations        []model.BillingReservation
	UserUsed, TokenUsed float64
}

func readExampleFinancialState(t *testing.T, st *store.Store, userID, tokenID uint) exampleFinancialState {
	t.Helper()
	var out exampleFinancialState
	if err := st.DB().Order("owner_type, owner_id").Find(&out.Balances).Error; err != nil {
		t.Fatal(err)
	}
	if err := st.DB().Order("id").Find(&out.Reservations).Error; err != nil {
		t.Fatal(err)
	}
	if err := st.DB().Model(&model.User{}).Where("id = ?", userID).Select("used_quota").Scan(&out.UserUsed).Error; err != nil {
		t.Fatal(err)
	}
	if err := st.DB().Model(&model.Token{}).Where("id = ?", tokenID).Select("used_quota").Scan(&out.TokenUsed).Error; err != nil {
		t.Fatal(err)
	}
	return out
}

func TestIndependentQuotationConsumerPinsHistoryDuringRealJSONSSE(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(map[bool]string{false: "json", true: "sse"}[stream], func(t *testing.T) {
			database := config.DatabaseConfig{Driver: "sqlite", Path: filepath.Join(t.TempDir(), "quotation.db")}
			st, err := store.Open(database)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { st.Close() })
			book := billing.NewQuotationBook(st.RecordPriceSnapshot, st.QuotationHistory)
			if err := book.Replace(exampleQuotationConfig(false)); err != nil {
				t.Fatal(err)
			}
			pinned, err := book.Current(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			var hits atomic.Int64
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hits.Add(1)
				var request struct {
					Model  string
					Stream bool
				}
				if json.NewDecoder(r.Body).Decode(&request) != nil || request.Model != "provider-model" || request.Stream != stream || r.Header.Get("Authorization") != "Bearer fixture-upstream" {
					t.Error("request did not use mapped model and prepared credential")
				}
				// Publish only once the actual generation has reached the upstream.
				if err := book.Replace(exampleQuotationConfig(true)); err != nil {
					t.Error(err)
					w.WriteHeader(500)
					return
				}
				document := `{"id":"native-quotation","object":"response","status":"completed","output":[],"usage":{"input_tokens":10,"output_tokens":4,"total_tokens":14,"input_tokens_details":{"cached_tokens":2,"cache_write_tokens":1},"output_tokens_details":{"reasoning_tokens":3}}}`
				if stream {
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = io.WriteString(w, "event: response.completed\ndata: "+`{"type":"response.completed","response":`+document+"}\n\n")
				} else {
					w.Header().Set("Content-Type", "application/json")
					_, _ = io.WriteString(w, document)
				}
			}))
			defer upstream.Close()
			user, err := st.CreateUser(&store.UserInput{Username: "quotation-user", Password: "fixture-password-123", ModelMode: access.Allowlist, Models: []string{"client-alias"}})
			if err != nil {
				t.Fatal(err)
			}
			key, err := st.IssueToken(user.ID, &store.TokenInput{Name: "quotation-key", Models: []string{"client-alias"}})
			if err != nil {
				t.Fatal(err)
			}
			_, err = st.CreateChannel(&store.ChannelInput{Name: "quotation-upstream", Type: "openai", BaseURL: upstream.URL, Models: []string{"client-alias"}, ModelMapping: map[string]string{"client-alias": "provider-model"}, GroupName: "actual-sale", Weight: 1, APIKey: "fixture-upstream"})
			if err != nil {
				t.Fatal(err)
			}
			balances := []model.BillingBalance{{OwnerType: "user", OwnerID: user.ID, UsedUnits: 37, ReservedUnits: 11}, {OwnerType: "token", OwnerID: key.ID, UsedUnits: 37, ReservedUnits: 11}}
			if err := st.DB().Create(&balances).Error; err != nil {
				t.Fatal(err)
			}
			reservation := model.BillingReservation{ID: "existing-unrelated-hold", UserID: user.ID, TokenID: key.ID, OriginModel: "client-alias", PriceVersion: "unrelated-legacy-price", ReservedUnits: 11, State: "reserved", LeaseUntil: time.Now().Add(time.Hour).UnixMilli()}
			if err := st.DB().Create(&reservation).Error; err != nil {
				t.Fatal(err)
			}
			if err := st.DB().Model(&model.User{}).Where("id = ?", user.ID).Update("used_quota", 1.25).Error; err != nil {
				t.Fatal(err)
			}
			if err := st.DB().Model(&model.Token{}).Where("id = ?", key.ID).Update("used_quota", 2.5).Error; err != nil {
				t.Fatal(err)
			}
			before := readExampleFinancialState(t, st, user.ID, key.ID)
			channels, err := st.ChannelSnapshot()
			if err != nil {
				t.Fatal(err)
			}
			g, err := gateway.New(gateway.Options{Channels: channels, BillingMode: billing.ModeDisabled, Operations: protocol.Operations(), Protocols: protocol.Protocols(nil), TotalTimeout: 3 * time.Second})
			if err != nil {
				t.Fatal(err)
			}
			defer g.Close()
			ctx, _, ok := identityimpl.NewTokenAuthenticator(st).Authenticate(context.Background(), key.Key)
			if !ok {
				t.Fatal("real token authentication failed")
			}
			service := facts.QuotationService{Authority: identityimpl.New(st.ResolvePrincipal), Executor: gateway.NewUsageFactsExecutor(g), Quotes: book}
			body, _ := json.Marshal(map[string]any{"model": "client-alias", "input": "quote-this", "stream": stream, "metadata": map[string]string{"group": "client-group"}})
			recorder := httptest.NewRecorder()
			out, err := service.Run(ctx, httpoutput.New(recorder), body)
			if err != nil || hits.Load() != 1 || out.Execution.Execution.Legacy.StatusCode != 200 || strings.Count(recorder.Body.String(), `"object":"response"`) != 1 {
				t.Fatalf("generation was not delivered once: hits=%d out=%+v err=%v wire=%s", hits.Load(), out, err, recorder.Body.String())
			}
			if len(out.Execution.Execution.Attempts) != 1 || !out.Execution.Execution.Attempts[0].ProtocolComplete || len(out.Execution.Usage) != 1 {
				t.Fatal("missing actual attempt facts", out)
			}
			actual := out.Execution.Execution.Legacy
			if len(actual.Attempts) != 1 || actual.Attempts[0].Model != "provider-model" || actual.Attempts[0].Model == "client-alias" || actual.ChannelGroup != "actual-sale" || actual.ChannelGroup == "client-group" {
				t.Fatal("quote binding was derived from client aliases", actual)
			}
			quote := out.Quote
			if !quote.Known || quote.PriceVersion != pinned.Version || quote.Model != actual.Attempts[0].Model || quote.Group != actual.ChannelGroup || quote.AmountUnits != "44" || quote.Reason != "quoted" {
				t.Fatal("executed observation did not retain pinned selling version", quote)
			}
			current, err := book.Current(ctx)
			if err != nil || current.Version == pinned.Version {
				t.Fatal("upstream did not publish the competing version", current, err)
			}
			usage := out.Execution.Usage[0].Usage
			if usage.Breakdown.ReasoningOutputTokens == nil || *usage.Breakdown.ReasoningOutputTokens != 3 {
				t.Fatal("native breakdown was lost")
			}
			newQuote, err := book.Calculate(ctx, q.Input{PriceVersion: current.Version, Model: quote.Model, Group: quote.Group, Usage: usage})
			if err != nil || !newQuote.Known || newQuote.AmountUnits != "170" {
				t.Fatal("fixture failed to distinguish newer prices", newQuote, err)
			}
			if after := readExampleFinancialState(t, st, user.ID, key.ID); !reflect.DeepEqual(before, after) {
				t.Fatal("read-only quotations changed financial state", before, after)
			}
			g.Close()
			if err := st.Close(); err != nil {
				t.Fatal(err)
			}
			reopened, err := store.Open(database)
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			restored := billing.NewQuotationBook(reopened.RecordPriceSnapshot, reopened.QuotationHistory)
			if err := restored.Replace(exampleQuotationConfig(true)); err != nil {
				t.Fatal(err)
			}
			historical, err := restored.Calculate(context.Background(), q.Input{PriceVersion: pinned.Version, Model: quote.Model, Group: quote.Group, Usage: usage})
			if err != nil || !reflect.DeepEqual(historical, quote) {
				t.Fatal("restart changed historical quotation", historical, quote, err)
			}
			if after := readExampleFinancialState(t, reopened, user.ID, key.ID); !reflect.DeepEqual(before, after) {
				t.Fatal("history recovery changed financial state", before, after)
			}
			if hits.Load() != 1 {
				t.Fatal("history read repeated generation", hits.Load())
			}
		})
	}
}

type quotationExampleExecutor struct {
	calls   int
	outcome v3.Outcome
}

func (e *quotationExampleExecutor) Execute(context.Context, v1.Output, v1.Invocation) v3.Outcome {
	e.calls++
	return e.outcome
}

type quotationExampleAuthority struct{}

func (quotationExampleAuthority) Current(context.Context) (identity.Principal, bool) {
	return identity.Principal{UserID: 1, TokenID: 2}, true
}
func (quotationExampleAuthority) Refresh(ctx context.Context, _ identity.Binding) (context.Context, identity.Principal, error) {
	return ctx, identity.Principal{UserID: 1, TokenID: 2}, nil
}

type quotationExampleReader struct{ calculations int }

func (*quotationExampleReader) Current(context.Context) (q.Current, error) {
	return q.Current{Enabled: true, Version: "pinned"}, nil
}
func (r *quotationExampleReader) Calculate(context.Context, q.Input) (q.Quote, error) {
	r.calculations++
	return q.Quote{}, nil
}

func TestQuotationConsumerRejectsAmbiguousAttemptGroupsWithoutReexecution(t *testing.T) {
	for _, outcome := range []v3.Outcome{
		{Execution: v2.Outcome{Legacy: v1.Outcome{ChannelGroup: "final-group", Attempts: []v1.AttemptOutcome{{Model: "first-provider"}, {Model: "second-provider"}}}}, Usage: []v3.AttemptUsage{{Number: 1}, {Number: 2}}},
		{Execution: v2.Outcome{Legacy: v1.Outcome{ChannelGroup: "final-group", Attempts: []v1.AttemptOutcome{{Model: "provider"}}}}, Usage: []v3.AttemptUsage{{Number: 2}}},
	} {
		executor := &quotationExampleExecutor{outcome: outcome}
		reader := &quotationExampleReader{}
		service := facts.QuotationService{Authority: quotationExampleAuthority{}, Executor: executor, Quotes: reader}
		out, err := service.Run(context.Background(), httpoutput.New(httptest.NewRecorder()), []byte(`{"model":"client-alias","input":"x"}`))
		if err == nil || executor.calls != 1 || reader.calculations != 0 || !reflect.DeepEqual(out.Execution, outcome) {
			t.Fatal("ambiguous attempts reused final group or lost execution", out, err, executor.calls, reader.calculations)
		}
	}
}
