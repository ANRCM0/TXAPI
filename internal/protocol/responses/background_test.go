package responses

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	bg "minigateway/internal/foundation/contracts/background/v1"
	execution "minigateway/internal/foundation/contracts/execution/v1"
)

func backgroundTarget() execution.Target {
	return execution.Target{ChannelID: 1, ChannelType: "openai", BaseURL: "https://native.test", APIKey: "private-key", OriginModel: "m", ActualModel: "native-m", Operation: execution.Responses, ResponseID: "resp_local", CredentialIDValue: 7}
}

func TestBackgroundCodecPreservesNativeSubmitAndSeparatesRPCTermination(t *testing.T) {
	body := []byte(`{"model":"m","background":true,"store":false,"input":"private input","tools":[{"type":"function","name":"f","parameters":{"type":"object"}}],"custom_extension":{"x":1}}`)
	prepared, problem := (BackgroundCodec{}).Prepare(bg.RemoteCommand{Action: bg.RemoteSubmit, Body: body}, backgroundTarget())
	if problem != nil || prepared.Input.Method != "POST" || prepared.Input.URL != "https://native.test/v1/responses" || prepared.Input.Replayable || prepared.Input.Stream {
		t.Fatalf("wrong native submission: %+v %+v", prepared.Input, problem)
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(prepared.Input.Body, &fields) != nil || string(fields["model"]) != `"native-m"` || string(fields["background"]) != "true" || string(fields["store"]) != "false" || len(fields["tools"]) == 0 || len(fields["custom_extension"]) == 0 {
		t.Fatal("background/model/store/native extension was rewritten incorrectly")
	}
	if prepared.Input.Header.Get("Authorization") != "Bearer private-key" || !bytes.Contains(body, []byte(`"model":"m"`)) {
		t.Fatal("credential or input ownership changed")
	}
	for _, status := range []string{"queued", "in_progress", "completed", "incomplete", "failed", "cancelled"} {
		wire := []byte(`{"id":"resp_provider","object":"response","status":"` + status + `","output":[],"usage":{"input_tokens":2,"output_tokens":1,"total_tokens":3}}`)
		out, usage, e := prepared.Input.Decoder.Decode(wire)
		if e != nil || usage == nil || usage.TotalTokens != 3 || strings.Contains(string(out), "resp_provider") || !bytes.Contains(out, []byte("resp_local")) {
			t.Fatalf("decode %s: %s %v", status, out, e)
		}
		observation := prepared.Progress.Drain()
		if observation.RemoteID != "resp_provider" || len(observation.Document) == 0 || status == "queued" && observation.State != bg.RemoteAccepted || status == "in_progress" && observation.State != bg.RemoteRunning {
			t.Fatalf("RPC completion was promoted to task completion: %+v", observation)
		}
		if again := prepared.Progress.Drain(); again.RemoteID != "" || len(again.Document) != 0 {
			t.Fatal("Drain replayed an old observation")
		}
		if status == "queued" || status == "in_progress" {
			if _, _, e = NewDecoder().Decode(wire); e == nil {
				t.Fatal("background support weakened the synchronous decoder")
			}
		}
	}
}

func TestBackgroundCodecBindsControlIDAndRejectsUnsupportedInputs(t *testing.T) {
	for _, action := range []bg.RemoteAction{bg.RemoteInspect, bg.RemoteCancel} {
		prepared, problem := (BackgroundCodec{}).Prepare(bg.RemoteCommand{Action: action, RemoteID: "resp_bound"}, backgroundTarget())
		if problem != nil || !strings.Contains(prepared.Input.URL, "/v1/responses/resp_bound") || action == bg.RemoteCancel && (prepared.Input.Method != "POST" || !strings.HasSuffix(prepared.Input.URL, "/cancel")) {
			t.Fatal("control is not bound to the original native resource")
		}
		if _, _, e := prepared.Input.Decoder.Decode([]byte(`{"id":"other","object":"response","status":"completed","output":[]}`)); e == nil {
			t.Fatal("control accepted a foreign native ID")
		}
	}
	for _, command := range []bg.RemoteCommand{
		{Action: bg.RemoteInspect, RemoteID: "../other"},
		{Action: bg.RemoteCancel, RemoteID: "x?api_key=secret"},
		{Action: bg.RemoteResume, RemoteID: "../resp_x"},
		{Action: bg.RemoteSubmit, Body: []byte(`{"model":"m","background":false,"input":"x"}`)},
		{Action: bg.RemoteSubmit, Body: []byte(`{"model":"m","background":true,"stream":null,"input":"x"}`)},
		{Action: bg.RemoteSubmit, Body: []byte(`{"model":"m","background":true,"previous_response_id":"resp_x","input":"x"}`)},
		{Action: bg.RemoteSubmit, Body: []byte(`{"model":"m","background":true,"input":"x","tools":[{"type":"web_search"}]}`)},
	} {
		if _, problem := (BackgroundCodec{}).Prepare(command, backgroundTarget()); problem == nil {
			t.Fatalf("unsupported native command was accepted: %s", command.Action)
		}
	}
	for _, changed := range []func(*execution.Target){
		func(t *execution.Target) { t.ChannelType = "anthropic" },
		func(t *execution.Target) { t.BaseURL = "https://host.test/?credential=x" },
		func(t *execution.Target) { t.BaseURL = "https://user:password@host.test" },
		func(t *execution.Target) { t.ResponseID = "" },
	} {
		target := backgroundTarget()
		changed(&target)
		if _, problem := (BackgroundCodec{}).Prepare(bg.RemoteCommand{Action: bg.RemoteInspect, RemoteID: "resp_x"}, target); problem == nil {
			t.Fatal("unsafe native target accepted")
		}
	}
}
