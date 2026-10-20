package responsesws

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	v2 "minigateway/internal/foundation/contracts/execution/v2"
)

func TestNativeClientOptionsPrewarmAndOpaqueReasoningPreserveWire(t *testing.T) {
	input := []any{map[string]any{"type": "reasoning", "id": "rs_one", "summary": []any{map[string]any{"type": "summary_text", "text": "雪🌏"}}, "encrypted_content": "opaque"}}
	frame := requestFrame(t, map[string]any{"generate": false, "stream": true, "input": input,
		"include": []string{"reasoning.encrypted_content"}, "reasoning": map[string]string{"effort": "medium", "summary": "auto"},
		"text":            map[string]any{"verbosity": "low", "format": map[string]string{"type": "text"}},
		"client_metadata": map[string]string{"request": "opaque-client-value"}, "prompt_cache_key": "client-cache"})
	r, err := Parse(frame)
	if err != nil || !strings.Contains(r.PromptText, "opaque") || !strings.Contains(r.PromptText, "雪🌏") {
		t.Fatal("reasoning omitted from estimate or request", r.PromptText, err)
	}
	mapped, err := MapModel(r.Initial, "actual")
	if err != nil || !bytes.Contains(mapped.Data, []byte(`"generate":false`)) || !bytes.Contains(mapped.Data, []byte(`"stream":true`)) || !bytes.Contains(mapped.Data, []byte(`"client_metadata":{"request":"opaque-client-value"}`)) || !bytes.Contains(mapped.Data, []byte(`"encrypted_content":"opaque"`)) {
		t.Fatal("native prewarm/options were changed", string(mapped.Data), err)
	}
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(frame.Data, &fields)
	fields["store"] = json.RawMessage("true")
	data, _ := json.Marshal(fields)
	if _, err := ParseStored(v2.Frame{Type: 1, Data: data}); err == nil {
		t.Fatal("prewarm created permanent history")
	}
}

func TestNativeClientOptionsFailClosedAtExactBounds(t *testing.T) {
	for _, extra := range []map[string]any{
		{"stream": false}, {"stream": nil}, {"generate": 0}, {"include": []string{"output_text"}}, {"include": []string{"reasoning.encrypted_content", "reasoning.encrypted_content"}},
		{"reasoning": map[string]any{"effort": "unknown"}}, {"reasoning": map[string]any{"summary": nil}}, {"reasoning": map[string]any{"future": true}},
		{"text": map[string]any{"verbosity": "unknown"}}, {"text": map[string]any{"format": map[string]any{"type": "json_schema"}}},
		{"text": map[string]any{"format": map[string]any{"type": "text", "future": true}}},
		{"prompt_cache_key": ""}, {"prompt_cache_key": strings.Repeat("a", 513)}, {"client_metadata": map[string]any{"key": false}},
		{"client_metadata": map[string]string{"": "v"}}, {"client_metadata": map[string]string{"key": strings.Repeat("v", 2049)}},
		{"input": []any{map[string]any{"type": "reasoning", "id": "bare"}}},
		{"input": []any{map[string]any{"type": "reasoning", "encrypted_content": "opaque", "summary": nil}}},
		{"input": []any{map[string]any{"type": "reasoning", "encrypted_content": "opaque", "summary": []any{map[string]any{"type": "summary_text", "text": 7}}}}},
	} {
		if _, err := Parse(requestFrame(t, extra)); err == nil {
			t.Fatal("unsupported native request accepted", extra)
		}
	}
	for _, n := range []int{128, 129} {
		if _, err := Parse(requestFrame(t, map[string]any{"client_metadata": map[string]string{strings.Repeat("k", n): "v"}})); (err == nil) != (n == 128) {
			t.Fatal("metadata key byte bound", n, err)
		}
	}
	for _, n := range []int{128 << 10, (128 << 10) + 1} {
		if err := validateReasoningItem(json.RawMessage(`{"type":"reasoning","encrypted_content":"`+strings.Repeat("a", n)+`"}`), true, true); (err == nil) != (n == 128<<10) {
			t.Fatal("opaque byte bound", n, err)
		}
	}
}

func TestNativeItemIDsPreserveClientToolResultAndRejectInvalidIdentity(t *testing.T) {
	for _, kind := range []string{"function_call_output", "custom_tool_call_output"} {
		for _, id := range []any{"result_one", strings.Repeat("i", 256), "", strings.Repeat("i", 257), "with space", "line\nfeed", nil, 7} {
			item := map[string]any{"type": kind, "id": id, "call_id": "call_one", "output": "unavailable"}
			parsed, err := Parse(requestFrame(t, map[string]any{"input": []any{item}}))
			value, ok := id.(string)
			valid := ok && storedItemID(value)
			if (err == nil) != valid {
				t.Fatal("client result identity bound", kind, id, err)
			}
			if valid && !bytes.Contains(parsed.Initial.Data, []byte(`"id":"`+value+`"`)) {
				t.Fatal("client result identity discarded", kind)
			}
		}
	}
}

func TestNativeReasoningEventsRemainOpaqueAndCannotForgeAnswerProgress(t *testing.T) {
	d := NewDecoder("m")
	if _, err := d.Decode(event(t, "response.created", "in_progress", nil)); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{
		`{"type":"response.output_item.added","item":{"type":"reasoning","id":"rs_one","summary":[],"encrypted_content":null}}`,
		`{"type":"response.reasoning_summary_part.added","part":{"type":"summary_text","text":""}}`,
		`{"type":"response.reasoning_summary_text.delta","delta":"雪🌏"}`,
		`{"type":"response.reasoning_summary_text.done","text":"雪🌏"}`,
		`{"type":"response.reasoning_summary_part.done","part":{"type":"summary_text","text":"雪🌏"}}`,
		`{"type":"response.output_item.done","item":{"type":"reasoning","id":"rs_one","summary":[{"type":"summary_text","text":"雪🌏"}],"encrypted_content":"opaque","future":true}}`,
	} {
		out, err := d.Decode(v2.Frame{Type: 1, Data: []byte(raw)})
		if err != nil || !bytes.Equal(out.Data, []byte(raw)) || out.ProtocolTerminal || out.Usage != nil {
			t.Fatal("reasoning generated terminal or usage", raw, err)
		}
	}
	for _, raw := range []string{
		`{"type":"response.reasoning_summary_text.delta","delta":null}`,
		`{"type":"response.reasoning_summary_text.done","text":{}}`,
		`{"type":"response.reasoning_summary_part.added","part":{"type":"input_image"}}`,
		`{"type":"response.output_item.done","item":{"type":"reasoning","encrypted_content":17}}`,
	} {
		bad := NewDecoder("m")
		bad.started = true
		if _, err := bad.Decode(v2.Frame{Type: 1, Data: []byte(raw)}); err == nil {
			t.Fatal("malformed reasoning event accepted", raw)
		}
	}
}
