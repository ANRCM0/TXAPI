package responsesws

import (
	"bytes"
	"encoding/json"
	v2 "minigateway/internal/foundation/contracts/execution/v2"
	"strings"
	"testing"
)

func TestFunctionNamespaceWSValidationAndStoredContext(t *testing.T) {
	tools := `[{"type":"namespace","name":"weather","description":"snow","tools":[{"type":"function","name":"lookup","parameters":{"type":"object"}}]}]`
	body := `{"type":"response.create","model":"alias","tools":` + tools + `,"tool_choice":{"type":"function","name":"lookup","namespace":"weather"},"input":[{"type":"function_call_output","name":"lookup","namespace":"weather","call_id":"one","output":"result"}]}`
	parsed, err := Parse(v2.Frame{Type: 1, Data: []byte(body)})
	if err != nil {
		t.Fatal(err)
	}
	mapped, err := MapModel(parsed.Initial, "native")
	if err != nil || !bytes.Contains(mapped.Data, []byte(`"namespace":"weather"`)) {
		t.Fatal("namespace map", err)
	}
	for _, bad := range []string{
		strings.Replace(body, `"namespace":"weather"`, `"namespace":null`, 1),
		strings.Replace(body, `"description":"snow"`, `"description":null`, 1),
		strings.Replace(body, `"parameters":{"type":"object"}`, `"parameters":{"type":"object"},"future":true`, 1),
		strings.Replace(body, `"namespace":"weather"`, `"namespace":1`, 1),
	} {
		if _, err := Parse(v2.Frame{Type: 1, Data: []byte(bad)}); err == nil {
			t.Fatalf("bad namespace accepted %s", bad)
		}
	}
	stored := strings.Replace(body, `"model":"alias"`, `"model":"alias","store":true`, 1)
	invalidInput := strings.Replace(stored, `"namespace":"weather","call_id"`, `"namespace":null,"call_id"`, 1)
	if _, err := ParseStored(v2.Frame{Type: 1, Data: []byte(invalidInput)}); err == nil {
		t.Fatal("stored WS accepted explicit null namespace")
	}
	parent := []json.RawMessage{json.RawMessage(`{"type":"function_call","id":"fc_one","call_id":"one","namespace":"weather","name":"lookup","arguments":"{}","future":{"keep":true}}`)}
	compiled, err := (StoredCodec{}).Compile(json.RawMessage(stored), parent, "resp_local")
	if err != nil {
		t.Fatal(err)
	}
	var fields struct{ Input []map[string]json.RawMessage }
	_ = json.Unmarshal(compiled.JSON, &fields)
	if len(fields.Input) != 2 || string(fields.Input[0]["namespace"]) != `"weather"` || string(fields.Input[1]["namespace"]) != `"weather"` || string(fields.Input[0]["future"]) != `{"keep":true}` {
		t.Fatalf("namespace history lost %s", compiled.JSON)
	}
	if _, err = MapStoredModel(v2.Frame{Type: 1, Data: compiled.JSON}, "native"); err != nil {
		t.Fatal(err)
	}
}

func TestFunctionNamespaceWSOutputTypeValidation(t *testing.T) {
	for _, namespace := range []string{`"weather"`, `null`, `42`, `""`} {
		valid := namespace == `"weather"`
		for _, kind := range []string{"response.output_item.added", "response.output_item.done", "response.completed"} {
			item := `{"type":"function_call","id":"fc","call_id":"call","name":"lookup","namespace":` + namespace + `,"arguments":"{}"}`
			data := `{"type":"` + kind + `","item":` + item + `}`
			if kind == "response.completed" {
				data = `{"type":"response.completed","response":{"id":"r","object":"response","model":"m","status":"completed","output":[` + item + `],"usage":null}}`
			}
			d := NewDecoder("m")
			d.started = true
			out, err := d.Decode(v2.Frame{Type: 1, Data: []byte(data)})
			if (err == nil) != valid {
				t.Fatalf("%s %s: %v", kind, namespace, err)
			}
			if valid && !bytes.Contains(out.Data, []byte(`"namespace":"weather"`)) {
				t.Fatal("namespace stripped")
			}
		}
	}
}
