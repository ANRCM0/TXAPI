package responses

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	execv1 "minigateway/internal/foundation/contracts/execution/v1"
)

func TestCustomToolDeclarationsInputsAndFormatsRemainNative(t *testing.T) {
	for _, format := range []string{"", `,"format":null`, `,"format":{"type":"text","future":true}`, `,"format":{"type":"grammar","syntax":"lark","definition":"start: /.+/"}`, `,"format":{"type":"grammar","syntax":"regex","definition":"[雪🌏]+"}`} {
		tools := `[{"type":"namespace","name":"text","tools":[{"type":"custom","name":"edit","description":"opaque"` + format + `,"future":true},{"type":"function","name":"lookup"}]},{"type":"custom","name":"top"}]`
		input := `[{"type":"custom_tool_call","id":"ctc","call_id":"one","name":"edit","namespace":"text","input":"raw 雪🌏\\nnot JSON","future":{"keep":true}},{"type":"custom_tool_call_output","call_id":"one","name":"edit","namespace":"text","output":[{"type":"output_text","text":"result"}]}]`
		body := namespaceRequest(tools, `{"type":"custom","name":"edit","namespace":"text"}`, input)
		r, err := ParseRequest(body)
		if err != nil || r.ValidateHTTP() != nil {
			t.Fatal("custom rejected", err, r.ValidateHTTP())
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
			t.Fatalf("custom raw fields changed: %s", wire)
		}
		if !strings.Contains(r.PromptText(), "raw 雪🌏") || !strings.Contains(r.PromptText(), "result") {
			t.Fatal("custom text missing from legacy estimate")
		}
		background := bytes.Replace(body, []byte(`"model":"public"`), []byte(`"model":"public","background":true`), 1)
		parsed, err := (BackgroundParser{}).ParseRequest(execv1.Responses, background)
		if err != nil || !bytes.Equal(parsed.Body(), background) {
			t.Fatal("background custom changed", err)
		}
	}
}

func TestCustomToolShapeBoundsAndUnsupportedCapabilities(t *testing.T) {
	for _, bad := range []string{
		`{"type":"tool_search"}`, `{"type":"web_search"}`,
		`{"type":"custom","name":""}`, `{"type":"custom","name":" x"}`, `{"type":"custom","name":"bad\n"}`,
		`{"type":"custom","name":"x","description":1}`,
		`{"type":"custom","name":"x","format":true}`, `{"type":"custom","name":"x","format":{}}`,
		`{"type":"custom","name":"x","format":{"type":"json_schema"}}`,
		`{"type":"custom","name":"x","format":{"type":"grammar","syntax":"unknown","definition":"a"}}`,
		`{"type":"custom","name":"x","format":{"type":"grammar","syntax":"lark","definition":""}}`,
		`{"type":"custom","name":"x","format":{"type":"grammar","syntax":"regex","definition":null}}`,
		`{"type":"namespace","name":"n","tools":[{"type":"function","name":"same"},{"type":"custom","name":"same"}]}`,
		`{"type":"namespace","name":"n","tools":[{"type":"namespace","name":"deep","tools":[{"type":"custom","name":"x"}]}]}`,
		`{"type":"namespace","name":"n","tools":[{"type":"tool_search","name":"x"}]}`,
	} {
		r, err := ParseRequest(namespaceRequest("["+bad+"]", "", ""))
		if err == nil {
			err = r.ValidateHTTP()
		}
		if err == nil {
			t.Fatalf("invalid custom tool accepted %s", bad)
		}
	}
	for _, name := range []string{strings.Repeat("a", 128), strings.Repeat("a", 129), strings.Repeat("雪", 42), strings.Repeat("雪", 43)} {
		quoted, _ := json.Marshal(name)
		r, err := ParseRequest(namespaceRequest(`[{"type":"custom","name":`+string(quoted)+`}]`, "", ""))
		if err == nil {
			err = r.ValidateHTTP()
		}
		if (err == nil) != (len(name) <= 128) {
			t.Fatal("custom name byte bound", len(name), err)
		}
	}
	for _, size := range []int{65536, 65537} {
		definition, _ := json.Marshal(strings.Repeat("a", size))
		r, err := ParseRequest(namespaceRequest(`[{"type":"custom","name":"x","format":{"type":"grammar","syntax":"lark","definition":`+string(definition)+`}}]`, "", ""))
		if err == nil {
			err = r.ValidateHTTP()
		}
		if (err == nil) != (size == 65536) {
			t.Fatal("grammar byte bound", size, err)
		}
	}
	for _, input := range []string{
		`{"type":"custom_tool_call","call_id":"a","name":"x","input":null}`,
		`{"type":"custom_tool_call","call_id":"a","name":"x","input":{}}`,
		`{"type":"custom_tool_call","name":"x","input":"x"}`,
		`{"type":"custom_tool_call","call_id":"a","name":"x","input":"x","namespace":1}`,
		`{"type":"custom_tool_call_output","call_id":"a","name":"","output":"x"}`,
		`{"type":"custom_tool_call_output","call_id":"a","output":{"data":"x"}}`,
		`{"type":"custom_tool_call_output","call_id":"a","output":[{"type":"image","url":"x"}]}`,
	} {
		r, err := ParseRequest(namespaceRequest(`[]`, "", "["+input+"]"))
		if err == nil {
			err = r.ValidateHTTP()
		}
		if err == nil {
			t.Fatalf("invalid custom input accepted %s", input)
		}
	}
	for _, typ := range []string{"function", "custom"} {
		r, err := ParseRequest(namespaceRequest(`[]`, fmt.Sprintf(`{"type":%q,"name":"x","namespace":true}`, typ), ""))
		if err == nil {
			err = r.ValidateHTTP()
		}
		if err == nil {
			t.Fatal("invalid custom choice")
		}
	}
}

func TestCustomAndFunctionInterleavedEventsRetainNativeInput(t *testing.T) {
	stream := NewDecoder().NewStream()
	events := []string{
		`{"type":"response.output_item.added","output_index":0,"item":{"type":"custom_tool_call","call_id":"c","name":"edit","input":""}}`,
		`{"type":"response.output_item.added","output_index":1,"item":{"type":"function_call","call_id":"f","name":"lookup","arguments":""}}`,
		`{"type":"response.custom_tool_call_input.delta","output_index":0,"delta":"雪🌏"}`,
		`{"type":"response.function_call_arguments.delta","output_index":1,"delta":"{}"}`,
		`{"type":"response.custom_tool_call_input.done","output_index":0,"input":"雪🌏"}`,
		`{"type":"response.function_call_arguments.done","output_index":1,"arguments":"{}"}`,
		`{"type":"response.completed","response":{"id":"r","object":"response","model":"m","status":"completed","output":[{"type":"custom_tool_call","call_id":"c","name":"edit","input":"雪🌏"},{"type":"function_call","call_id":"f","name":"lookup","arguments":"{}"}],"usage":null}}`,
	}
	for _, event := range events {
		if _, err := stream.DecodeLine([]byte("data: " + event)); err != nil {
			t.Fatal(err)
		}
		frames, err := stream.DecodeLine(nil)
		if err != nil || len(frames) != 1 || !bytes.Contains(frames[0].Data, []byte(event)) {
			t.Fatal("interleaved native event changed", event, err)
		}
	}
	if _, err := stream.End(); err != nil {
		t.Fatal(err)
	}
}

func TestCustomToolOutputEventsAndEffectiveProgress(t *testing.T) {
	item := `{"type":"custom_tool_call","id":"ctc","call_id":"call","name":"edit","namespace":"text","input":"raw 雪🌏","future":true}`
	body := `{"id":"resp_remote","object":"response","model":"m","status":"completed","output":[` + item + `],"usage":null}`
	out, usage, err := NewDecoderWithID("resp_local").Decode([]byte(body))
	if err != nil || usage != nil || !bytes.Contains(out, []byte(`"input":"raw 雪🌏"`)) || !bytes.Contains(out, []byte(`"future":true`)) {
		t.Fatal("custom JSON changed", string(out), err)
	}
	for _, test := range []struct {
		event            string
		valid, effective bool
	}{
		{`{"type":"response.output_item.added","item":` + item + `}`, true, true},
		{`{"type":"response.custom_tool_call_input.delta","delta":"雪🌏"}`, true, true},
		{`{"type":"response.custom_tool_call_input.delta","delta":""}`, true, false},
		{`{"type":"response.custom_tool_call_input.done","input":"raw 雪🌏"}`, true, false},
		{`{"type":"response.output_item.done","item":` + item + `}`, true, false},
		{`{"type":"response.completed","response":` + body + `}`, true, false},
		{`{"type":"response.custom_tool_call_input.delta","delta":null}`, false, false},
		{`{"type":"response.custom_tool_call_input.done","input":{}}`, false, false},
		{`{"type":"response.output_item.added","item":{"type":"custom_tool_call","name":"x","call_id":"a","input":true}}`, false, false},
	} {
		stream := NewDecoder().NewStream().(*streamDecoder)
		if frames, err := stream.DecodeLine([]byte("data: " + test.event)); err != nil || len(frames) != 0 {
			t.Fatal("partial custom event emitted")
		}
		frames, err := stream.DecodeLine(nil)
		if (err == nil) != test.valid || stream.Progress().EffectiveOutput != test.effective {
			t.Fatalf("custom event %s: %v %+v", test.event, err, stream.Progress())
		}
		if test.valid && (len(frames) != 1 || !bytes.Contains(frames[0].Data, []byte(test.event))) {
			t.Fatal("custom native event changed")
		}
		if _, err := stream.End(); test.valid && !strings.Contains(test.event, "response.completed") && err == nil {
			t.Fatal("unterminated custom flow became successful")
		}
	}
}

func TestCustomBackgroundStreamRejectsMalformedKnownEvents(t *testing.T) {
	for _, event := range []string{
		`{"type":"response.custom_tool_call_input.delta","delta":null}`,
		`{"type":"response.custom_tool_call_input.done","input":42}`,
		`{"type":"response.output_item.done","item":{"type":"custom_tool_call","call_id":"a","name":"x","input":null}}`,
	} {
		decoder := &backgroundStreamDecoder{owner: &backgroundDecoder{publicID: "resp_local"}, remoteID: "resp_remote", lastSequence: -1}
		var fields map[string]json.RawMessage
		_ = json.Unmarshal([]byte(event), &fields)
		fields["sequence_number"] = json.RawMessage("0")
		data, _ := json.Marshal(fields)
		_, _ = decoder.DecodeLine(append([]byte("data: "), data...))
		if _, err := decoder.DecodeLine(nil); err == nil {
			t.Fatal("invalid background custom event persisted", event)
		}
	}
}
