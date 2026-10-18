package protocol

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"minigateway/internal/core"
	execv1 "minigateway/internal/foundation/contracts/execution/v1"
	"minigateway/internal/gateway"
	"minigateway/internal/protocol/meta"
)

func TestAnthropicBlockFailuresDoNotEmitDone(t *testing.T) {
	for _, tc := range []struct {
		name   string
		events []string
	}{
		{name: "closed block delta", events: []string{
			`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":"visible"}}`,
			`{"type":"content_block_stop","index":0}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"invalid"}}`,
		}},
		{name: "mixed initial tool input", events: []string{
			`{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"call","name":"f","input":{"a":1}}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{}"}}`,
		}},
		{name: "unfinished content block", events: []string{
			`{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"call","name":"f","input":{}}}`,
			`{"type":"message_delta","delta":{"stop_reason":"tool_use"}}`,
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := "data: " + strings.Join(tc.events, "\n\ndata: ") + "\n\ndata: {\"type\":\"message_stop\"}\n\n"
			a, _ := Resolve("anthropic")
			executor := core.New(core.Options{Client: &http.Client{Transport: staticResponse{body: strings.NewReader(body)}}})
			defer executor.Close()
			rec := httptest.NewRecorder()
			result := executor.Execute(context.Background(), rec, gateway.ToCoreInput(execv1.Input{URL: "http://upstream.test", Stream: true, Decoder: NewDecoder(a, &meta.Meta{IsStream: true})}))
			if result.Error == nil || result.ProtocolComplete || !result.Committed || strings.Contains(rec.Body.String(), "[DONE]") {
				t.Fatalf("invalid block sequence completed: result=%+v body=%s", result, rec.Body.String())
			}
		})
	}
}
