package responsesws

import "testing"

func TestHTTPCompactionDoesNotExpandWebSocketScope(t *testing.T) {
	for _, item := range []map[string]any{
		{"type": "compaction_trigger"},
		{"type": "compaction", "id": "cmp_fixture", "encrypted_content": "opaque-fixture"},
	} {
		frame := requestFrame(t, map[string]any{"store": false, "input": []any{item}})
		if _, err := Parse(frame); err == nil {
			t.Fatal("HTTP compaction expanded connection-local WS")
		}
		if _, err := ParseStored(frame); err == nil {
			t.Fatal("HTTP compaction expanded persistent WS")
		}
	}
}
