package responses

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestIdentityRewritePreservesBusinessMetadataAndOpaqueItems(t *testing.T) {
	response := `{"id":"resp-upstream","object":"response","status":"completed","error":{"message":"private-error","code":"private-code"},"metadata":{"error":"ticket-label","response_id":"business-label","response":"business-response"},"output":[{"type":"message","id":"msg-retained","role":"assistant","content":[{"type":"output_text","text":"hello"}],"business_extension":{"error":{"code":"customer-code"},"response_id":"customer-reference","response":{"id":"customer-id"}}}],"extension":{"response":{"id":"extension-id","error":"extension-error"},"response_id":"extension-reference","error":"extension-ticket"}}`
	assertPreserved := func(t *testing.T, original, rewritten []byte) {
		t.Helper()
		var before, after map[string]any
		if json.Unmarshal(original, &before) != nil || json.Unmarshal(rewritten, &after) != nil {
			t.Fatal("invalid rewritten JSON")
		}
		if after["id"] != "resp-public" {
			t.Fatal("protocol response identity was not rewritten")
		}
		for _, key := range []string{"metadata", "output", "extension"} {
			if !reflect.DeepEqual(before[key], after[key]) {
				t.Fatalf("business field %s changed: before=%v after=%v", key, before[key], after[key])
			}
		}
		if strings.Contains(string(rewritten), "private-error") || strings.Contains(string(rewritten), "private-code") {
			t.Fatal("protocol error details escaped sanitization")
		}
	}
	t.Run("JSON", func(t *testing.T) {
		out, _, err := NewDecoderWithID("resp-public").Decode([]byte(response))
		if err != nil {
			t.Fatal(err)
		}
		assertPreserved(t, []byte(response), out)
	})
	t.Run("SSE", func(t *testing.T) {
		envelope := `{"type":"response.completed","sequence_number":0,"response_id":"resp-upstream","metadata":{"error":"event-ticket","response_id":"event-business-label"},"extension":{"response":{"id":"event-extension-id"},"error":"event-business-error"},"response":` + response + `}`
		s := NewDecoderWithID("resp-public").NewStream()
		if _, err := s.DecodeLine([]byte("data: " + envelope)); err != nil {
			t.Fatal(err)
		}
		frames, err := s.DecodeLine(nil)
		if err != nil || len(frames) != 1 || !frames[0].Complete {
			t.Fatalf("invalid terminal frame: %+v %v", frames, err)
		}
		var before, after map[string]json.RawMessage
		_ = json.Unmarshal([]byte(envelope), &before)
		if json.Unmarshal(bytes.TrimSpace(bytes.TrimPrefix(frames[0].Data, []byte("data: "))), &after) != nil {
			t.Fatalf("invalid SSE JSON: %s", frames[0].Data)
		}
		if string(after["response_id"]) != `"resp-public"` {
			t.Fatal("event protocol identity was not rewritten")
		}
		for _, key := range []string{"metadata", "extension"} {
			var original, rewritten any
			_ = json.Unmarshal(before[key], &original)
			_ = json.Unmarshal(after[key], &rewritten)
			if !reflect.DeepEqual(original, rewritten) {
				t.Fatalf("opaque event field %s changed", key)
			}
		}
		assertPreserved(t, before["response"], after["response"])
	})
}

func TestHTTPAdmissionPreservesNativeFieldsAndDefaultStorage(t *testing.T) {
	for _, storage := range []string{"", `,"store":true`, `,"store":false`} {
		body := `{"model":"public","instructions":"system rules","max_output_tokens":16,"input":[{"type":"function_call","call_id":"call-a","name":"lookup","arguments":"{}"},{"type":"function_call_output","call_id":"call-a","output":"result"},{"type":"reasoning","encrypted_content":"opaque","summary":[]}],"tools":[{"type":"function","name":"lookup","parameters":{"type":"object"}}],"extension":{"retain":true}` + storage + `}`
		r, err := ParseRequest([]byte(body))
		if err != nil {
			t.Fatal(err)
		}
		if err := r.ValidateHTTP(); err != nil {
			t.Fatal(err)
		}
		if r.StoreEnabled() != (storage != `,"store":false`) || r.MaxOutputTokens() != 16 {
			t.Fatalf("wrong native options: %+v", r)
		}
		for _, want := range []string{"system rules", "lookup", "result"} {
			if !strings.Contains(r.PromptText(), want) {
				t.Fatalf("missing estimate text %q", want)
			}
		}
		out, err := r.WithModel("actual")
		if err != nil {
			t.Fatal(err)
		}
		var original, rewritten map[string]json.RawMessage
		_ = json.Unmarshal([]byte(body), &original)
		_ = json.Unmarshal(out, &rewritten)
		delete(original, "model")
		delete(rewritten, "model")
		a, _ := json.Marshal(original)
		b, _ := json.Marshal(rewritten)
		if string(a) != string(b) {
			t.Fatalf("native fields changed: %s", out)
		}
	}
	text, _ := ParseRequest([]byte(`{"model":"m","input":"question","tools":[{"type":"function","name":"lookup"}]}`))
	if !strings.Contains(text.PromptText(), "question") || !strings.Contains(text.PromptText(), "lookup") {
		t.Fatal("text input lost tool estimate")
	}
}

func TestHTTPAdmissionRejectsUnsupportedAndMalformedSemantics(t *testing.T) {
	for _, fields := range []string{
		`"background":true`, `"previous_response_id":"resp-other"`, `"conversation":"conv-other"`,
		`"max_output_tokens":15`, `"max_output_tokens":16.5`, `"instructions":{}`,
		`"tools":[{"type":"web_search"}]`, `"tools":[{"type":"function"}]`,
		`"tool_choice":{"type":"web_search"}`, `"audio":{}`, `"prompt":{"id":"p"}`,
	} {
		r, err := ParseRequest([]byte(`{"model":"m","input":"hi",` + fields + `}`))
		if err == nil {
			err = r.ValidateHTTP()
		}
		if err == nil {
			t.Fatalf("unsupported HTTP option accepted: %s", fields)
		}
	}
	for _, input := range []string{
		`null`, `{}`, `[null]`, `[{"type":"item_reference","id":"msg-other"}]`,
		`[{"role":"user","content":[{"type":"input_image","image_url":"https://private.test"}]}]`,
		`[{"type":"function_call_output","output":"missing identity"}]`,
		`[{"type":"function_call","call_id":"a","name":"f","arguments":{}}]`,
	} {
		r, err := ParseRequest([]byte(`{"model":"m","input":` + input + `}`))
		if err == nil {
			err = r.ValidateHTTP()
		}
		if err == nil {
			t.Fatalf("unsupported HTTP input accepted: %s", input)
		}
	}
}

func TestNativeTerminalJSONIdentityAndSafeErrors(t *testing.T) {
	for _, status := range []string{"completed", "incomplete", "failed", "cancelled"} {
		d := NewDecoderWithID("resp-public")
		body := `{"id":"resp-upstream","object":"response","status":"` + status + `","output":[{"type":"function_call","id":"fc-original","call_id":"call-original","name":"lookup","arguments":"{}"}],"usage":{"input_tokens":3,"output_tokens":2,"total_tokens":5,"output_tokens_details":{"reasoning_tokens":1}},"error":{"message":"secret-key https://private.test","code":"secret-code","debug":"private"}}`
		out, usage, err := d.Decode([]byte(body))
		if err != nil || usage == nil || usage.TotalTokens != 5 {
			t.Fatalf("terminal decode failed: %v", err)
		}
		observation := d.Observation()
		if observation.ID != "resp-upstream" || observation.Status != status || observation.Usage == nil {
			t.Fatalf("lost terminal observation: %+v", observation)
		}
		observation.Usage.TotalTokens = 0
		if d.Observation().Usage.TotalTokens != 5 {
			t.Fatal("mutable usage observation escaped")
		}
		for _, secret := range []string{"resp-upstream", "secret-key", "secret-code", "private.test", "debug"} {
			if strings.Contains(string(out), secret) {
				t.Fatalf("private data leaked: %s", out)
			}
		}
		for _, retained := range []string{"resp-public", "fc-original", "call-original", "reasoning_tokens"} {
			if !strings.Contains(string(out), retained) {
				t.Fatalf("native output lost: %s", out)
			}
		}
	}
	for _, status := range []string{"queued", "in_progress"} {
		if _, _, err := NewDecoder().Decode([]byte(`{"id":"resp-u","object":"response","status":"` + status + `"}`)); err == nil {
			t.Fatal("nonterminal HTTP response accepted")
		}
	}
}

func TestNativeSSETerminalsAndIdentity(t *testing.T) {
	for _, status := range []string{"completed", "incomplete", "failed", "cancelled"} {
		d := NewDecoderWithID("resp-public")
		s := d.NewStream()
		lines := []string{
			`event: response.created`,
			`data: {"type":"response.created","sequence_number":0,"response":{"id":"resp-upstream","object":"response","status":"in_progress"}}`, "",
			`event: response.function_call_arguments.delta`,
			`data: {"type":"response.function_call_arguments.delta","sequence_number":1,"response_id":"resp-upstream","item_id":"fc-original","delta":"{}"}`, "",
			`event: response.` + status,
			`data: {"type":"response.` + status + `","sequence_number":2,"response":{"id":"resp-upstream","object":"response","status":"` + status + `","output":[],"error":{"message":"private.test secret-key","code":"secret-code"},"usage":{"input_tokens":3,"output_tokens":2,"total_tokens":5}}}`, "",
		}
		var output strings.Builder
		complete := false
		for _, line := range lines {
			frames, err := s.DecodeLine([]byte(line))
			if err != nil {
				t.Fatal(err)
			}
			for _, frame := range frames {
				output.Write(frame.Data)
				complete = complete || frame.Complete
			}
		}
		if _, err := s.End(); err != nil {
			t.Fatal(err)
		}
		o := d.Observation()
		if !complete || o.ID != "resp-upstream" || o.Status != status || o.Usage == nil || o.Usage.TotalTokens != 5 {
			t.Fatalf("wrong terminal observation: %+v", o)
		}
		for _, secret := range []string{"resp-upstream", "secret-key", "secret-code", "private.test", "[DONE]"} {
			if strings.Contains(output.String(), secret) {
				t.Fatalf("unsafe native output: %s", output.String())
			}
		}
		if !strings.Contains(output.String(), "fc-original") || !strings.Contains(output.String(), "resp-public") {
			t.Fatal("native item identity lost")
		}
	}
	d := NewDecoderWithID("resp-public")
	s := d.NewStream()
	_, _ = s.DecodeLine([]byte(`data: {"type":"error","sequence_number":0,"message":"secret-key","code":"private-code","debug":"private.test"}`))
	frames, err := s.DecodeLine(nil)
	if err != nil || len(frames) != 1 || !frames[0].Complete || d.Observation().Status != "failed" || strings.Contains(string(frames[0].Data), "private") {
		t.Fatalf("unsafe or lost native error: %+v %v", frames, err)
	}
	if _, err := s.End(); err != nil {
		t.Fatal(err)
	}

	s = NewDecoderWithID("resp-public").NewStream()
	for _, line := range []string{`data: {"type":"response.created","response":{"id":"one","object":"response","status":"in_progress"}}`, "", `data: {"type":"response.completed","response":{"id":"two","object":"response","status":"completed"}}`} {
		if _, err := s.DecodeLine([]byte(line)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.DecodeLine(nil); err == nil {
		t.Fatal("upstream response identity changed within an attempt")
	}
}
