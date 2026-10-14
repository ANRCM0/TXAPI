package anthropic

import (
	"testing"

	"minigateway/internal/protocol/adaptor"
)

func TestStreamSessionsKeepMessageStateSeparate(t *testing.T) {
	sharedAdaptor := &Adaptor{}
	first, second := sharedAdaptor.NewStreamSession(), sharedAdaptor.NewStreamSession()

	for _, tc := range []struct {
		session adaptor.StreamSession
		id      string
		model   string
	}{{first, "msg-first", "claude-first"}, {second, "msg-second", "claude-second"}} {
		line := []byte(`data: {"type":"message_start","message":{"id":"` + tc.id + `","model":"` + tc.model + `"}}`)
		chunks, err := tc.session.ParseStreamLine(line)
		if err != nil || len(chunks) != 1 {
			t.Fatalf("message_start %s: chunks=%d err=%v", tc.id, len(chunks), err)
		}
		if chunks[0].ID != tc.id || chunks[0].Model != tc.model {
			t.Fatalf("session start state = (%s, %s), want (%s, %s)", chunks[0].ID, chunks[0].Model, tc.id, tc.model)
		}
	}

	for _, tc := range []struct {
		session adaptor.StreamSession
		id      string
		model   string
		text    string
	}{{first, "msg-first", "claude-first", "alpha"}, {second, "msg-second", "claude-second", "beta"}} {
		line := []byte(`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"` + tc.text + `"}}`)
		chunks, err := tc.session.ParseStreamLine(line)
		if err != nil || len(chunks) != 1 {
			t.Fatalf("text delta %s: chunks=%d err=%v", tc.id, len(chunks), err)
		}
		if chunks[0].ID != tc.id || chunks[0].Model != tc.model || chunks[0].Content[0].Text != tc.text {
			t.Fatalf("cross-session state: got %+v, want id=%s model=%s text=%s", chunks[0], tc.id, tc.model, tc.text)
		}
	}
}
