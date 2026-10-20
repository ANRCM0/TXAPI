package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func TestCodexFixtureNativeRoundsAndRefusals(t *testing.T) {
	for _, model := range []string{"native-cli-text", "native-cli-function-result", "native-cli-custom", "native-cli-tool-search"} {
		t.Run(model, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			f := &fixture{root: ctx}
			server := httptest.NewTLSServer(http.HandlerFunc(f.provider))
			defer server.Close()
			c, _, err := websocket.Dial(ctx, "wss"+strings.TrimPrefix(server.URL, "https")+"/v1/responses", &websocket.DialOptions{HTTPClient: server.Client(), HTTPHeader: http.Header{"Authorization": {"Bearer " + upstreamKey}}})
			if err != nil {
				t.Fatal(err)
			}
			defer c.CloseNow()
			request := map[string]any{"type": "response.create", "model": model, "stream_id": "main", "store": false,
				"input": []any{map[string]any{"role": "user", "content": "synthetic fixture"}}, "generate": false}
			round := func() map[string]any {
				t.Helper()
				raw, _ := json.Marshal(request)
				if err := c.Write(ctx, websocket.MessageText, raw); err != nil {
					t.Fatal(err)
				}
				for count := 0; count < 24; count++ {
					_, raw, err := c.Read(ctx)
					if err != nil {
						t.Fatal(err)
					}
					var event map[string]any
					if json.Unmarshal(raw, &event) != nil || event["sequence_number"] != float64(count) {
						t.Fatal("invalid ordered event", string(raw))
					}
					if event["type"] == "response.completed" {
						return event["response"].(map[string]any)
					}
				}
				t.Fatal("terminal not emitted within bounded fixture events")
				return nil
			}
			warm := round()
			if len(warm["output"].([]any)) != 0 || warm["usage"].(map[string]any)["total_tokens"] != float64(0) {
				t.Fatal("prewarm fabricated output or nonzero usage")
			}
			delete(request, "generate")
			request["previous_response_id"] = warm["id"]
			first := round()
			output := first["output"].([]any)
			if len(output) != 2 || output[0].(map[string]any)["encrypted_content"] != "fixture-opaque-only" {
				t.Fatal("reasoning lost")
			}
			if model != "native-cli-text" {
				kind := map[string]string{"native-cli-function-result": "function_call_output", "native-cli-custom": "custom_tool_call_output", "native-cli-tool-search": "tool_search_output"}[model]
				request["previous_response_id"] = first["id"]
				request["input"] = []any{map[string]any{"type": kind, "call_id": "call_codex_fixture", "output": "synthetic result"}}
				result := round()
				item := result["output"].([]any)[1].(map[string]any)
				if item["type"] != "message" || result["usage"].(map[string]any)["total_tokens"] != float64(3) {
					t.Fatal("tool result did not complete one real generation")
				}
			}
			// A new physical connection cannot inherit the first connection's ID.
			other, _, err := websocket.Dial(ctx, "wss"+strings.TrimPrefix(server.URL, "https")+"/v1/responses", &websocket.DialOptions{HTTPClient: server.Client(), HTTPHeader: http.Header{"Authorization": {"Bearer " + upstreamKey}}})
			if err != nil {
				t.Fatal(err)
			}
			defer other.CloseNow()
			raw, _ := json.Marshal(request)
			if err := other.Write(ctx, websocket.MessageText, raw); err != nil {
				t.Fatal(err)
			}
			if _, _, err := other.Read(ctx); err == nil {
				t.Fatal("fixture accepted a foreign parent")
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			if len(f.errors) != 1 || f.errors[0] != "Codex prewarm or connection-local parent changed" {
				t.Fatal("foreign parent did not leave a failure observation", f.errors)
			}
		})
	}
}
