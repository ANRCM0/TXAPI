package facts_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"minigateway/internal/access"
	"minigateway/internal/billing"
	"minigateway/internal/compat/httpoutput"
	"minigateway/internal/config"
	"minigateway/internal/examples/facts"
	v2 "minigateway/internal/foundation/contracts/execution/v2"
	identity "minigateway/internal/foundation/contracts/identity/v1"
	identityimpl "minigateway/internal/foundation/identity"
	"minigateway/internal/gateway"
	"minigateway/internal/protocol"
	"minigateway/internal/store"
)

// Only the test composition root imports implementations; the L3 service is
// separately checked to consume public contracts exclusively.
func TestIndependentFactsConsumerUsesCurrentAuthorityAndRealJSONSSE(t *testing.T) {
	var hits atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		var request struct {
			Model, Input string
			Stream       bool
		}
		if json.NewDecoder(r.Body).Decode(&request) != nil || request.Model != "provider-model" || r.Header.Get("Authorization") != "Bearer upstream-fixture" {
			t.Error("native mapping or credential preparation was bypassed")
		}
		document := `{"id":"native-facts","object":"response","status":"completed","output":[]}`
		if request.Input == "zero" {
			document = strings.TrimSuffix(document, "}") + `,"usage":{"input_tokens":0,"output_tokens":0,"total_tokens":0}}`
		}
		if request.Input == "details" {
			document = strings.TrimSuffix(document, "}") + `,"usage":{"input_tokens":10,"output_tokens":6,"total_tokens":16,"input_tokens_details":{"cached_tokens":3,"cache_write_tokens":2},"output_tokens_details":{"reasoning_tokens":4}}}`
		}
		if request.Input == "detail-only" {
			document = strings.TrimSuffix(document, "}") + `,"usage":{"input_tokens_details":{"cached_tokens":0}}}`
		}
		if request.Stream {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "event: response.completed\ndata: "+`{"type":"response.completed","response":`+document+"}\n\n")
		} else {
			_, _ = io.WriteString(w, document)
		}
	}))
	defer upstream.Close()
	st, err := store.Open(config.DatabaseConfig{Driver: "sqlite", Path: filepath.Join(t.TempDir(), "facts.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	user, err := st.CreateUser(&store.UserInput{Username: "facts-user", Password: "example-password-123", ModelMode: access.Allowlist, Models: []string{"m"}})
	if err != nil {
		t.Fatal(err)
	}
	key, err := st.IssueToken(user.ID, &store.TokenInput{Name: "facts-key", Models: []string{"m"}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = st.CreateChannel(&store.ChannelInput{Name: "facts-upstream", Type: "openai", BaseURL: upstream.URL, Models: []string{"m"},
		ModelMapping: map[string]string{"m": "provider-model"}, Weight: 1, APIKey: "upstream-fixture"})
	if err != nil {
		t.Fatal(err)
	}
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
		t.Fatal("real authentication failed")
	}
	service := facts.Service{Authority: identityimpl.New(st.ResolvePrincipal), Executor: gateway.NewFactsExecutor(g)}
	usageService := facts.UsageService{Authority: service.Authority, Executor: gateway.NewUsageFactsExecutor(g)}
	for _, stream := range []bool{false, true} {
		for _, input := range []string{"missing", "zero"} {
			body, _ := json.Marshal(map[string]any{"model": "m", "input": input, "stream": stream})
			recorder := httptest.NewRecorder()
			before := hits.Load()
			out, err := service.Run(ctx, httpoutput.New(recorder), body)
			if err != nil || out.Legacy.StatusCode != 200 || hits.Load() != before+1 || len(out.Attempts) != 1 || !out.Attempts[0].ProtocolComplete || strings.Count(recorder.Body.String(), `"object":"response"`) != 1 {
				t.Fatalf("independent consumer bypassed execution or repeated wire: err=%v result=%+v body=%s", err, out, recorder.Body.String())
			}
			if input == "missing" && out.Attempts[0].Usage.Completeness != v2.Unknown {
				t.Fatal("missing usage became an explicit zero")
			}
			if input == "zero" && (out.Attempts[0].Usage.Completeness != v2.Complete || out.Attempts[0].Usage.Counts.TotalTokens == nil || *out.Attempts[0].Usage.Counts.TotalTokens != 0) {
				t.Fatal("explicit zero lost its provider provenance")
			}
		}
	}
	for _, stream := range []bool{false, true} {
		for _, input := range []string{"missing", "zero", "details", "detail-only"} {
			body, _ := json.Marshal(map[string]any{"model": "m", "input": input, "stream": stream})
			recorder := httptest.NewRecorder()
			before := hits.Load()
			out, err := usageService.Run(ctx, httpoutput.New(recorder), body)
			if err != nil || out.Execution.Legacy.StatusCode != 200 || len(out.Execution.Attempts) != 1 || len(out.Usage) != 1 || hits.Load() != before+1 || strings.Count(recorder.Body.String(), `"object":"response"`) != 1 {
				t.Fatalf("usage consumer did not use one real execution: err=%v outcome=%+v body=%s", err, out, recorder.Body.String())
			}
			observation := out.Usage[0]
			if observation.Number != out.Execution.Attempts[0].Number || observation.Usage.Validate() != nil {
				t.Fatalf("unbound or invalid usage observation: %+v", observation)
			}
			base := out.Execution.Attempts[0].Usage
			switch input {
			case "missing":
				if observation.Usage.Completeness != v2.Unknown || observation.Usage.Breakdown.CachedInputTokens != nil {
					t.Fatal("missing details acquired known quantities")
				}
			case "zero":
				if observation.Usage.Completeness != v2.Complete || observation.Usage.Counts.TotalTokens == nil || *observation.Usage.Counts.TotalTokens != 0 {
					t.Fatal("explicit zero lost in enriched consumer")
				}
			case "details":
				d := observation.Usage.Breakdown
				if d.CachedInputTokens == nil || *d.CachedInputTokens != 3 || d.CacheWriteInputTokens == nil || *d.CacheWriteInputTokens != 2 || d.ReasoningOutputTokens == nil || *d.ReasoningOutputTokens != 4 || base.Profile != "responses_native_tokens_v1" || observation.Usage.Profile != "responses_native_tokens_v2" {
					t.Fatalf("details/profiles were lost: %+v", observation)
				}
			case "detail-only":
				if observation.Usage.Completeness != v2.Partial || observation.Usage.Counts.InputTokens != nil || observation.Usage.Breakdown.CachedInputTokens == nil || *observation.Usage.Breakdown.CachedInputTokens != 0 || base.Completeness != v2.Unknown {
					t.Fatal("detail-only zero inferred parent or complete base usage")
				}
			}
		}
	}
	if _, err := service.Run(context.Background(), httpoutput.New(httptest.NewRecorder()), []byte(`{"model":"m","input":"x"}`)); !errors.Is(err, identity.ErrUnauthorized) {
		t.Fatal("untrusted context acquired authority from facts")
	}
	if err := st.DeleteOwnedToken(key.UserID, key.ID); err != nil {
		t.Fatal(err)
	}
	before := hits.Load()
	if _, err := service.Run(ctx, httpoutput.New(httptest.NewRecorder()), []byte(`{"model":"m","input":"x"}`)); !errors.Is(err, identity.ErrUnauthorized) || hits.Load() != before {
		t.Fatal("old context survived key revocation")
	}
	if _, err := usageService.Run(ctx, httpoutput.New(httptest.NewRecorder()), []byte(`{"model":"m","input":"details"}`)); !errors.Is(err, identity.ErrUnauthorized) || hits.Load() != before {
		t.Fatal("enriched facts bypassed current authorization")
	}
}
