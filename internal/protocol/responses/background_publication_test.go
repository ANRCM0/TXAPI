package responses

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	history "minigateway/internal/foundation/contracts/responsehistory/v1"
)

func publicationDocument(output string) json.RawMessage {
	return json.RawMessage(`{"id":"resp_provider","object":"response","model":"provider-model","status":"completed","store":false,"previous_response_id":"resp_provider_parent","conversation":{"id":"conv_provider"},"output":` + output + `,"usage":null}`)
}

func TestBackgroundPublicationPreservesFullFunctionRoundAndRewritesProvenance(t *testing.T) {
	codec := BackgroundHistoryCodec{}
	request := json.RawMessage(`{"model":"alias","background":true,"store":true,"stream":true,"instructions":"current only","input":[{"role":"user","content":"first"},{"id":"fc_parent","type":"function_call","call_id":"c_parent","name":"lookup","arguments":"{}"},{"type":"function_call_output","call_id":"c_parent","output":"result"}],"tools":[{"type":"function","name":"lookup","parameters":{"type":"object"}}],"custom_extension":42}`)
	response := publicationDocument(`[{"id":"fc_result","type":"function_call","call_id":"c_next","name":"lookup","arguments":"{\"x\":1}","status":"completed"}]`)
	prepared, err := codec.PrepareBackgroundHistory(request, "resp_local")
	if err != nil {
		t.Fatal(err)
	}
	again, err := codec.PrepareBackgroundHistory(prepared.Request, "resp_local")
	if err != nil || !reflect.DeepEqual(prepared, again) {
		t.Fatal("recovery changed stable input identities", err)
	}
	out, err := codec.ProjectBackgroundHistory(prepared.Request, response, "resp_local", "resp_local_parent", "alias")
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Input) != 3 || len(out.Items) != 4 || !bytes.Contains(out.Items[3], []byte(`"call_id":"c_next"`)) || !bytes.Contains(out.Items[3], []byte(`"arguments":"{\"x\":1}"`)) {
		t.Fatalf("function context lost: %+v", out)
	}
	var document, fields map[string]json.RawMessage
	_ = json.Unmarshal(out.Response, &document)
	_ = json.Unmarshal(out.Request, &fields)
	if string(document["id"]) != `"resp_local"` || string(document["model"]) != `"alias"` || string(document["store"]) != "true" || string(document["previous_response_id"]) != `"resp_local_parent"` || document["conversation"] != nil || string(fields["stream"]) != "true" || string(fields["instructions"]) != `"current only"` || string(fields["custom_extension"]) != "42" || bytes.Contains(out.Response, []byte("resp_provider")) {
		t.Fatalf("private provenance escaped or current options changed: %s / %s", out.Response, out.Request)
	}
	continued, err := codec.CompileBackground(json.RawMessage(`{"model":"alias","background":true,"input":[{"type":"function_call_output","call_id":"c_next","output":"next result"}]}`), out.Items)
	if err != nil || !bytes.Contains(continued, []byte("next result")) || bytes.Contains(continued, []byte("item_tx_")) {
		t.Fatal("published items cannot be replayed as complete input", string(continued), err)
	}
	if !bytes.Contains(request, []byte(`"content":"first"`)) || !bytes.Contains(response, []byte("resp_provider_parent")) {
		t.Fatal("projection mutated caller-owned source")
	}
}

func TestBackgroundPublicationTextRefusalAndExplicitEmptyOutput(t *testing.T) {
	codec := BackgroundHistoryCodec{}
	request := json.RawMessage(`{"model":"m","background":true,"store":true,"input":"question"}`)
	for _, output := range []string{
		`[]`,
		`[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"answer","annotations":[]}]}]`,
		`[{"type":"message","role":"assistant","content":[{"type":"refusal","refusal":"declined"}]}]`,
	} {
		out, err := codec.ProjectBackgroundHistory(request, publicationDocument(output), "resp_local", "", "m")
		if err != nil {
			t.Fatal(output, err)
		}
		if len(out.Input) != 1 || len(out.Items) < 1 || !bytes.Contains(out.Response, []byte(`"previous_response_id":null`)) {
			t.Fatal("empty/text/refusal result lost its complete input", out)
		}
	}
}

func TestBackgroundPublicationCanonicalMessageItemsMatchSDKContentShape(t *testing.T) {
	codec := BackgroundHistoryCodec{}
	for _, role := range []string{"user", "system", "developer", "assistant"} {
		request, _ := json.Marshal(map[string]any{"model": "m", "background": true, "store": true,
			"input": []any{map[string]string{"role": role, "content": "plain text"}}})
		prepared, err := codec.PrepareBackgroundHistory(request, "resp_local")
		if err != nil {
			t.Fatal(role, err)
		}
		var item struct {
			Content []struct {
				Type        string
				Text        string
				Annotations []json.RawMessage
			}
		}
		if json.Unmarshal(prepared.Input[0], &item) != nil || len(item.Content) != 1 || item.Content[0].Text != "plain text" {
			t.Fatal("stored message does not expose structured SDK content", role, string(prepared.Input[0]))
		}
		want := "input_text"
		if role == "assistant" {
			want = "output_text"
			if item.Content[0].Annotations == nil {
				t.Fatal("assistant output_text lacks SDK annotations array")
			}
		}
		if item.Content[0].Type != want {
			t.Fatal("message content direction changed", role, item.Content[0].Type)
		}
		again, err := codec.PrepareBackgroundHistory(prepared.Request, "resp_local")
		if err != nil || !reflect.DeepEqual(prepared, again) {
			t.Fatal("message canonicalization is not stable", role, err)
		}
	}
}

func TestBackgroundPublicationRejectsUnsupportedInputWithoutWeakeningExecution(t *testing.T) {
	for _, extra := range []string{
		`"input":[{"type":"reasoning","encrypted_content":"opaque"}]`,
		`"input":[{"type":"custom_tool_call","call_id":"c","name":"custom","input":"text"}]`,
		`"input":[{"type":"function_call","call_id":"c","name":"f","namespace":"group","arguments":"{}"}]`,
		`"input":"x","tools":[{"type":"namespace","name":"group","tools":[{"type":"function","name":"f"}]}]`,
		`"input":"x","tools":[{"type":"custom","name":"custom"}]`,
		`"input":"x","tools":[{"type":"tool_search","execution":"client","parameters":{"type":"object"}}]`,
	} {
		raw := json.RawMessage(`{"model":"m","background":true,"store":true,` + extra + `}`)
		if _, err := (BackgroundHistoryCodec{}).PrepareBackgroundHistory(raw, "resp_local"); err == nil {
			t.Fatalf("unsupported publication input accepted: %s", raw)
		}
	}
	request := json.RawMessage(`{"model":"m","background":true,"input":[{"type":"reasoning","encrypted_content":"opaque"}]}`)
	if _, err := (BackgroundHistoryCodec{}).CompileBackground(request, nil); err != nil {
		t.Fatal("publication restriction changed P6-A execution compilation", err)
	}
}

func TestBackgroundPublicationRejectsUnknownMalformedAndUnreplayableOutputs(t *testing.T) {
	request := json.RawMessage(`{"model":"m","background":true,"store":true,"input":"x"}`)
	for _, output := range []string{
		`null`, `"text"`, `[null]`, `[{}]`,
		`[{"type":"message","role":"user","content":[{"type":"output_text","text":"x"}]}]`,
		`[{"type":"message","role":"assistant","content":"text"}]`,
		`[{"type":"message","role":"assistant","content":[{"type":"output_text","text":null}]}]`,
		`[{"type":"message","role":"assistant","content":[{"type":"input_image","image_url":"https://example.test/x"}]}]`,
		`[{"type":"function_call","call_id":"c","name":"f","arguments":{}}]`,
		`[{"type":"function_call","name":"f","arguments":"{}"}]`,
		`[{"type":"function_call_output","call_id":"c","output":"x"}]`,
		`[{"type":"reasoning","encrypted_content":"opaque"}]`,
		`[{"type":"function_call","call_id":"c","name":"f","arguments":"{}","status":"in_progress"}]`,
		`[{"type":"message","id":"item_tx_bg_local_in_0","role":"assistant","content":[]}]`,
		`[{"type":"message","role":"assistant","content":[],"id":"duplicate"},{"type":"message","role":"assistant","content":[],"id":"duplicate"}]`,
	} {
		if _, err := (BackgroundHistoryCodec{}).ProjectBackgroundHistory(request, publicationDocument(output), "resp_local", "", "m"); err == nil {
			t.Fatalf("unreplayable output accepted: %s", output)
		}
	}
	for _, status := range []string{"queued", "in_progress", "incomplete", "failed", "cancelled", "unknown"} {
		document := bytes.Replace(publicationDocument(`[]`), []byte(`"status":"completed"`), []byte(`"status":"`+status+`"`), 1)
		if _, err := (BackgroundHistoryCodec{}).ProjectBackgroundHistory(request, document, "resp_local", "", "m"); err == nil {
			t.Fatal("non-completed status accepted", status)
		}
	}
	document := bytes.Replace(publicationDocument(`[]`), []byte(`,"output":[]`), nil, 1)
	if _, err := (BackgroundHistoryCodec{}).ProjectBackgroundHistory(request, document, "resp_local", "", "m"); err == nil {
		t.Fatal("missing output was inferred as empty")
	}
	if _, err := (BackgroundHistoryCodec{}).ProjectBackgroundHistory(request, publicationDocument(`[]`), "resp_local", "", "other"); err == nil {
		t.Fatal("client model mismatch accepted")
	}
}

func TestBackgroundPublicationBoundsWholeReplayContext(t *testing.T) {
	codec := BackgroundHistoryCodec{}
	input := make([]map[string]string, history.MaxContextItems)
	for index := range input {
		input[index] = map[string]string{"role": "user", "content": "x"}
	}
	request, _ := json.Marshal(map[string]any{"model": "m", "background": true, "store": true, "input": input})
	if _, err := codec.ProjectBackgroundHistory(request, publicationDocument(`[]`), "resp_local", "", "m"); err != nil {
		t.Fatal("exact item limit with empty output rejected", err)
	}
	if _, err := codec.ProjectBackgroundHistory(request, publicationDocument(`[{"type":"message","role":"assistant","content":[]}]`), "resp_local", "", "m"); err == nil {
		t.Fatal("input/output combined item overflow accepted")
	}
	request, _ = json.Marshal(map[string]any{"model": "m", "background": true, "store": true, "input": strings.Repeat("x", history.MaxContextBytes/2)})
	output, _ := json.Marshal([]any{map[string]any{"type": "message", "role": "assistant", "content": []any{map[string]string{"type": "output_text", "text": strings.Repeat("y", history.MaxContextBytes/2)}}}})
	if _, err := codec.ProjectBackgroundHistory(request, publicationDocument(string(output)), "resp_local", "", "m"); err == nil {
		t.Fatal("separately bounded request/output exceeded combined replay limit")
	}
}
