package anthropic

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"minigateway/internal/protocol/canonical"
)

func TestStreamPreservesInitialContentAndEmptyToolArguments(t *testing.T) {
	session := newTranslator()
	events := []string{
		`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":"hello"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":" world"}}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"call-initial","name":"lookup","input":{"city":"\u676d\u5dde"}}}`,
		`{"type":"content_block_start","index":2,"content_block":{"type":"tool_use","id":"call-empty","name":"clock","input":{}}}`,
		`{"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":""}}`,
		`{"type":"content_block_stop","index":2}`,
		`{"type":"content_block_stop","index":1}`,
		`{"type":"message_delta","delta":{"stop_reason":"tool_use"}}`,
	}
	var text strings.Builder
	arguments := map[int]string{}
	ids := map[int]string{}
	finishes := 0
	for _, event := range events {
		chunks, err := session.ParseStreamLine([]byte("data: " + event))
		if err != nil {
			t.Fatal(err)
		}
		for _, chunk := range chunks {
			for _, part := range chunk.Content {
				text.WriteString(part.Text)
			}
			for _, call := range chunk.ToolCalls {
				arguments[call.Index] += call.Arguments
				ids[call.Index] += call.ID
			}
			if chunk.FinishReason == canonical.FinishToolCalls {
				finishes++
			}
		}
	}
	if text.String() != "hello world" || ids[0] != "call-initial" || ids[1] != "call-empty" || arguments[0] != `{"city":"\u676d\u5dde"}` || arguments[1] != "{}" || finishes != 1 {
		t.Fatalf("initial/empty content changed: text=%q ids=%v arguments=%v finishes=%d", text.String(), ids, arguments, finishes)
	}
}

func TestStreamRejectsInvalidBlockTransitions(t *testing.T) {
	textStart := `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`
	toolStart := `{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"call","name":"f","input":{}}}`
	stop := `{"type":"content_block_stop","index":0}`
	textDelta := `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"later"}}`
	toolDelta := `{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{}"}}`
	for _, tc := range []struct {
		name   string
		prefix []string
		bad    string
	}{
		{name: "orphan text", bad: textDelta},
		{name: "orphan tool", bad: toolDelta},
		{name: "orphan stop", bad: stop},
		{name: "text delta after stop", prefix: []string{textStart, stop}, bad: textDelta},
		{name: "tool delta after stop", prefix: []string{toolStart, stop}, bad: toolDelta},
		{name: "duplicate stop", prefix: []string{toolStart, stop}, bad: stop},
		{name: "duplicate index", prefix: []string{textStart, stop}, bad: toolStart},
		{name: "duplicate tool id", prefix: []string{toolStart, stop}, bad: `{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"call","name":"g","input":{}}}`},
		{name: "mixed initial and delta", prefix: []string{`{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"call","name":"f","input":{"a":1}}}`}, bad: toolDelta},
		{name: "text delta in tool", prefix: []string{toolStart}, bad: textDelta},
		{name: "tool delta in text", prefix: []string{textStart}, bad: toolDelta},
		{name: "negative index", bad: `{"type":"content_block_start","index":-1,"content_block":{"type":"text","text":""}}`},
		{name: "invalid initial input", bad: `{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"call","name":"f","input":[]}}`},
		{name: "null initial input", bad: `{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"call","name":"f","input":null}}`},
		{name: "finish with open block", prefix: []string{toolStart}, bad: `{"type":"message_delta","delta":{"stop_reason":"tool_use"}}`},
		{name: "block after finish", prefix: []string{textStart, stop, `{"type":"message_delta","delta":{"stop_reason":"end_turn"}}`}, bad: toolStart},
	} {
		t.Run(tc.name, func(t *testing.T) {
			session := newTranslator()
			for _, event := range tc.prefix {
				if _, err := session.ParseStreamLine([]byte("data: " + event)); err != nil {
					t.Fatalf("invalid fixture: %v", err)
				}
			}
			if _, err := session.ParseStreamLine([]byte("data: " + tc.bad)); err == nil {
				t.Fatal("invalid transition was accepted")
			}
		})
	}
}

func TestStreamBlockAndIdentityStateIsBounded(t *testing.T) {
	session := newTranslator()
	for index := 0; index < maxStreamBlocks; index++ {
		for _, event := range []string{
			fmt.Sprintf(`data: {"type":"content_block_start","index":%d,"content_block":{"type":"text","text":""}}`, index),
			fmt.Sprintf(`data: {"type":"content_block_stop","index":%d}`, index),
		} {
			if _, err := session.ParseStreamLine([]byte(event)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, err := session.ParseStreamLine([]byte(fmt.Sprintf(`data: {"type":"content_block_start","index":%d,"content_block":{"type":"text","text":""}}`, maxStreamBlocks))); err == nil || len(session.blocks) != maxStreamBlocks {
		t.Fatal("closed blocks bypassed the total block bound")
	}

	session = newTranslator()
	for index, id := range []string{strings.Repeat("a", maxToolIDBytes/2), strings.Repeat("b", maxToolIDBytes/2), "c"} {
		encoded, err := json.Marshal(id)
		if err != nil {
			t.Fatal(err)
		}
		_, err = session.ParseStreamLine([]byte(fmt.Sprintf(`data: {"type":"content_block_start","index":%d,"content_block":{"type":"tool_use","id":%s,"name":"f","input":{}}}`, index, encoded)))
		if index == 2 {
			if err == nil || session.toolIDBytes != maxToolIDBytes || len(session.blocks) != 2 {
				t.Fatal("closed tool identities bypassed their byte bound")
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		if _, err := session.ParseStreamLine([]byte(fmt.Sprintf(`data: {"type":"content_block_stop","index":%d}`, index))); err != nil {
			t.Fatal(err)
		}
	}
}
