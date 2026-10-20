package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	v1 "minigateway/internal/foundation/contracts/execution/v1"
	v2 "minigateway/internal/foundation/contracts/execution/v2"
	"minigateway/internal/protocol"
)

func TestEmbeddingsNativeUsageBothSocketsAndAccountingProjection(t *testing.T) {
	count := func(n int64) *int64 { return &n }
	for _, tc := range []struct {
		name, usage string
		counts      v2.TokenCounts
		state       v2.Completeness
		financial   bool
	}{
		{"input only", `{"prompt_tokens":11,"total_tokens":11}`, v2.TokenCounts{InputTokens: count(11), TotalTokens: count(11)}, v2.Partial, true},
		{"zero", `{"prompt_tokens":0,"total_tokens":0}`, v2.TokenCounts{InputTokens: count(0), TotalTokens: count(0)}, v2.Partial, true},
		{"explicit output", `{"prompt_tokens":11,"completion_tokens":0,"total_tokens":11}`, v2.TokenCounts{InputTokens: count(11), OutputTokens: count(0), TotalTokens: count(11)}, v2.Complete, true},
		{"null output", `{"prompt_tokens":11,"completion_tokens":null,"total_tokens":11}`, v2.TokenCounts{InputTokens: count(11), TotalTokens: count(11)}, v2.Partial, true},
		{"input missing", `{"total_tokens":11}`, v2.TokenCounts{TotalTokens: count(11)}, v2.Partial, false},
		{"total missing", `{"prompt_tokens":11}`, v2.TokenCounts{InputTokens: count(11)}, v2.Partial, false},
		{"large exact", `{"prompt_tokens":9007199254740993,"total_tokens":9007199254740993}`, v2.TokenCounts{InputTokens: count(9007199254740993), TotalTokens: count(9007199254740993)}, v2.Partial, true},
		{"unknown", `null`, v2.TokenCounts{}, v2.Unknown, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wire := `{"object":"list","data":[{"embedding":"AQID","index":0}],"usage":` + tc.usage + `,"extension":true}`
			var sends atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				sends.Add(1)
				if r.URL.Path != "/v1/embeddings" {
					t.Error("operation changed", r.URL.Path)
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, wire)
			}))
			defer upstream.Close()
			probe := &historyRecorderProbe{}
			gateway := newP0SocketGatewayWithOptions(t, upstream.URL, protocol.Embeddings, func(options *Options) { options.UsageHistory = probe })
			response, err := gateway.server.Client().Post(gateway.server.URL+"/test", "application/json", strings.NewReader(`{"model":"m","input":"hello"}`))
			if err != nil {
				t.Fatal(err)
			}
			data, readErr := io.ReadAll(response.Body)
			_ = response.Body.Close()
			result := gateway.result(t)
			if readErr != nil || response.StatusCode != 200 || string(data) != wire || !result.ProtocolComplete || result.RecordingState != "sealed" || sends.Load() != 1 || len(result.Attempts) != 1 {
				t.Fatal("wire, attempt or terminal changed", result, response.StatusCode, readErr)
			}
			if len(probe.receipts) != 1 || len(probe.finishes) != 1 || response.Header.Get("X-TXAPI-Usage-ID") != result.UsageRecordID || len(result.protocolFacts) != 1 || len(result.usageFacts) != 1 {
				t.Fatal("recording identity or lifecycle changed", result, probe.events)
			}
			native := probe.receipts[0].Usage
			if native.Completeness != tc.state || !reflect.DeepEqual(native.Counts, tc.counts) || !reflect.DeepEqual(result.protocolFacts[0].Usage.Counts, tc.counts) || !reflect.DeepEqual(result.usageFacts[0].Usage, native) {
				t.Fatal("native projections changed", native, result.protocolFacts, result.usageFacts)
			}
			if (result.Usage != nil) != tc.financial {
				t.Fatal("input-only financial completeness changed", result.Usage)
			}
			if result.Usage != nil && (int64(result.Usage.PromptTokens) != *tc.counts.InputTokens || result.Usage.CompletionTokens != 0 || int64(result.Usage.TotalTokens) != *tc.counts.TotalTokens) {
				t.Fatal("financial projection disagrees with reported input", result.Usage)
			}
			if native.Counts.OutputTokens == nil && result.protocolFacts[0].Usage.Counts.OutputTokens != nil {
				t.Fatal("financial zero leaked into native output")
			}
		})
	}
}

func TestInputOnlyAccountingProjectionIsOperationAndProfileBound(t *testing.T) {
	input, total := int64(11), int64(11)
	usage := unknownDetailedUsageFact()
	usage.Counts = v2.TokenCounts{InputTokens: &input, TotalTokens: &total}
	usage.Source, usage.Completeness, usage.Profile = v2.ProviderReported, v2.Partial, "embeddings_native_tokens_v1"
	for _, operation := range []v1.Operation{v1.Chat, v1.Responses, "unknown"} {
		if got := inputOnlyAccountingUsage(operation, &usage); got != nil {
			t.Fatal("another operation acquired input-only accounting", operation, got)
		}
	}
	usage.Profile = "chat_native_tokens_v1"
	if got := inputOnlyAccountingUsage(v1.Embeddings, &usage); got != nil {
		t.Fatal("another profile acquired input-only accounting", got)
	}
}
