package responsesws

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	v2 "minigateway/internal/foundation/contracts/execution/v2"
	history "minigateway/internal/foundation/contracts/responsehistory/v1"
)

func TestStoredHistoryCompilePreservesCompleteFunctionContext(t *testing.T) {
	codec := StoredCodec{}
	request := json.RawMessage(`{"type":"response.create","model":"alias","store":true,"previous_response_id":"resp_parent","instructions":"current-instructions","input":[{"type":"function_call_output","call_id":"call_1","output":"tool-result-雪"}]}`)
	if _, err := Parse(v2.Frame{Type: 1, Data: request}); err == nil {
		t.Fatal("default path accepted storage")
	}
	parsed, err := ParseStored(v2.Frame{Type: 1, Data: request})
	if err != nil || !parsed.Store {
		t.Fatal("persistent parse failed", err)
	}
	parent := []json.RawMessage{
		json.RawMessage(`{"id":"item_input","type":"message","role":"user","content":[{"type":"input_text","text":"old-question"}]}`),
		json.RawMessage(`{"id":"fc_1","type":"function_call","call_id":"call_1","name":"lookup","arguments":"{\"name\":\"雪\"}","status":"completed"}`),
	}
	compiled, err := codec.Compile(parsed.Initial.Data, parent, "resp_local")
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"old-question", "lookup", "tool-result-雪", "current-instructions"} {
		if !strings.Contains(compiled.PromptText, value) {
			t.Fatalf("full TPM input lost %q: %s", value, compiled.PromptText)
		}
	}
	wire, err := MapStoredModel(v2.Frame{Type: 1, Data: compiled.JSON}, "actual")
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Model    string
		Store    bool
		Previous json.RawMessage `json:"previous_response_id"`
		Input    []map[string]json.RawMessage
	}
	if json.Unmarshal(wire.Data, &decoded) != nil || decoded.Model != "actual" || decoded.Store || len(decoded.Previous) != 0 || len(decoded.Input) != 3 {
		t.Fatalf("wrong wire context: %s", wire.Data)
	}
	if _, id := decoded.Input[0]["id"]; id {
		t.Fatal("local message identity was sent upstream")
	}
	if string(decoded.Input[1]["call_id"]) != `"call_1"` || string(decoded.Input[1]["name"]) != `"lookup"` || string(decoded.Input[2]["call_id"]) != `"call_1"` {
		t.Fatalf("function association lost: %s", wire.Data)
	}
	if !bytes.Equal(parent[0], json.RawMessage(`{"id":"item_input","type":"message","role":"user","content":[{"type":"input_text","text":"old-question"}]}`)) {
		t.Fatal("compile mutated parent content")
	}
	// Stored and connection-local branches do not infer each other's context.
	if _, err := MapModel(v2.Frame{Type: 1, Data: compiled.JSON}, "actual"); err == nil {
		t.Fatal("stateless codec accepted historical function input")
	}
	if _, err := MapStoredModel(v2.Frame{Type: 1, Data: parsed.Initial.Data}, "actual"); err == nil {
		t.Fatal("full context also retained native parent")
	}
}

func TestStoredHistoryContextLimitsAndTerminalProjection(t *testing.T) {
	codec := StoredCodec{}
	raw := json.RawMessage(`{"type":"response.create","model":"alias","store":true,"input":[]}`)
	if compiled, err := codec.Compile(raw, nil, "resp_empty"); err != nil || compiled.Input == nil {
		t.Fatal("known empty input is valid", err)
	}
	for name, inherited := range map[string][]json.RawMessage{
		"missing-id":       {json.RawMessage(`{"type":"message","role":"user","content":"x"}`)},
		"control-id":       {json.RawMessage(`{"id":"bad\u0000id","type":"message","role":"user","content":"x"}`)},
		"reference":        {json.RawMessage(`{"id":"ref","type":"item_reference"}`)},
		"opaque-reasoning": {json.RawMessage(`{"id":"reason","type":"reasoning","encrypted_content":"opaque"}`)},
		"duplicate":        {json.RawMessage(`{"id":"same","type":"message","role":"user","content":"x"}`), json.RawMessage(`{"id":"same","type":"message","role":"user","content":"y"}`)},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := codec.Compile(raw, inherited, "resp_new"); err == nil {
				t.Fatal("unreconstructable context accepted")
			}
		})
	}
	large, _ := json.Marshal(strings.Repeat("x", history.MaxContextBytes))
	if _, err := codec.Compile(json.RawMessage(`{"type":"response.create","model":"alias","store":true,"input":`+string(large)+`}`), nil, "resp_big"); err == nil {
		t.Fatal("large context accepted")
	}
	for _, status := range []string{"completed", "failed", "incomplete", "cancelled"} {
		t.Run(status, func(t *testing.T) {
			data := json.RawMessage(`{"type":"response.` + status + `","response_id":"private-native","response":{"id":"private-native","object":"response","model":"actual","status":"` + status + `","output":[],"store":false,"previous_response_id":"private-parent"}}`)
			event, err := codec.Project(data, "resp_local", "resp_parent", "alias")
			if err != nil || !event.Terminal || event.Output == nil || bytes.Contains(event.JSON, []byte("private")) {
				t.Fatal("terminal projection failed", string(event.JSON), err)
			}
			var doc struct {
				ID, Model, Status string
				Store             bool
				Previous          string `json:"previous_response_id"`
			}
			if json.Unmarshal(event.Response, &doc) != nil || doc.ID != "resp_local" || doc.Model != "alias" || doc.Status != status || !doc.Store || doc.Previous != "resp_parent" {
				t.Fatalf("terminal semantics changed: %s", event.Response)
			}
		})
	}
}
