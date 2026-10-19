package facts_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"minigateway/internal/access"
	"minigateway/internal/billing"
	"minigateway/internal/compat/httpoutput"
	"minigateway/internal/config"
	"minigateway/internal/controlplane"
	"minigateway/internal/examples/facts"
	v1 "minigateway/internal/foundation/contracts/execution/v1"
	v3 "minigateway/internal/foundation/contracts/execution/v3"
	identity "minigateway/internal/foundation/contracts/identity/v1"
	"minigateway/internal/foundation/contracts/observability/v1"
	quotation "minigateway/internal/foundation/contracts/quotation/v1"
	history "minigateway/internal/foundation/contracts/usagehistory/v1"
	identityimpl "minigateway/internal/foundation/identity"
	"minigateway/internal/foundation/persistence"
	"minigateway/internal/gateway"
	"minigateway/internal/model"
	"minigateway/internal/protocol"
	"minigateway/internal/store"
	"minigateway/internal/usageconsole"
)

func assertHistoryCounts(t *testing.T, usage history.UsageView, expected [6]string) {
	t.Helper()
	counts := [6]*string{usage.InputTokens, usage.OutputTokens, usage.TotalTokens,
		usage.CachedInputTokens, usage.CacheWriteInputTokens, usage.ReasoningOutputTokens}
	for index, want := range expected {
		if want == "" {
			if counts[index] != nil {
				t.Fatalf("unknown count %d became %q", index, *counts[index])
			}
		} else if counts[index] == nil || *counts[index] != want {
			t.Fatalf("count %d lost exact value %q: %+v", index, want, usage)
		}
	}
	encoded, err := json.Marshal(usage)
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]any
	if err := json.Unmarshal(encoded, &wire); err != nil {
		t.Fatal(err)
	}
	for index, name := range []string{"input_tokens", "output_tokens", "total_tokens", "cached_input_tokens", "cache_write_input_tokens", "reasoning_output_tokens"} {
		value, exists := wire[name]
		if !exists || expected[index] == "" && value != nil || expected[index] != "" && value != expected[index] {
			t.Fatalf("count %s must serialize as null or an exact string: %s", name, encoded)
		}
	}
}

func TestIndependentUsageHistoryConsumerPersistsRealJSONSSEAndCurrentAuthority(t *testing.T) {
	database := config.DatabaseConfig{Driver: "sqlite", Path: filepath.Join(t.TempDir(), "usage-history.db")}
	st, err := store.Open(database)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if st != nil {
			_ = st.Close()
		}
	})
	book := billing.NewQuotationBook(st.RecordPriceSnapshot, st.QuotationHistory)
	if err := book.Replace(exampleQuotationConfig(false)); err != nil {
		t.Fatal(err)
	}
	pinned, err := book.Current(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name, usage string
		counts      [6]string
	}{
		{"details", `{"input_tokens":10,"output_tokens":4,"total_tokens":14,"input_tokens_details":{"cached_tokens":2,"cache_write_tokens":1},"output_tokens_details":{"reasoning_tokens":3}}`, [6]string{"10", "4", "14", "2", "1", "3"}},
		{"missing", "", [6]string{}},
		{"null", `{"input_tokens":null,"output_tokens":null,"total_tokens":null,"input_tokens_details":{"cached_tokens":null,"cache_write_tokens":null},"output_tokens_details":{"reasoning_tokens":null}}`, [6]string{}},
		{"zero", `{"input_tokens":0,"output_tokens":0,"total_tokens":0,"input_tokens_details":{"cached_tokens":0,"cache_write_tokens":0},"output_tokens_details":{"reasoning_tokens":0}}`, [6]string{"0", "0", "0", "0", "0", "0"}},
		{"large", `{"input_tokens":9007199254740993,"output_tokens":0,"total_tokens":9007199254740993,"input_tokens_details":{"cached_tokens":0,"cache_write_tokens":1},"output_tokens_details":{"reasoning_tokens":0}}`, [6]string{"9007199254740993", "0", "9007199254740993", "0", "1", "0"}},
		{"detail-only", `{"input_tokens_details":{"cached_tokens":0}}`, [6]string{"", "", "", "0", "", ""}},
	}
	var hits atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count := hits.Add(1)
		var request struct {
			Model, Input string
			Stream       bool
		}
		if json.NewDecoder(r.Body).Decode(&request) != nil || request.Model != "provider-model" || r.Header.Get("Authorization") != "Bearer usage-upstream-fixture" {
			t.Error("generation bypassed real model mapping or credential preparation")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if count == 1 {
			if err := book.Replace(exampleQuotationConfig(true)); err != nil {
				t.Error(err)
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
		}
		document := `{"id":"native-history","object":"response","status":"completed","output":[]}`
		matched := false
		for _, item := range cases {
			if request.Input == item.name {
				matched = true
				if item.usage != "" {
					document = strings.TrimSuffix(document, "}") + `,"usage":` + item.usage + "}"
				}
				break
			}
		}
		if !matched {
			t.Error("unexpected generation input", request.Input)
		}
		if request.Stream {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "event: response.completed\ndata: "+`{"type":"response.completed","response":`+document+"}\n\n")
		} else {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, document)
		}
	}))
	t.Cleanup(upstream.Close)
	createUser := func(name, role string) (*model.User, string) {
		t.Helper()
		user, err := st.CreateUser(&store.UserInput{Username: name, Password: "usage-history-password", Role: role,
			ModelMode: access.Allowlist, Models: []string{"history-alias"}})
		if err != nil {
			t.Fatal(err)
		}
		session, _, err := st.Login(name, "usage-history-password")
		if err != nil {
			t.Fatal(err)
		}
		return user, session
	}
	owner, ownerSession := createUser("history-owner", "user")
	_, otherSession := createUser("history-other", "user")
	_, adminSession := createUser("history-admin", "admin")
	key, err := st.IssueToken(owner.ID, &store.TokenInput{Name: "history-key", Models: []string{"history-alias"}})
	if err != nil {
		t.Fatal(err)
	}
	channel, err := st.CreateChannel(&store.ChannelInput{Name: "history-upstream", Type: "openai", BaseURL: upstream.URL,
		Models: []string{"history-alias"}, ModelMapping: map[string]string{"history-alias": "provider-model"},
		GroupName: "actual-sale", Weight: 1, APIKey: "usage-upstream-fixture"})
	if err != nil {
		t.Fatal(err)
	}
	balances := []model.BillingBalance{{OwnerType: "user", OwnerID: owner.ID, UsedUnits: 37, ReservedUnits: 11},
		{OwnerType: "token", OwnerID: key.ID, UsedUnits: 37, ReservedUnits: 11}}
	if err := st.DB().Create(&balances).Error; err != nil {
		t.Fatal(err)
	}
	if err := st.DB().Create(&model.BillingReservation{ID: "history-unrelated-hold", UserID: owner.ID, TokenID: key.ID,
		OriginModel: "history-alias", PriceVersion: "existing-price", ReservedUnits: 11, State: "reserved",
		LeaseUntil: time.Now().Add(time.Hour).UnixMilli()}).Error; err != nil {
		t.Fatal(err)
	}
	before := readExampleFinancialState(t, st, owner.ID, key.ID)
	channels, err := st.ChannelSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	g, err := gateway.New(gateway.Options{Channels: channels, BillingMode: billing.ModeDisabled,
		Operations: protocol.Operations(), Protocols: protocol.Protocols(nil), TotalTimeout: 3 * time.Second,
		UsageHistory:   st.UsageRecorder(model.UsageHistoryLimits{InstanceRecords: 64, OwnerRecords: 32, RetentionSeconds: 3600}),
		UsageQuotation: book})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(g.Close)
	ctx, _, ok := identityimpl.NewTokenAuthenticator(st).Authenticate(context.Background(), key.Key)
	if !ok {
		t.Fatal("real authentication failed")
	}
	ctx = observability.WithRequestID(ctx, "same-client-request-id")
	service := facts.UsageService{Authority: identityimpl.New(st.ResolvePrincipal), Executor: gateway.NewUsageFactsExecutor(g)}
	limiter := access.NewMemoryLimiter()
	console := usageconsole.New(st.UsageHistory(true, 3600), controlplane.New(persistence.Control(st)), limiter)
	adminAccess := history.Access{Session: adminSession, Administrator: true}
	ownerAccess := history.Access{Session: ownerSession}
	ids := make(map[string]bool)
	var firstID string
	var firstUsage v3.UsageObservation
	var firstDetail history.Detail
	for _, stream := range []bool{false, true} {
		for _, item := range cases {
			body, err := json.Marshal(map[string]any{"model": "history-alias", "input": item.name, "stream": stream,
				"metadata": map[string]string{"group": "client-forged-group"}})
			if err != nil {
				t.Fatal(err)
			}
			output := httptest.NewRecorder()
			previous := hits.Load()
			out, err := service.Run(ctx, httpoutput.New(output), body)
			if err != nil || out.Execution.Legacy.StatusCode != http.StatusOK || hits.Load() != previous+1 ||
				len(out.Execution.Attempts) != 1 || len(out.Usage) != 1 || strings.Count(output.Body.String(), `"object":"response"`) != 1 {
				t.Fatalf("%s stream=%v did not execute/deliver once: %+v err=%v hits=%d wire=%s", item.name, stream, out, err, hits.Load(), output.Body.String())
			}
			id := output.Header().Get("X-TXAPI-Usage-ID")
			if !regexp.MustCompile(`^usage_[a-f0-9]{32}$`).MatchString(id) || ids[id] {
				t.Fatal("server usage ID missing or client Request ID deduplicated an invocation", id)
			}
			ids[id] = true
			admin, err := console.Read(context.Background(), adminAccess, id)
			if err != nil || admin.Record.State != "sealed" || admin.Record.AttemptCount != 1 || len(admin.Attempts) != 1 ||
				admin.Record.UserID != owner.ID || admin.Record.TokenID != key.ID || admin.Record.Operation != v1.Responses ||
				admin.Record.Model != "history-alias" || admin.Record.Stream != stream || admin.Record.RequestID != "same-client-request-id" {
				t.Fatalf("durable metadata did not match accepted invocation: %+v err=%v", admin, err)
			}
			attempt := admin.Attempts[0]
			if attempt.Number != 1 || attempt.State != "observed" || !attempt.MayDispatch || attempt.ChannelID != channel.ID ||
				attempt.CredentialID == 0 || attempt.ActualModel != "provider-model" || attempt.Group != "actual-sale" || attempt.Protocol == "" ||
				!attempt.Host.ProtocolComplete || attempt.Host.Status != http.StatusOK {
				t.Fatalf("actual target/host facts were not persisted: %+v", attempt)
			}
			assertHistoryCounts(t, attempt.Usage, item.counts)
			observed := out.Usage[0].Usage
			if observed.Validate() != nil || attempt.Usage.Completeness != string(observed.Completeness) ||
				attempt.Usage.Source != string(observed.Source) || attempt.Usage.Profile != observed.Profile {
				t.Fatal("durable observation lost native provenance", attempt.Usage, observed)
			}
			ownerDetail, err := console.Read(context.Background(), ownerAccess, id)
			if err != nil || len(ownerDetail.Attempts) != 1 || ownerDetail.Record.QuotationVersion != "" {
				t.Fatal("owner projection unavailable or leaked quotation version", ownerDetail, err)
			}
			projected := ownerDetail.Attempts[0]
			if projected.ActualModel != "" || projected.Group != "" || projected.Protocol != "" || projected.ChannelID != 0 || projected.CredentialID != 0 {
				t.Fatal("owner projection exposed administrator target metadata", projected)
			}
			assertHistoryCounts(t, projected.Usage, item.counts)
			if firstID == "" {
				firstID, firstUsage, firstDetail = id, out.Usage[0].Usage, admin
				if admin.Record.QuotationVersion != pinned.Version {
					t.Fatal("active execution picked newer quotation version", admin.Record.QuotationVersion, pinned.Version)
				}
			} else if admin.Record.QuotationVersion == "" {
				t.Fatal("quotation provenance was not captured")
			}
		}
	}
	page, err := console.List(context.Background(), ownerAccess, history.Query{Limit: 50})
	if err != nil || len(page.Data) != len(ids) || page.HasMore || !page.Enabled {
		t.Fatal("current owner list lost independent same-request-ID records", page, err)
	}
	for _, row := range page.Data {
		if !ids[row.ID] || row.QuotationVersion != "" {
			t.Fatal("owner list returned unknown record or private quotation version", row)
		}
	}
	if _, err := console.Read(context.Background(), history.Access{Session: otherSession}, firstID); !errors.Is(err, history.ErrNotFound) {
		t.Fatal("cross-owner detail must be absent", err)
	}
	if _, err := console.Read(context.Background(), history.Access{Session: ownerSession, Administrator: true}, firstID); !errors.Is(err, history.ErrForbidden) {
		t.Fatal("requested administrator scope became authority", err)
	}
	if err := st.DeleteOwnedToken(owner.ID, key.ID); err != nil {
		t.Fatal(err)
	}
	previous := hits.Load()
	if _, err := service.Run(ctx, httpoutput.New(httptest.NewRecorder()), []byte(`{"model":"history-alias","input":"details"}`)); !errors.Is(err, identity.ErrUnauthorized) || hits.Load() != previous {
		t.Fatal("revoked key acquired another upstream execution", err, hits.Load())
	}
	if detail, err := console.Read(context.Background(), ownerAccess, firstID); err != nil || detail.Record.ID != firstID {
		t.Fatal("key revocation erased previously authorized owner metadata", detail, err)
	}
	if after := readExampleFinancialState(t, st, owner.ID, key.ID); !reflect.DeepEqual(before, after) {
		t.Fatal("recording or reading usage changed balances/reservations", before, after)
	}
	current, err := book.Current(context.Background())
	if err != nil || current.Version == pinned.Version {
		t.Fatal("fixture did not publish a new quotation during execution", current, err)
	}
	g.Close()
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	st, err = store.Open(database)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.InterruptUsageHistory(context.Background()); err != nil {
		t.Fatal(err)
	}
	console = usageconsole.New(st.UsageHistory(false, 3600), controlplane.New(persistence.Control(st)), limiter)
	reopened, err := console.Read(context.Background(), adminAccess, firstID)
	if err != nil || !reflect.DeepEqual(firstDetail, reopened) {
		t.Fatal("restart or disabled recording changed sealed historical metadata", reopened, firstDetail, err)
	}
	restored := billing.NewQuotationBook(st.RecordPriceSnapshot, st.QuotationHistory)
	if err := restored.Replace(exampleQuotationConfig(true)); err != nil {
		t.Fatal(err)
	}
	oldQuote, err := restored.Calculate(context.Background(), quotation.Input{PriceVersion: reopened.Record.QuotationVersion,
		Model: reopened.Attempts[0].ActualModel, Group: reopened.Attempts[0].Group, Usage: firstUsage})
	if err != nil || !oldQuote.Known || oldQuote.PriceVersion != pinned.Version || oldQuote.AmountUnits != "44" {
		t.Fatal("stored provenance could not recover historical quotation", oldQuote, err)
	}
	if after := readExampleFinancialState(t, st, owner.ID, key.ID); !reflect.DeepEqual(before, after) || hits.Load() != previous {
		t.Fatal("history restart/quotation changed financial state or replayed upstream", before, after, hits.Load())
	}
}
