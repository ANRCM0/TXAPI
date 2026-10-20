package responsesws

import (
	"bytes"
	"encoding/json"
	"testing"

	v2 "minigateway/internal/foundation/contracts/execution/v2"
)

func requestFrame(t *testing.T, extra map[string]any) v2.Frame {
	t.Helper()
	fields := map[string]any{"type": "response.create", "model": "m", "input": "hello"}
	for k, v := range extra {
		fields[k] = v
	}
	body, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	return v2.Frame{Type: 1, Data: body}
}
func TestParseNormalizesExplicitStatelessMainLane(t *testing.T) {
	r, err := Parse(requestFrame(t, nil))
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	_ = json.Unmarshal(r.Initial.Data, &fields)
	if r.Model != "m" || fields["store"] != false || fields["stream_id"] != "main" {
		t.Fatalf("unsafe defaults: %s", r.Initial.Data)
	}
	tools := []any{map[string]any{"type": "function", "name": "f", "parameters": map[string]any{"type": "object"}, "strict": true}}
	for _, input := range []any{[]any{map[string]any{"role": "user", "content": []any{map[string]any{"type": "input_text", "text": "hi"}}}}, []any{map[string]any{"type": "function_call_output", "call_id": "c1", "output": "ok"}}} {
		if _, err := Parse(requestFrame(t, map[string]any{"input": input, "tools": tools})); err != nil {
			t.Fatal(err)
		}
	}
}
func TestParseFailsClosedBeforeDispatch(t *testing.T) {
	for _, extra := range []map[string]any{
		{"store": true}, {"store": nil}, {"stream_id": "other"}, {"stream_id": nil}, {"generate": "false"}, {"generate": nil}, {"background": false}, {"stream": false}, {"unknown": 1}, {"model": ""}, {"input": nil},
		{"input": []any{map[string]any{"type": "item_reference", "id": "foreign"}}}, {"input": []any{map[string]any{"role": "user", "content": []any{map[string]any{"type": "input_image", "image_url": "x"}}}}},
		{"input": []any{map[string]any{"role": "user", "content": "hi", "hidden": true}}}, {"tools": []any{map[string]any{"type": "web_search"}}}, {"tools": []any{map[string]any{"type": "function", "name": "f", "hidden": true}}}, {"temperature": 3}, {"top_p": -1}, {"parallel_tool_calls": "true"},
	} {
		if _, err := Parse(requestFrame(t, extra)); err == nil {
			t.Errorf("accepted unsupported request: %#v", extra)
		}
	}
	for _, data := range [][]byte{[]byte(`{"type":"response.create","model":"m","model":"n","input":"hi"}`), append(requestFrame(t, nil).Data, []byte(" {}")...), {0xff}} {
		if _, err := Parse(v2.Frame{Type: 1, Data: data}); err == nil {
			t.Errorf("accepted invalid JSON: %q", data)
		}
	}
	if _, err := Parse(v2.Frame{Type: 2, Data: requestFrame(t, nil).Data}); err == nil {
		t.Fatal("accepted binary")
	}
	if _, err := Parse(v2.Frame{Type: 1, Data: bytes.Repeat([]byte(" "), (256<<10)+1)}); err == nil {
		t.Fatal("accepted large message")
	}
}
func event(t *testing.T, typ string, status string, usage any) v2.Frame {
	t.Helper()
	root := map[string]any{"type": typ, "stream_id": "main", "response": map[string]any{"id": "r1", "object": "response", "model": "m", "status": status, "output": []any{}, "usage": usage}}
	data, _ := json.Marshal(root)
	return v2.Frame{Type: 1, Data: data}
}
func TestDecoderOneMessageOneDeliveryAndCumulativeFacts(t *testing.T) {
	d := NewDecoder("m")
	created := event(t, "response.created", "in_progress", nil)
	first, err := d.Decode(created)
	if err != nil {
		t.Fatal(err)
	}
	if first.ProtocolTerminal || !bytes.Equal(first.Data, created.Data) {
		t.Fatal("created event duplicated or changed")
	}
	last := event(t, "response.completed", "completed", map[string]any{"input_tokens": 2, "output_tokens": 1, "total_tokens": 3})
	out, err := d.Decode(last)
	if err != nil {
		t.Fatal(err)
	}
	if !out.ProtocolTerminal || out.Usage == nil || out.Usage.Completeness != v2.Complete || *out.Usage.Counts.TotalTokens != 3 || !bytes.Equal(out.Data, last.Data) {
		t.Fatalf("facts altered: %+v", out)
	}
	if _, err := d.Decode(last); err == nil {
		t.Fatal("duplicate terminal accepted")
	}
}
func TestDecoderRejectsUnknownLaneModelIdentityAndInvalidUsage(t *testing.T) {
	for _, frame := range []v2.Frame{event(t, "response.unknown", "in_progress", nil), event(t, "response.completed", "in_progress", nil), event(t, "response.completed", "completed", map[string]any{"input_tokens": -1})} {
		if _, err := NewDecoder("m").Decode(frame); err == nil {
			t.Fatal("invalid event accepted")
		}
	}
	base := event(t, "response.created", "in_progress", nil)
	for _, change := range []func(map[string]any){func(m map[string]any) { m["stream_id"] = "other" }, func(m map[string]any) { m["stream_id"] = nil }, func(m map[string]any) { m["response"].(map[string]any)["model"] = "other" }} {
		var m map[string]any
		_ = json.Unmarshal(base.Data, &m)
		change(m)
		body, _ := json.Marshal(m)
		if _, err := NewDecoder("m").Decode(v2.Frame{Type: 1, Data: body}); err == nil {
			t.Fatal("invalid binding accepted")
		}
	}
	d := NewDecoder("m")
	if _, err := d.Decode(base); err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	last := event(t, "response.completed", "completed", nil)
	_ = json.Unmarshal(last.Data, &m)
	m["response"].(map[string]any)["id"] = "r2"
	data, _ := json.Marshal(m)
	if _, err := d.Decode(v2.Frame{Type: 1, Data: data}); err == nil {
		t.Fatal("response identity changed")
	}
}
