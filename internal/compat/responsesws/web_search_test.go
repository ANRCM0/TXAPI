package responsesws

import "testing"

func TestHTTPWebSearchDoesNotExpandWebSocketScope(t *testing.T) {
	for _, extra := range []map[string]any{
		{"store": false, "tools": []any{map[string]any{"type": "web_search", "external_web_access": false, "search_content_types": []string{"text", "image"}}}},
		{"store": false, "input": []any{map[string]any{"type": "web_search_call", "id": "ws_fixture", "status": "completed", "action": map[string]any{"type": "search", "query": "fixture"}}}},
	} {
		frame := requestFrame(t, extra)
		if _, err := Parse(frame); err == nil {
			t.Fatal("HTTP search expanded connection-local WS")
		}
		if _, err := ParseStored(frame); err == nil {
			t.Fatal("HTTP search expanded persistent WS")
		}
	}
}
