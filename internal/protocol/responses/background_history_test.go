package responses

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	execution "minigateway/internal/foundation/contracts/execution/v1"
	history "minigateway/internal/foundation/contracts/responsehistory/v1"
)

func TestBackgroundHistoryCompileCompleteToolContext(t *testing.T) {
	inherited := []json.RawMessage{
		json.RawMessage(`{"id":"item_tx_user","role":"user","content":"read the file"}`),
		json.RawMessage(`{"id":"fc_provider","type":"function_call","call_id":"call_1","name":"read","namespace":"files","arguments":"{}"}`),
		json.RawMessage(`{"id":"rs_provider","type":"reasoning","encrypted_content":"opaque-bytes","summary":[]}`),
	}
	body := json.RawMessage(`{"model":"m","background":true,"stream":true,"store":false,"previous_response_id":"resp_parent","input":[{"id":"item_tx_result","type":"function_call_output","call_id":"call_1","namespace":"files","output":"file contents"}],"instructions":"current instructions","tools":[{"type":"namespace","name":"files","tools":[{"type":"function","name":"read","parameters":{"type":"object"}}]}],"custom_extension":{"keep":true}}`)
	before, _ := json.Marshal(inherited)
	out, err := (BackgroundHistoryCodec{}).CompileBackground(body, inherited)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(out, &fields)
	if fields["previous_response_id"] != nil || string(fields["store"]) != "false" || string(fields["stream"]) != "true" || string(fields["background"]) != "true" || string(fields["instructions"]) != `"current instructions"` || string(fields["custom_extension"]) != `{"keep":true}` {
		t.Fatalf("request options changed: %s", out)
	}
	var items []map[string]json.RawMessage
	_ = json.Unmarshal(fields["input"], &items)
	if len(items) != 4 || items[0]["id"] != nil || items[3]["id"] != nil || string(items[1]["id"]) != `"fc_provider"` || string(items[1]["namespace"]) != `"files"` || string(items[2]["encrypted_content"]) != `"opaque-bytes"` || string(items[3]["call_id"]) != `"call_1"` {
		t.Fatalf("complete tool context lost: %s", fields["input"])
	}
	after, _ := json.Marshal(inherited)
	if !bytes.Equal(before, after) || !bytes.Contains(body, []byte(`"previous_response_id":"resp_parent"`)) {
		t.Fatal("compiler mutated caller-owned request/history")
	}
	if _, err := (BackgroundParser{}).ParseRequest(execution.Responses, body); err == nil {
		t.Fatal("history compiler weakened direct background parser")
	}
}

func TestBackgroundHistoryCompileCustomContextAndText(t *testing.T) {
	inherited := []json.RawMessage{
		json.RawMessage(`{"type":"custom_tool_call","id":"ct_provider","call_id":"custom_1","name":"edit","input":"patch"}`),
		json.RawMessage(`{"type":"custom_tool_call_output","id":"item_tx_custom","call_id":"custom_1","output":"done"}`),
	}
	for _, store := range []string{"", `,"store":true`, `,"store":false`} {
		body := json.RawMessage(`{"model":"m","background":true,"previous_response_id":"resp_parent","input":"continue"` + store + `}`)
		out, err := (BackgroundHistoryCodec{}).CompileBackground(body, inherited)
		if err != nil {
			t.Fatal(err)
		}
		var fields map[string]json.RawMessage
		_ = json.Unmarshal(out, &fields)
		if (fields["store"] == nil) != (store == "") || fields["stream"] != nil {
			t.Fatalf("implicit options acquired explicit values: %s", out)
		}
		var items []map[string]json.RawMessage
		_ = json.Unmarshal(fields["input"], &items)
		if len(items) != 3 || items[1]["id"] != nil || string(items[2]["role"]) != `"user"` || string(items[2]["content"]) != `"continue"` {
			t.Fatalf("custom context/text input lost: %s", out)
		}
	}
}

func TestBackgroundHistoryCompileRejectsUnsupportedInheritedItems(t *testing.T) {
	body := json.RawMessage(`{"model":"m","background":true,"previous_response_id":"resp_parent","input":"continue"}`)
	for _, item := range []string{
		`null`, `"text"`, `{}`, `{"type":"item_reference","id":"msg_remote"}`,
		`{"type":"reasoning","id":"rs_remote"}`,
		`{"type":"message","role":"user","content":[{"type":"input_image","image_url":"https://example.test/a.png"}]}`,
		`{"type":"function_call","call_id":"c","name":"f","arguments":{}}`,
		`{"type":"function_call_output","output":"missing call identity"}`,
		`{"type":"message","role":"user","content":"x","content":"y"}`,
		`{"type":"message","role":"user","content":"x","id":null}`,
	} {
		t.Run(item, func(t *testing.T) {
			if _, err := (BackgroundHistoryCodec{}).CompileBackground(body, []json.RawMessage{json.RawMessage(item)}); err == nil {
				t.Fatal("unsupported inherited item accepted")
			}
		})
	}
}

func TestBackgroundHistoryCompileRejectsInvalidRequest(t *testing.T) {
	for _, body := range []string{
		`{"model":"m","background":false,"input":"x"}`,
		`{"model":"m","background":true,"input":null}`,
		`{"model":"m","background":true,"input":"x","stream":null}`,
		`{"model":"m","background":true,"input":"x","store":null}`,
		`{"model":"m","background":true,"input":"x","previous_response_id":123}`,
		`{"model":"m","background":true,"input":"x","conversation":"conv_x"}`,
		`{"model":"m","background":true,"input":"x","input":"y"}`,
		`{"model":"m","background":true,"input":"x","tools":[{"type":"web_search"}]}`,
	} {
		if _, err := (BackgroundHistoryCodec{}).CompileBackground(json.RawMessage(body), nil); err == nil {
			t.Fatalf("invalid request accepted: %s", body)
		}
	}
	inherited := []json.RawMessage{json.RawMessage(`{"id":"item_tx_same","role":"user","content":"parent"}`)}
	body := json.RawMessage(`{"model":"m","background":true,"input":[{"id":"item_tx_same","role":"user","content":"current"}]}`)
	if _, err := (BackgroundHistoryCodec{}).CompileBackground(body, inherited); err == nil {
		t.Fatal("duplicate identity across parent and current input accepted")
	}
}

func TestBackgroundHistoryCompileBoundsCombinedRequest(t *testing.T) {
	codec := BackgroundHistoryCodec{}
	item := json.RawMessage(`{"role":"user","content":"x"}`)
	inherited := make([]json.RawMessage, history.MaxContextItems)
	for i := range inherited {
		inherited[i] = item
	}
	body := json.RawMessage(`{"model":"m","background":true,"input":"next"}`)
	if _, err := codec.CompileBackground(body, inherited[:len(inherited)-1]); err != nil {
		t.Fatalf("exact item limit rejected: %v", err)
	}
	if _, err := codec.CompileBackground(body, inherited); err == nil {
		t.Fatal("combined item overflow accepted")
	}
	largeItem, _ := json.Marshal(map[string]string{"role": "user", "content": strings.Repeat("x", history.MaxContextBytes/2)})
	largeBody, _ := json.Marshal(map[string]any{"model": "m", "background": true, "input": "next", "instructions": strings.Repeat("x", history.MaxContextBytes/2)})
	if _, err := codec.CompileBackground(largeBody, []json.RawMessage{largeItem}); err == nil {
		t.Fatal("combined full-request overflow accepted")
	}
	if _, err := codec.CompileBackground(json.RawMessage(strings.Repeat(" ", history.MaxContextBytes+1)), nil); err == nil {
		t.Fatal("raw request overflow accepted")
	}
}
