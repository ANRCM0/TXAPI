package responses

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	execv1 "minigateway/internal/foundation/contracts/execution/v1"
)

func namespaceRequest(tools, choice, input string) []byte {
	if choice == "" {
		choice = `"auto"`
	}
	if input == "" {
		input = `"hi"`
	}
	return []byte(`{"model":"public","input":` + input + `,"tools":` + tools + `,"tool_choice":` + choice + `,"future":{"retained":true}}`)
}

func TestFunctionNamespaceDeclarationsAndHistoryRetainRawFields(t *testing.T) {
	tools := `[{"type":"namespace","name":"weather","description":"雪🌏","tools":[{"type":"function","name":"lookup","parameters":{"type":"object"},"future":{"keep":1}}],"future":true},{"type":"namespace","name":"travel","tools":[{"type":"function","name":"lookup"}]},{"type":"function","name":"lookup"}]`
	input := `[{"type":"function_call","call_id":"one","namespace":"weather","name":"lookup","arguments":"{}","future":"雪"},{"type":"function_call_output","call_id":"one","namespace":"weather","name":"lookup","output":"result"}]`
	body := namespaceRequest(tools, `{"type":"function","name":"lookup","namespace":"travel"}`, input)
	r, err := ParseRequest(body)
	if err != nil || r.ValidateHTTP() != nil {
		t.Fatal("namespace rejected", err, r.ValidateHTTP())
	}
	wire, err := r.WithModel("actual")
	if err != nil {
		t.Fatal(err)
	}
	var before, after map[string]json.RawMessage
	_ = json.Unmarshal(body, &before)
	_ = json.Unmarshal(wire, &after)
	after["model"] = before["model"]
	a, _ := json.Marshal(before)
	b, _ := json.Marshal(after)
	if !bytes.Equal(a, b) {
		t.Fatalf("namespace or unknown fields changed: %s", wire)
	}
	if !strings.Contains(r.PromptText(), "weather") || !strings.Contains(r.PromptText(), "result") {
		t.Fatal("namespace absent from prompt estimate")
	}
	background := bytes.Replace(body, []byte(`"model":"public"`), []byte(`"model":"public","background":true`), 1)
	parsed, err := (BackgroundParser{}).ParseRequest(execv1.Responses, background)
	if err != nil || !bytes.Equal(parsed.Body(), background) {
		t.Fatal("background lost namespace", err)
	}
}

func TestFunctionNamespaceInvalidDeclarationsAndReferences(t *testing.T) {
	group := func(name, members string) string {
		return `{"type":"namespace","name":` + name + `,"tools":` + members + `}`
	}
	good := `[{"type":"function","name":"lookup"}]`
	groups := make([]string, 65)
	for i := range groups {
		groups[i] = group(fmt.Sprintf(`"group%d"`, i), good)
	}
	members := make([]string, 129)
	for i := range members {
		members[i] = fmt.Sprintf(`{"type":"function","name":"f%d"}`, i)
	}
	bad := []string{
		`[{"type":"computer","name":"x"}]`, `[{"type":"web_search"}]`,
		`[` + group(`""`, good) + `]`, `[` + group(`" x"`, good) + `]`, `[` + group(`"x\u0000"`, good) + `]`,
		`[` + group(fmt.Sprintf("%q", strings.Repeat("雪", 43)), good) + `]`,
		`[` + group(`"x"`, `null`) + `]`, `[` + group(`"x"`, `[]`) + `]`, `[` + group(`"x"`, `{}`) + `]`,
		`[` + group(`"x"`, `[{"type":"function","name":"f"},{"type":"function","name":"f"}]`) + `]`,
		`[` + group(`"x"`, `[{"type":"function","name":"f "}]`) + `]`,
		`[` + group(`"x"`, `[{"type":"web_search","name":"f"}]`) + `]`,
		`[` + group(`"x"`, `[{"type":"namespace","name":"nested","tools":`+good+`}]`) + `]`,
		`[` + group(`"x"`, good) + `,` + group(`"x"`, good) + `]`, `[` + strings.Join(groups, ",") + `]`,
		`[` + group(`"x"`, `[`+strings.Join(members, ",")+`]`) + `]`,
	}
	for i, tools := range bad {
		r, err := ParseRequest(namespaceRequest(tools, "", ""))
		if err == nil {
			err = r.ValidateHTTP()
		}
		if err == nil {
			t.Fatalf("invalid tools accepted %d: %s", i, tools)
		}
	}
	for _, raw := range []string{`1`, `true`, `[]`, `{}`, `""`, `" x"`, `"x\n"`, fmt.Sprintf("%q", strings.Repeat("a", 129))} {
		for _, typ := range []string{"function_call", "function_call_output", "tool_choice"} {
			input, choice := `"hi"`, `"auto"`
			if typ == "tool_choice" {
				choice = `{"type":"function","name":"f","namespace":` + raw + `}`
			} else {
				input = `[{"type":"` + typ + `","call_id":"a","name":"f","arguments":"{}","output":"x","namespace":` + raw + `}]`
			}
			r, err := ParseRequest(namespaceRequest(`[]`, choice, input))
			if err == nil {
				err = r.ValidateHTTP()
			}
			if err == nil {
				t.Fatalf("invalid namespace accepted %s %s", typ, raw)
			}
		}
	}
	for _, raw := range []string{`null`, `"weather"`} {
		r, err := ParseRequest(namespaceRequest(`[]`, `{"type":"function","name":"f","namespace":`+raw+`}`, `[{"type":"function_call","call_id":"a","name":"f","arguments":"{}","namespace":`+raw+`}]`))
		if err != nil || r.ValidateHTTP() != nil {
			t.Fatal("optional HTTP namespace", err)
		}
	}
}

func TestFunctionNamespaceOutputJSONAndCompleteSSEEvents(t *testing.T) {
	for _, ns := range []string{`"weather"`, `null`, `17`, `""`, `"bad\n"`} {
		valid := ns == `"weather"` || ns == `null`
		item := `{"type":"function_call","id":"fc_1","call_id":"call_1","namespace":` + ns + `,"name":"lookup","arguments":"{}","future":"雪🌏"}`
		body := []byte(`{"id":"resp_remote","object":"response","status":"completed","output":[` + item + `],"usage":null}`)
		out, _, err := NewDecoderWithID("resp_local").Decode(body)
		if (err == nil) != valid {
			t.Fatalf("namespace output %s: %v", ns, err)
		}
		if valid && (!bytes.Contains(out, []byte(`"namespace":`+ns)) || !bytes.Contains(out, []byte("雪🌏"))) {
			t.Fatalf("lost namespace %s", out)
		}
		for _, kind := range []string{"response.output_item.added", "response.output_item.done", "response.completed"} {
			event := `{"type":"` + kind + `","item":` + item + `}`
			if kind == "response.completed" {
				event = `{"type":"response.completed","response":` + string(body) + `}`
			}
			stream := NewDecoderWithID("resp_local").NewStream()
			if frames, e := stream.DecodeLine([]byte("data: " + event)); e != nil || len(frames) != 0 {
				t.Fatal("partial event emitted", e)
			}
			frames, e := stream.DecodeLine(nil)
			if (e == nil) != valid {
				t.Fatalf("%s namespace=%s error=%v", kind, ns, e)
			}
			if valid && (len(frames) != 1 || !bytes.Contains(frames[0].Data, []byte(`"namespace":`+ns))) {
				t.Fatalf("lost item %s %v", kind, frames)
			}
		}
	}
}

func TestFunctionNamespaceExactBoundsAndLegacyExtensions(t *testing.T) {
	groups := make([]map[string]any, 64)
	for i := range groups {
		groups[i] = map[string]any{"type": "namespace", "name": fmt.Sprint("group", i), "tools": []any{map[string]string{"type": "function", "name": "lookup"}}}
	}
	members := make([]map[string]string, 128)
	for i := range members {
		members[i] = map[string]string{"type": "function", "name": fmt.Sprint("member", i)}
	}
	groups[0]["tools"] = members
	groups[0]["name"] = strings.Repeat("雪", 42) + "aa" // exactly 128 UTF-8 bytes
	members[0]["name"] = strings.Repeat("a", 128)
	raw, _ := json.Marshal(groups)
	r, err := ParseRequest(namespaceRequest(string(raw), "", ""))
	if err != nil || r.ValidateHTTP() != nil {
		t.Fatal("exact namespace bounds rejected", err, r.ValidateHTTP())
	}
	// These extension fields on a top-level function predate namespace support.
	r, err = ParseRequest(namespaceRequest(`[{"type":"function","name":"legacy","tools":7,"future":true}]`, "", ""))
	if err != nil || r.ValidateHTTP() != nil {
		t.Fatal("legacy function extension tightened", err)
	}
}
