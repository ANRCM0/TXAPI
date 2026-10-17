package protocol

import (
	"bytes"
	"encoding/json"
	"net/http"
	"reflect"
	"testing"

	"minigateway/internal/protocol/adaptor"
	"minigateway/internal/protocol/adaptor/anthropic"
	"minigateway/internal/protocol/canonical"
	"minigateway/internal/protocol/meta"
)

func TestOperationRequestOwnsPayloadAndCapabilityFacts(t *testing.T) {
	for _, operation := range []Operation{Chat, Embeddings} {
		t.Run(string(operation), func(t *testing.T) {
			body := []byte(`{"model":"m","messages":[{"role":"tool","content":"result","tool_call_id":"c"}],"input":[[1,2],[3]],"encoding_format":"base64","dimensions":256,"extension":{"keep":true}}`)
			original := bytes.Clone(body)
			request, err := ParseRequest(operation, body)
			if err != nil {
				t.Fatal(err)
			}
			body[0] = '!'
			copy := request.Body()
			copy[0] = '!'
			chat, isChat := request.Chat()
			embeddings, isEmbeddings := request.Embeddings()
			if isChat != (operation == Chat) || isEmbeddings != (operation == Embeddings) || request.Model() != "m" || !request.Requirements().Tools {
				t.Fatalf("mixed operation payload: %+v", request)
			}
			if isChat {
				chat.Messages[0] = '!'
				fresh, _ := request.Chat()
				if fresh.Messages[0] != '[' {
					t.Fatal("mutable Chat projection escaped")
				}
			} else {
				embeddings.Input[0] = '!'
				fresh, _ := request.Embeddings()
				if string(fresh.Input) != `[[1,2],[3]]` || string(fresh.EncodingFormat) != `"base64"` || string(fresh.Dimensions) != "256" {
					t.Fatal("embeddings payload changed")
				}
			}
			if !bytes.Equal(request.Body(), original) {
				t.Fatalf("body ownership lost: %s", request.Body())
			}
		})
	}
	for _, body := range []string{"", "null", "[]", `{"stream":true}`, `{"model":2}`, `{"model":"m","stream":"yes"}`} {
		for _, operation := range []Operation{Chat, Embeddings} {
			if _, err := ParseRequest(operation, []byte(body)); err == nil {
				t.Fatalf("invalid routing facts accepted: %s %s", operation, body)
			}
		}
	}
	if _, err := ParseRequest(Responses, []byte(`{"model":"m"}`)); err == nil {
		t.Fatal("Responses request without input was accepted")
	}
}

// This declares native embeddings but deliberately has unusable Chat codecs.
// Its ProtocolAdaptor name is not OpenAI, exercising the former Chat fallback.
type nativeEmbeddingsAdaptor struct {
	anthropic.Adaptor
	calls []string
}

func (a *nativeEmbeddingsAdaptor) Supports(r adaptor.Requirements) bool {
	return r.Operation == Embeddings && !r.Stream && !r.Tools
}
func (a *nativeEmbeddingsAdaptor) GetRequestURL(m *meta.Meta) (string, error) {
	a.calls = append(a.calls, "url")
	if m.Mode != meta.ModeEmbeddings || m.IsStream {
		panic("target metadata overrode operation facts")
	}
	m.Mode, m.IsStream = meta.ModeChatCompletions, true
	return "http://upstream.test/v1/embeddings", nil
}
func (a *nativeEmbeddingsAdaptor) SetupRequestHeader(r *http.Request, m *meta.Meta) error {
	a.calls = append(a.calls, "header")
	if m.Mode != meta.ModeEmbeddings || m.IsStream {
		panic("previous callback changed header metadata")
	}
	r.Header.Set("Authorization", "Bearer fixture")
	m.Mode, m.IsStream = meta.ModeChatCompletions, true
	return nil
}
func (a *nativeEmbeddingsAdaptor) ConvertRequest(body []byte, m *meta.Meta) ([]byte, error) {
	a.calls = append(a.calls, "native")
	if m.Mode != meta.ModeEmbeddings || m.IsStream {
		panic("previous callback changed native conversion metadata")
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(body, &object); err != nil {
		return nil, err
	}
	object["model"], _ = json.Marshal(m.ActualModel)
	m.Mode, m.IsStream = meta.ModeChatCompletions, true
	return json.Marshal(object)
}
func (*nativeEmbeddingsAdaptor) BuildRequest(*canonical.ChatRequest) ([]byte, error) {
	panic("embeddings entered Chat request conversion")
}
func (*nativeEmbeddingsAdaptor) ParseResponse([]byte) (*canonical.ChatResponse, error) {
	panic("embeddings entered Chat response conversion")
}

func TestPreparedEmbeddingsNeverUsesChatRequestOrResponse(t *testing.T) {
	a := &nativeEmbeddingsAdaptor{}
	body := []byte(`{"model":"alias","input":[[1,2],[3]],"dimensions":256,"encoding_format":"base64","extension":{"retain":true}}`)
	request, err := ParseRequest(Embeddings, body)
	if err != nil {
		t.Fatal(err)
	}
	m := &meta.Meta{OriginModel: "wrong", ActualModel: "mapped", Mode: meta.ModeChatCompletions, IsStream: true}
	input, prepareErr := PrepareRequest(a, request, m)
	if prepareErr != nil || input.Stream || !reflect.DeepEqual(a.calls, []string{"url", "native", "header"}) || m.Mode != meta.ModeChatCompletions || !m.IsStream {
		t.Fatalf("operation was not bound independently: %+v %v %v", input, prepareErr, a.calls)
	}
	var actual, expected map[string]json.RawMessage
	_ = json.Unmarshal(input.Body, &actual)
	_ = json.Unmarshal(body, &expected)
	expected["model"] = json.RawMessage(`"mapped"`)
	if !reflect.DeepEqual(actual, expected) {
		t.Fatalf("lost native fields: %s", input.Body)
	}
	response := []byte(`{"data":[{"index":0,"embedding":"AACAPw==","extension":true}],"usage":{"prompt_tokens":3,"total_tokens":3},"extra":"kept"}`)
	output, usage, decodeErr := input.Decoder.Decode(response)
	if decodeErr != nil || !bytes.Equal(output, response) || usage == nil || usage.PromptTokens != 3 || usage.CompletionTokens != 0 {
		t.Fatalf("native response changed: %s %+v %v", output, usage, decodeErr)
	}
	stream := input.Decoder.NewStream()
	if _, err := stream.DecodeLine([]byte("data: [DONE]")); err == nil {
		t.Fatal("embeddings acquired a Chat terminal")
	}
	if frames, err := stream.End(); err == nil || len(frames) != 0 {
		t.Fatal("embeddings manufactured SSE success")
	}
}

func TestPrepareEnforcesCapabilitiesBeforeCallingAdaptor(t *testing.T) {
	for _, body := range []string{`{"model":"m","input":"x","stream":true}`, `{"model":"m","tools":[{}]}`, `{"model":"m","messages":[{"role":"tool"}]}`} {
		a := &nativeEmbeddingsAdaptor{}
		request, err := ParseRequest(Embeddings, []byte(body))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := PrepareRequest(a, request, &meta.Meta{}); err == nil || len(a.calls) != 0 {
			t.Fatalf("unsupported preparation reached adaptor: %s %v", body, a.calls)
		}
	}
	for _, name := range []string{"openai", "anthropic", "gemini"} {
		a, _ := Resolve(name)
		if _, err := Prepare(a, []byte(`{"model":"m"}`), &meta.Meta{Mode: 99}); err == nil {
			t.Fatal("unknown mode fell back to Chat: " + name)
		}
		if name != "openai" {
			if _, err := Prepare(a, []byte(`{"model":"m","input":"x"}`), &meta.Meta{Mode: meta.ModeEmbeddings}); err == nil {
				t.Fatal("unregistered native embedding preparation: " + name)
			}
		}
	}
	legacy := &nativeEmbeddingsAdaptor{}
	if _, err := Prepare(legacy, []byte(`{"model":"m","input":"x"}`), &meta.Meta{Mode: meta.ModeEmbeddings, IsStream: true}); err == nil || len(legacy.calls) != 0 {
		t.Fatal("legacy metadata introduced unsupported embeddings SSE")
	}
	gemini, _ := Resolve("gemini")
	for _, field := range []string{`"tools":{}`, `"messages":{}`, `"messages":[{"role":"assistant","tool_calls":{}}]`} {
		if Supports(gemini, RequirementsFor(Chat, []byte(`{"model":"m",`+field+`}`))) {
			t.Fatal("malformed tool facts became text-only: " + field)
		}
	}
}
