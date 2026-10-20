package responsesws

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	v2 "minigateway/internal/foundation/contracts/execution/v2"
)

func TestSearchWSClosedFieldsStoredContextAndModelMapping(t *testing.T) {
	body := `{"type":"response.create","model":"alias","store":true,"tools":[{"type":"tool_search","execution":"client","parameters":{"type":"object"}}],"input":[{"type":"tool_search_output","execution":"client","call_id":"one","status":"completed","tools":[{"type":"function","name":"lookup","defer_loading":true,"parameters":{"type":"object"}}]},{"type":"additional_tools","role":"developer","tools":[]}]}`
	parsed, err := ParseStored(v2.Frame{Type: 1, Data: []byte(body)})
	if err != nil {
		t.Fatal(err)
	}
	parent := []json.RawMessage{json.RawMessage(`{"id":"tsc_one","type":"tool_search_call","execution":"client","call_id":"one","arguments":{"query":"雪🌏"},"future":true}`)}
	compiled, err := (StoredCodec{}).Compile(parsed.Initial.Data, parent, "resp_local")
	if err != nil {
		t.Fatal(err)
	}
	mapped, err := MapStoredModel(v2.Frame{Type: 1, Data: compiled.JSON}, "native")
	if err != nil || !bytes.Contains(mapped.Data, []byte(`"future":true`)) || !strings.Contains(compiled.PromptText, "雪🌏") || !strings.Contains(compiled.PromptText, "lookup") {
		t.Fatal("search full context changed", string(mapped.Data), err)
	}
	if _, err := Parse(v2.Frame{Type: 1, Data: []byte(strings.Replace(body, `"store":true,`, "", 1))}); err != nil {
		t.Fatal("stateless search output", err)
	}
	for _, bad := range []string{
		strings.Replace(body, `"parameters":{"type":"object"}`, `"parameters":null`, 1),
		strings.Replace(body, `"execution":"client"`, `"execution":"client","future":true`, 1),
		strings.Replace(body, `"defer_loading":true`, `"defer_loading":null`, 1),
		strings.Replace(body, `"defer_loading":true`, `"defer_loading":true,"future":true`, 1),
		strings.Replace(body, `"tools":[]`, `"tools":[{"type":"tool_search","execution":"client"}]`, 1),
	} {
		if _, err := ParseStored(v2.Frame{Type: 1, Data: []byte(bad)}); err == nil {
			t.Fatal("invalid search WS accepted", bad)
		}
	}
}

func TestSearchWSNativeEventsValidateClientModeWithoutRewriting(t *testing.T) {
	for _, item := range []string{
		`{"type":"tool_search_call","id":"tsc_one","execution":"client","call_id":"one","status":"in_progress","arguments":{}}`,
		`{"type":"tool_search_output","execution":"client","call_id":"one","status":"completed","tools":[]}`,
	} {
		for _, kind := range []string{"response.output_item.added", "response.output_item.done", "response.completed"} {
			event := `{"type":"` + kind + `","item":` + item + `}`
			if kind == "response.completed" {
				event = `{"type":"response.completed","response":{"id":"resp_one","object":"response","model":"m","status":"completed","output":[` + item + `],"usage":null}}`
			}
			d := NewDecoder("m")
			d.started = true
			out, err := d.Decode(v2.Frame{Type: 1, Data: []byte(event)})
			if err != nil || !bytes.Equal(out.Data, []byte(event)) {
				t.Fatal("native search changed", kind, err)
			}
			d = NewDecoder("m")
			d.started = true
			if _, err := d.Decode(v2.Frame{Type: 1, Data: []byte(strings.Replace(event, `"client"`, `"server"`, 1))}); err == nil {
				t.Fatal("hosted search accepted")
			}
		}
	}
}
