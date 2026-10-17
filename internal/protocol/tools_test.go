package protocol

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"minigateway/internal/core"
	"minigateway/internal/protocol/meta"
)

func TestFunctionCallsAndResultsRoundTrip(t *testing.T) {
	a, _ := Resolve("anthropic")
	m := &meta.Meta{ActualModel: "claude-test", Mode: meta.ModeChatCompletions}
	out, _, err := ConvertResponseBody(a, []byte(`{"id":"msg-1","model":"claude-test","content":[{"type":"tool_use","id":"first","name":"lookup","input":{"city":"one"}},{"type":"tool_use","id":"second","name":"lookup","input":{"city":"two"}}],"stop_reason":"tool_use","usage":{"input_tokens":3,"output_tokens":4}}`), m)
	if err != nil {
		t.Fatal(err)
	}
	var response struct {
		Choices []struct {
			Message json.RawMessage `json:"message"`
			Finish  string          `json:"finish_reason"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(out, &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Choices) != 1 || response.Choices[0].Finish != "tool_calls" {
		t.Fatalf("lost tool termination: %s", out)
	}
	body := []byte(`{"model":"m","max_tokens":100,"tools":[{"type":"function","function":{"name":"lookup","parameters":{"type":"object"}}}],"messages":[` + string(response.Choices[0].Message) + `,{"role":"tool","tool_call_id":"first","content":"result-one"},{"role":"tool","tool_call_id":"second","content":"result-two"}]}`)
	prepared, convertErr := Prepare(a, body, m)
	if convertErr != nil {
		t.Fatal(convertErr)
	}
	var wire struct {
		Messages []struct {
			Content []struct {
				Type, ID, Name, Content string
				ToolID                  string `json:"tool_use_id"`
			} `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(prepared.Body, &wire); err != nil {
		t.Fatal(err)
	}
	if len(wire.Messages) != 2 || len(wire.Messages[0].Content) != 2 || len(wire.Messages[1].Content) != 2 {
		t.Fatalf("lost parallel calls/results: %s", prepared.Body)
	}
	for i, id := range []string{"first", "second"} {
		if wire.Messages[0].Content[i].ID != id || wire.Messages[1].Content[i].ToolID != id || wire.Messages[1].Content[i].Content != []string{"result-one", "result-two"}[i] {
			t.Fatalf("lost association: %s", prepared.Body)
		}
	}
	bad := strings.Replace(string(body), `"tool_call_id":"first"`, `"tool_call_id":"unknown"`, 1)
	if _, err := Prepare(a, []byte(bad), m); err == nil {
		t.Fatal("unresolved tool result was guessed")
	}
}

func TestAnthropicInterleavedToolArgumentsAndTruncation(t *testing.T) {
	a, _ := Resolve("anthropic")
	lines := []string{
		`data: {"type":"message_start","message":{"id":"msg","model":"claude-test","usage":{"input_tokens":4}}}`,
		`data: {"type":"content_block_start","index":2,"content_block":{"type":"tool_use","id":"call-a","name":"lookup","input":{}}}`,
		`data: {"type":"content_block_start","index":5,"content_block":{"type":"tool_use","id":"call-b","name":"lookup","input":{}}}`,
		`data: {"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"{\"city\":"}}`,
		`data: {"type":"content_block_delta","index":5,"delta":{"type":"input_json_delta","partial_json":"{\"city\":\"two\"}"}}`,
		`data: {"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"\"\u676d\u5dde\"}"}}`,
		`data: {"type":"content_block_stop","index":2}`,
		`data: {"type":"content_block_stop","index":5}`,
		`data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":8}}`,
		`data: {"type":"message_stop"}`,
	}
	for _, truncated := range []bool{false, true} {
		payload := strings.Join(lines, "\n\n") + "\n\n"
		if truncated {
			payload = strings.Join(lines[:6], "\n\n") + "\n\n"
		}
		executor := core.New(core.Options{Client: &http.Client{Transport: staticResponse{body: strings.NewReader(payload)}}})
		rec := httptest.NewRecorder()
		result := executor.Execute(context.Background(), rec, core.Input{URL: "http://upstream.test", Stream: true, Decoder: NewDecoder(a, &meta.Meta{})})
		executor.Close()
		if truncated {
			if result.Error == nil || result.ProtocolComplete || strings.Contains(rec.Body.String(), "[DONE]") {
				t.Fatalf("truncated tools marked complete: %+v %s", result, rec.Body)
			}
			continue
		}
		if result.Error != nil || !result.ProtocolComplete || result.Usage == nil || result.Usage.TotalTokens != 12 {
			t.Fatalf("failed tool stream: %+v", result)
		}
		type call struct{ ID, Name, Arguments string }
		calls := map[int]*call{}
		for _, line := range strings.Split(rec.Body.String(), "\n") {
			if !strings.HasPrefix(line, "data: {") {
				continue
			}
			var chunk struct {
				Choices []struct {
					Delta struct {
						Calls []struct {
							Index    int
							ID       string
							Function struct{ Name, Arguments string }
						} `json:"tool_calls"`
					} `json:"delta"`
				} `json:"choices"`
			}
			if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &chunk); err != nil {
				t.Fatal(err)
			}
			for _, choice := range chunk.Choices {
				for _, delta := range choice.Delta.Calls {
					c := calls[delta.Index]
					if c == nil {
						c = &call{}
						calls[delta.Index] = c
					}
					c.ID += delta.ID
					c.Name += delta.Function.Name
					c.Arguments += delta.Function.Arguments
				}
			}
		}
		if len(calls) != 2 || calls[0].ID != "call-a" || calls[1].ID != "call-b" || calls[0].Arguments != "{\"city\":\"\u676d\u5dde\"}" || calls[1].Arguments != `{"city":"two"}` {
			t.Fatalf("interleaved tool corruption: %+v; %s", calls, rec.Body)
		}
	}
	decoder := NewDecoder(a, &meta.Meta{}).NewStream()
	if _, err := decoder.DecodeLine([]byte(lines[3])); err == nil {
		t.Fatal("orphan tool delta accepted")
	}
}

func TestCrossProtocolRejectsUnrepresentableFields(t *testing.T) {
	for _, target := range []string{"anthropic", "gemini"} {
		a, _ := Resolve(target)
		for _, extra := range []string{`"seed":1`, `"max_completion_tokens":4`, `"tool_choice":"required"`, `"parallel_tool_calls":false`, `"n":2`, `"response_format":{"type":"json_object"}`} {
			body := []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}],` + extra + `}`)
			if _, err := Prepare(a, body, &meta.Meta{ActualModel: "m"}); err == nil {
				t.Fatalf("%s silently accepted %s", target, extra)
			}
			native, _ := Resolve("openai")
			out, err := Prepare(native, body, &meta.Meta{ActualModel: "m", Mode: meta.ModeChatCompletions})
			var before, after any
			_ = json.Unmarshal(body, &before)
			_ = json.Unmarshal(out.Body, &after)
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatalf("native fast path changed: %s %v", out.Body, err)
			}
		}
	}
}

func TestCapabilitiesRejectUnsupportedOperationTransportAndHiddenTools(t *testing.T) {
	for _, name := range []string{"openai", "anthropic", "gemini"} {
		a, _ := Resolve(name)
		if !Supports(a, Requirements{Operation: Chat, Transport: HTTP}) {
			t.Fatal(name)
		}
		if Supports(a, Requirements{Operation: Responses}) || Supports(a, Requirements{Operation: Chat, Transport: WebSocket}) || Supports(a, Requirements{Operation: Chat, Source: "anthropic"}) {
			t.Fatalf("invented capability: %s", name)
		}
	}
	r := RequirementsFor(Chat, []byte(`{"messages":[{"role":"tool","content":"result","tool_call_id":"a"}]}`))
	a, _ := Resolve("gemini")
	if !r.Tools || Supports(a, r) {
		t.Fatal("tool history bypassed capability filtering")
	}
}
