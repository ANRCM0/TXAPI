package responses

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"

	"minigateway/internal/core"
)

func TestRequestRetainsNativeOptionsAndRejectsAmbiguousIdentity(t *testing.T) {
	body := []byte(`{"model":"public","input":[{"type":"function_call_output","call_id":"a","output":"one"}],"previous_response_id":"resp-parent","stream":true,"background":true,"store":false,"tools":[{"type":"function","name":"lookup","parameters":{"type":"object"}}],"future_extension":{"retained":true}}`)
	r, err := ParseRequest(body)
	if err != nil {
		t.Fatal(err)
	}
	if !r.Stream || !r.Background || r.Store == nil || *r.Store || r.PreviousResponseID != "resp-parent" {
		t.Fatalf("lost options: %+v", r)
	}
	out, err := r.WithModel("actual")
	if err != nil {
		t.Fatal(err)
	}
	var before, after map[string]any
	_ = json.Unmarshal(body, &before)
	_ = json.Unmarshal(out, &after)
	before["model"] = "actual"
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("native fields changed: %s", out)
	}
	again, _ := r.WithModel("public")
	if !strings.Contains(string(again), `"model":"public"`) {
		t.Fatal("model mapping mutated request")
	}
	for _, bad := range []string{
		`{"model":"first","\u006dodel":"second"}`,
		`{"model":"m","stream":null}`,
		`{"model":"m","background":"true"}`,
		`{"model":"m","store":null}`,
		`{"model":"m","previous_response_id":"p","conversation":"c"}`,
		`{"model":"m"} {"model":"n"}`,
	} {
		if _, err := ParseRequest([]byte(bad)); err == nil {
			t.Fatalf("ambiguous request accepted: %s", bad)
		}
	}
}

func TestNativeJSONAndReadUseCoreWithoutChatConversion(t *testing.T) {
	response := `{"id":"resp-one","object":"response","status":"completed","model":"native","output":[{"type":"function_call","id":"item-one","call_id":"call-one","name":"lookup","arguments":"{\"city\":\"one\"}"}],"usage":{"input_tokens":7,"output_tokens":2,"total_tokens":9,"input_tokens_details":{"cached_tokens":3}},"future_extension":true}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/responses/resp-one" {
			t.Errorf("wrong native request: %s %s", r.Method, r.URL.Path)
		}
		_, _ = io.WriteString(w, response)
	}))
	defer server.Close()
	e := core.New(core.Options{})
	defer e.Close()
	rec := httptest.NewRecorder()
	result := e.Execute(context.Background(), rec, core.Input{Method: http.MethodGet, URL: server.URL + "/v1/responses/resp-one", Decoder: NewDecoder()})
	if result.Error != nil || !result.ProtocolComplete || result.Usage == nil || result.Usage.PromptTokens != 7 || result.Usage.TotalTokens != 9 || rec.Body.String() != response {
		t.Fatalf("native response changed: %+v; %s", result, rec.Body)
	}
}

func TestNativeSSEFramingToolsAndTerminalState(t *testing.T) {
	events := []string{
		": heartbeat\n\n",
		"event: response.created\ndata: {\"type\":\"response.created\",\"sequence_number\":0,\n" + "data: \"response\":{\"id\":\"resp-one\",\"object\":\"response\",\"status\":\"in_progress\"}}\n\n",
		"event: response.function_call_arguments.delta\ndata: {\"type\":\"response.function_call_arguments.delta\",\"sequence_number\":1,\"item_id\":\"fc-one\",\"output_index\":0,\"delta\":\"{\\\"city\\\":\"}\n\n",
		"event: response.completed\ndata: {\"type\":\"response.completed\",\"sequence_number\":2,\"response\":{\"id\":\"resp-one\",\"object\":\"response\",\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":3,\"output_tokens\":2,\"total_tokens\":5}}}\n\n",
	}
	for _, test := range []struct {
		name, payload string
		good          bool
	}{
		{"native", strings.Join(events, ""), true},
		{"truncated", strings.Join(events[:3], ""), false},
		{"chat terminator", events[1] + "data: [DONE]\n\n", false},
		{"failed", events[1] + "event: response.failed\ndata: {\"type\":\"response.failed\",\"sequence_number\":1,\"response\":{\"id\":\"resp-one\",\"object\":\"response\",\"status\":\"failed\"}}\n\n", false},
		{"wrong event", strings.Replace(strings.Join(events, ""), "event: response.completed", "event: response.incomplete", 1), false},
		{"sequence replay", strings.Replace(strings.Join(events, ""), `"sequence_number":2`, `"sequence_number":1`, 1), false},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, test.payload) }))
			defer server.Close()
			e := core.New(core.Options{})
			defer e.Close()
			rec := httptest.NewRecorder()
			result := e.Execute(context.Background(), rec, core.Input{URL: server.URL, Stream: true, Decoder: NewDecoder()})
			if (result.Error == nil) != test.good || result.ProtocolComplete != test.good {
				t.Fatalf("native SSE result: %+v; %s", result, rec.Body)
			}
			if test.good && (result.Usage == nil || result.Usage.TotalTokens != 5 || rec.Body.String() != test.payload) {
				t.Fatalf("lost native events or usage: %+v %s", result, rec.Body)
			}
			if strings.Contains(rec.Body.String(), "[DONE]") {
				t.Fatal("Responses was converted to Chat terminators")
			}
		})
	}
}

func TestStreamDecoderSessionsAreIndependentAndBounded(t *testing.T) {
	shared := NewDecoder()
	var workers sync.WaitGroup
	for i := range 20 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			s := shared.NewStream()
			line := fmt.Sprintf(`data: {"type":"response.completed","sequence_number":0,"response":{"id":"resp-%d","object":"response","status":"completed","usage":{"input_tokens":1,"output_tokens":0,"total_tokens":1}}}`, i)
			if _, err := s.DecodeLine([]byte(line)); err != nil {
				t.Error(err)
				return
			}
			frames, err := s.DecodeLine(nil)
			if err != nil || len(frames) != 1 || !frames[0].Complete {
				t.Errorf("shared decoder state: %v %+v", err, frames)
			}
			if _, err := s.End(); err != nil {
				t.Error(err)
			}
		}()
	}
	workers.Wait()
	s := shared.NewStream()
	if _, err := s.DecodeLine([]byte("data: " + strings.Repeat("x", maxEventBytes))); err == nil {
		t.Fatal("unbounded SSE event")
	}
	for _, usage := range []string{`{"input_tokens":-1,"output_tokens":1,"total_tokens":0}`, `{"input_tokens":1,"output_tokens":1,"total_tokens":3}`} {
		if _, _, err := ParseResponse([]byte(`{"id":"resp","object":"response","status":"completed","usage":` + usage + `}`)); err == nil {
			t.Fatalf("invalid usage accepted: %s", usage)
		}
	}
}
