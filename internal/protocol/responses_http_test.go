package protocol

import (
	"encoding/json"
	"strings"
	"testing"

	"minigateway/internal/protocol/adaptor/anthropic"
	"minigateway/internal/protocol/meta"
	"minigateway/internal/protocol/responses"
)

func TestResponsesEnvelopePreparesOnlyNativeHTTP(t *testing.T) {
	body := []byte(`{"model":"public","input":[{"type":"function_call_output","call_id":"a","output":"result"}],"instructions":"rules","max_output_tokens":32,"extension":{"keep":true}}`)
	r, err := ParseRequest(Responses, body)
	if err != nil {
		t.Fatal(err)
	}
	if r.Model() != "public" || r.LegacyMaxTokens() != 32 || !r.Requirements().Tools || r.Stream() {
		t.Fatalf("wrong operation facts: %+v", r)
	}
	native, ok := r.Responses()
	if !ok || !native.StoreEnabled() || !strings.Contains(native.PromptText(), "result") {
		t.Fatal("missing Responses projection")
	}
	native.Input[0] = '!'
	fresh, _ := r.Responses()
	if fresh.Input[0] != '[' {
		t.Fatal("mutable native projection escaped")
	}
	a, _ := Resolve("openai")
	prepared, problem := PrepareRequest(a, r, &meta.Meta{BaseURL: "https://upstream.test", ActualModel: "actual", ResponseID: "resp-public"})
	if problem != nil || prepared.Replayable || prepared.URL != "https://upstream.test/v1/responses" {
		t.Fatalf("wrong native preparation: %+v %+v", prepared, problem)
	}
	if _, ok := prepared.Decoder.(*responses.Decoder); !ok {
		t.Fatal("native Responses used a Chat decoder")
	}
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(prepared.Body, &fields)
	if string(fields["model"]) != `"actual"` || string(fields["extension"]) != `{"keep":true}` || fields["store"] != nil {
		t.Fatalf("native request fields changed: %s", prepared.Body)
	}
	for _, name := range []string{"anthropic", "gemini"} {
		other, _ := Resolve(name)
		if _, problem := PrepareRequest(other, r, &meta.Meta{ActualModel: "actual"}); problem == nil {
			t.Fatal("Responses was translated through another protocol")
		}
	}
	if Supports(&legacyResponsesDeclaration{}, r.Requirements()) {
		t.Fatal("legacy declaration invented native Responses support")
	}
	if Supports(a, Requirements{Operation: Responses, Transport: WebSocket}) {
		t.Fatal("HTTP declaration invented WebSocket support")
	}
}

type legacyResponsesDeclaration struct{ anthropic.Adaptor }

func (*legacyResponsesDeclaration) SupportsOperation(string, bool, bool) bool { return true }

func TestResponsesRequirementsIncludeNativeFunctionHistory(t *testing.T) {
	for _, input := range []string{`[{"type":"function_call","name":"f","call_id":"a","arguments":"{}"}]`, `[{"type":"function_call_output","call_id":"a","output":"x"}]`} {
		if !RequirementsFor(Responses, []byte(`{"input":`+input+`}`)).Tools {
			t.Fatal("hidden Responses tool history")
		}
	}
	if RequirementsFor(Responses, []byte(`{"input":"text"}`)).Tools {
		t.Fatal("text Responses input requires tools")
	}
}
