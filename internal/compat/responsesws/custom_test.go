package responsesws

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	v2 "minigateway/internal/foundation/contracts/execution/v2"
)

func TestCustomToolWSClosedDeclarationsAndStoredContext(t *testing.T) {
	body := `{"type":"response.create","model":"alias","store":true,"tools":[{"type":"namespace","name":"text","tools":[{"type":"custom","name":"edit","format":{"type":"grammar","syntax":"lark","definition":"start: /.+/"}}]}],"tool_choice":{"type":"custom","name":"edit","namespace":"text"},"input":[{"type":"custom_tool_call_output","call_id":"one","name":"edit","namespace":"text","output":"result"}]}`
	parsed, err := ParseStored(v2.Frame{Type: 1, Data: []byte(body)})
	if err != nil {
		t.Fatal(err)
	}
	parent := []json.RawMessage{json.RawMessage(`{"type":"custom_tool_call","id":"ctc","call_id":"one","name":"edit","namespace":"text","input":"raw 雪🌏\\nsecond","future":true}`)}
	compiled, err := (StoredCodec{}).Compile(parsed.Initial.Data, parent, "resp_local")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(compiled.PromptText, "raw 雪🌏") || !strings.Contains(compiled.PromptText, "result") {
		t.Fatal("custom history text estimate lost")
	}
	mapped, err := MapStoredModel(v2.Frame{Type: 1, Data: compiled.JSON}, "native")
	if err != nil {
		t.Fatal(err)
	}
	var fields struct {
		Input []map[string]json.RawMessage
		Model string
	}
	_ = json.Unmarshal(mapped.Data, &fields)
	if fields.Model != "native" || len(fields.Input) != 2 || string(fields.Input[0]["namespace"]) != `"text"` || string(fields.Input[1]["namespace"]) != `"text"` || string(fields.Input[0]["future"]) != "true" {
		t.Fatalf("custom history modified %s", mapped.Data)
	}
	stateless := strings.Replace(body, `"store":true,`, "", 1)
	if _, err := Parse(v2.Frame{Type: 1, Data: []byte(stateless)}); err != nil {
		t.Fatal("connection-local custom result rejected", err)
	}
	for _, bad := range []string{
		strings.Replace(body, `"format":{"type":"grammar","syntax":"lark","definition":"start: /.+/"}`, `"format":null`, 1),
		strings.Replace(body, `"syntax":"lark"`, `"syntax":"invalid"`, 1),
		strings.Replace(body, `"definition":"start: /.+/"`, `"definition":"start: /.+/","future":true`, 1),
		strings.Replace(body, `"namespace":"text","output"`, `"namespace":null,"output"`, 1),
		strings.Replace(body, `"name":"edit","namespace":"text","output"`, `"name":null,"namespace":"text","output"`, 1),
	} {
		if _, err := ParseStored(v2.Frame{Type: 1, Data: []byte(bad)}); err == nil {
			t.Fatalf("invalid custom WS accepted %s", bad)
		}
	}
}

func TestCustomToolWSTypedEventsAndInvalidFields(t *testing.T) {
	for _, test := range []struct {
		data  string
		valid bool
	}{
		{`{"type":"response.output_item.added","item":{"type":"custom_tool_call","id":"ctc","call_id":"a","name":"edit","input":"","namespace":"text"}}`, true},
		{`{"type":"response.custom_tool_call_input.delta","delta":"雪🌏"}`, true},
		{`{"type":"response.custom_tool_call_input.done","input":"雪🌏"}`, true},
		{`{"type":"response.custom_tool_call_input.delta","delta":null}`, false},
		{`{"type":"response.custom_tool_call_input.done","input":false}`, false},
		{`{"type":"response.output_item.done","item":{"type":"custom_tool_call","call_id":"a","name":"edit","input":42}}`, false},
		{`{"type":"response.completed","response":{"id":"r","object":"response","model":"m","status":"completed","output":[{"type":"custom_tool_call","call_id":"a","name":"edit","input":"raw 雪🌏","namespace":"text"}],"usage":null}}`, true},
	} {
		d := NewDecoder("m")
		d.started = true
		out, err := d.Decode(v2.Frame{Type: 1, Data: []byte(test.data)})
		if (err == nil) != test.valid {
			t.Fatalf("custom WS %s: %v", test.data, err)
		}
		if test.valid && !bytes.Equal(out.Data, []byte(test.data)) {
			t.Fatal("custom WS raw bytes changed")
		}
	}
}
