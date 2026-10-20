package responses

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	execv1 "minigateway/internal/foundation/contracts/execution/v1"
)

const searchDeclaration = `{"type":"tool_search","execution":"client","parameters":{"type":"object","properties":{"query":{"type":"string"}}}}`
const searchCall = `{"type":"tool_search_call","id":"tsc_one","execution":"client","call_id":"one","status":"completed","arguments":{"query":"雪🌏 tools"},"future":true}`
const loadedTools = `[{"type":"namespace","name":"crm","tools":[{"type":"function","name":"lookup","parameters":{"type":"object"},"defer_loading":true},{"type":"custom","name":"edit","format":{"type":"text"},"defer_loading":false}]}]`

func TestClientToolSearchRequestHistoryAndLegacyProjection(t *testing.T) {
	input := `[` + searchCall + `,{"type":"tool_search_output","call_id":"one","execution":"client","status":"completed","tools":` + loadedTools + `},{"type":"additional_tools","role":"developer","tools":` + loadedTools + `}]`
	body := namespaceRequest("["+searchDeclaration+"]", "", input)
	r, err := ParseRequest(body)
	if err != nil || r.ValidateHTTP() != nil {
		t.Fatal(err, r.ValidateHTTP())
	}
	wire, err := r.WithModel("native")
	if err != nil || !bytes.Contains(wire, []byte(`"arguments":{"query":"雪🌏 tools"}`)) || !bytes.Contains(wire, []byte(`"future":true`)) {
		t.Fatal("search JSON was not preserved", string(wire), err)
	}
	if !strings.Contains(r.PromptText(), "雪🌏 tools") || !strings.Contains(r.PromptText(), "defer_loading") || !strings.Contains(r.PromptText(), "lookup") {
		t.Fatal("search or loaded definitions missing from prompt estimate", r.PromptText())
	}
	background := bytes.Replace(body, []byte(`"model":"public"`), []byte(`"model":"public","background":true`), 1)
	if _, err := (BackgroundParser{}).ParseRequest(execv1.Responses, background); err != nil {
		t.Fatal("background search rejected", err)
	}
}

func TestClientToolSearchFailsClosedBeforeLoadedToolsCanRecur(t *testing.T) {
	for _, tools := range []string{
		`[{"type":"tool_search"}]`, `[{"type":"tool_search","execution":"server"}]`,
		`[` + searchDeclaration + `,` + searchDeclaration + `]`,
		`[{"type":"tool_search","execution":"client","parameters":[]}]`,
		`[{"type":"tool_search","execution":"client","description":1}]`,
		`[{"type":"function","name":"x","defer_loading":null}]`,
		`[{"type":"custom","name":"x","defer_loading":1}]`,
		`[{"type":"namespace","name":"n","defer_loading":true,"tools":[{"type":"function","name":"x"}]}]`,
		`[{"type":"namespace","name":"n","tools":[` + searchDeclaration + `]}]`,
	} {
		r, err := ParseRequest(namespaceRequest(tools, "", ""))
		if err == nil {
			err = r.ValidateHTTP()
		}
		if err == nil {
			t.Fatal("invalid search declaration accepted", tools)
		}
	}
	for _, item := range []string{
		strings.Replace(searchCall, `"client"`, `"server"`, 1),
		strings.Replace(searchCall, `"one"`, `null`, 1),
		strings.Replace(searchCall, `{"query":"雪🌏 tools"}`, `"JSON string"`, 1),
		strings.Replace(searchCall, `"completed"`, `"failed"`, 1),
		`{"type":"tool_search_output","call_id":"one","execution":"client","status":"in_progress","tools":[]}`,
		`{"type":"tool_search_output","call_id":"one","execution":"client","status":"completed","tools":null}`,
		`{"type":"tool_search_output","call_id":"one","execution":"client","status":"completed","tools":[` + searchDeclaration + `]}`,
		`{"type":"additional_tools","role":"system","tools":[]}`,
		`{"type":"additional_tools","role":"developer","tools":[{"type":"web_search"}]}`,
		`{"type":"additional_tools","role":"developer","tools":[{"type":"function","name":"x","parameters":7}]}`,
		`{"type":"additional_tools","role":"developer","tools":[{"type":"namespace","name":"n","tools":[{"type":"namespace","name":"deep","tools":[]}]}]}`,
	} {
		r, err := ParseRequest(namespaceRequest("", "", "["+item+"]"))
		if err == nil {
			err = r.ValidateHTTP()
		}
		if err == nil {
			t.Fatal("invalid search history accepted", item)
		}
	}
	for _, count := range []int{0, 128, 129} {
		tools := make([]json.RawMessage, count)
		for i := range tools {
			tools[i] = json.RawMessage(fmt.Sprintf(`{"type":"function","name":"f%d"}`, i))
		}
		raw, _ := json.Marshal(tools)
		if (validateLoadedTools(raw) == nil) != (count <= 128) {
			t.Fatal("loaded tool capacity", count)
		}
	}
	for _, size := range []int{64, 65} {
		item := strings.Replace(searchCall, `"one"`, `"`+strings.Repeat("a", size)+`"`, 1)
		if (validateSearchItem(json.RawMessage(item)) == nil) != (size == 64) {
			t.Fatal("call_id byte bound", size)
		}
	}
	for _, size := range []int{65536, 65537} {
		raw := json.RawMessage(`{"x":"` + strings.Repeat("x", size-8) + `"}`)
		if len(raw) != size || searchObject(raw, false) != (size == 65536) {
			t.Fatal("schema byte bound", size)
		}
	}
}

func TestSearchItemsJSONSSEProgressAndMalformedOutput(t *testing.T) {
	for _, item := range []string{searchCall, `{"type":"tool_search_output","call_id":"one","execution":"client","status":"completed","tools":` + loadedTools + `}`} {
		doc := `{"id":"resp_one","object":"response","model":"m","status":"completed","output":[` + item + `],"usage":null}`
		if _, _, err := ParseResponse([]byte(doc)); err != nil {
			t.Fatal(err)
		}
		d := NewDecoder()
		s := d.NewStream().(*streamDecoder)
		wire := "event: response.output_item.added\ndata: {\"type\":\"response.output_item.added\",\"item\":" + item + "}\n\n"
		for _, line := range strings.Split(strings.TrimSuffix(wire, "\n"), "\n") {
			if _, err := s.DecodeLine([]byte(line)); err != nil {
				t.Fatal(err)
			}
		}
		wantEffective := strings.Contains(item, `"type":"tool_search_call"`)
		if s.Progress().EffectiveOutput != wantEffective {
			t.Fatal("incorrect search progress", item, s.Progress())
		}
		bad := strings.Replace(item, `"client"`, `"server"`, 1)
		if _, _, err := ParseResponse([]byte(strings.Replace(doc, item, bad, 1))); err == nil {
			t.Fatal("hosted search output accepted")
		}
	}
}

func TestSearchInterleavedWithFunctionCustomPreservesPartialAndTerminalFacts(t *testing.T) {
	for _, status := range []string{"", "completed", "failed", "incomplete"} {
		t.Run("terminal="+status, func(t *testing.T) {
			d := NewDecoder()
			s := d.NewStream()
			events := []string{
				`{"type":"response.output_item.added","output_index":0,"item":` + searchCall + `}`,
				`{"type":"response.output_item.added","output_index":1,"item":{"type":"function_call","call_id":"f","name":"lookup","arguments":""}}`,
				`{"type":"response.output_item.added","output_index":2,"item":{"type":"custom_tool_call","call_id":"c","name":"edit","input":""}}`,
				`{"type":"response.function_call_arguments.delta","output_index":1,"delta":"{}"}`,
				`{"type":"response.custom_tool_call_input.delta","output_index":2,"delta":"雪🌏"}`,
				`{"type":"response.output_item.done","output_index":0,"item":` + searchCall + `}`,
			}
			if status != "" {
				events = append(events, `{"type":"response.`+status+`","response":{"id":"resp_one","object":"response","model":"m","status":"`+status+`","output":[`+searchCall+`,{"type":"function_call","call_id":"f","name":"lookup","arguments":"{}"},{"type":"custom_tool_call","call_id":"c","name":"edit","input":"雪🌏"}],"usage":null}}`)
			}
			for _, event := range events {
				if _, err := s.DecodeLine([]byte("data: " + event)); err != nil {
					t.Fatal(err)
				}
				frames, err := s.DecodeLine(nil)
				if err != nil || len(frames) != 1 || !bytes.Contains(frames[0].Data, []byte(event)) {
					t.Fatal("search interleaved event changed", event, err)
				}
			}
			if _, err := s.End(); (err == nil) != (status != "") {
				t.Fatal("search truncation or terminal misclassified", status, err)
			}
			if got := d.Observation(); got.Status != status || got.Usage != nil {
				t.Fatal("search fabricated status or usage", got)
			}
		})
	}
}
