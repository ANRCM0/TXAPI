package responsesws

import (
	"bytes"
	"testing"

	v2 "minigateway/internal/foundation/contracts/execution/v2"
)

func TestIsTerminalRequiresValidNativeTerminalFrame(t *testing.T) {
	for _, status := range []string{"completed", "incomplete", "failed", "cancelled"} {
		if !IsTerminal(event(t, "response."+status, status, nil)) {
			t.Fatal("valid native terminal not classified", status)
		}
	}
	if !IsTerminal(v2.Frame{Type: 1, Data: []byte(`{"type":"error","error":{"code":"upstream_error","message":"Upstream generation failed."}}`)}) {
		t.Fatal("validated native error not classified")
	}
	for _, frame := range []v2.Frame{
		event(t, "response.created", "in_progress", nil),
		event(t, "response.completed", "in_progress", nil),
		event(t, "response.completed", "completed", map[string]any{"input_tokens": -1}),
		{Type: 2, Data: event(t, "response.completed", "completed", nil).Data},
		{Type: 1, Data: bytes.Repeat([]byte(" "), (256<<10)+1)},
		{Type: 1, Data: []byte(`{"type":"response.completed"}`)},
		{Type: 1, Data: []byte(`{"type":"response.completed","response":null}`)},
		{Type: 1, Data: []byte(`{"type":"response.completed","response":{"id":"r1","object":"response","status":"completed"}}`)},
		{Type: 1, Data: []byte(`{"type":"response.completed","response":{"id":"r1","object":"response","model":"m","status":"completed"},"stream_id":"other"}`)},
		{Type: 1, Data: []byte(`{"type":"response.completed","type":"response.failed","response":{"id":"r1","object":"response","model":"m","status":"failed"}}`)},
		{Type: 1, Data: []byte(`{"type":"response.completed","response":{"id":"r1","object":"response","model":"m","status":"completed","output":[{"type":"message","content":[{"type":"image"}]}]}}`)},
		{Type: 1, Data: []byte(`{"type":"error"}`)},
		{Type: 1, Data: []byte(`{"type":"error","error":null}`)},
		{Type: 1, Data: []byte(`{"type":"error","error":{"code":1,"message":"invalid"}}`)},
		{Type: 1, Data: []byte(`{"type":"error","error":{"code":"upstream_error","message":""}}`)},
		{Type: 1, Data: []byte(`{"type":"error","error":{"code":"upstream_error","message":"invalid lane"},"stream_id":"other"}`)},
	} {
		if IsTerminal(frame) {
			t.Errorf("invalid/nonterminal frame granted a boundary: %q", frame.Data)
		}
	}
}
