package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	ws "github.com/coder/websocket"
	"minigateway/internal/access"
	"minigateway/internal/model"
	"minigateway/internal/store"
)

func namespaceTools() []any {
	return []any{map[string]any{"type": "namespace", "name": "weather", "description": "雪", "tools": []any{map[string]any{"type": "function", "name": "lookup", "parameters": map[string]any{"type": "object"}}}}}
}

func TestResponsesFunctionNamespaceStoredHTTPWSAndReopenedContext(t *testing.T) {
	f := newWSAssemblyFixtureWithHistory(t, true, true)
	first := f.historyHTTP(t, f.key, "POST", "/v1/responses", map[string]any{"model": "alias", "input": "root", "tools": namespaceTools()}, 200)
	var parent string
	_ = json.Unmarshal(first["id"], &parent)
	a := <-f.observations
	if !bytes.Contains(a.JSON, []byte(`"type":"namespace"`)) {
		t.Fatal("HTTP declaration lost")
	}
	c := f.dial(t)
	request := map[string]any{"type": "response.create", "model": "alias", "store": true, "previous_response_id": parent, "input": "call", "tools": namespaceTools(), "tool_choice": map[string]any{"type": "function", "name": "lookup", "namespace": "weather"}}
	if err := c.Write(f.ctx, ws.MessageText, wsAssemblyJSON(request)); err != nil {
		t.Fatal(err)
	}
	second := f.receiveStoredRound(t, c, parent)
	a = <-f.observations
	historyWire(t, a, 3)
	if !bytes.Contains(a.JSON, []byte(`"namespace":"weather"`)) {
		t.Fatal("WS selection lost")
	}
	_ = c.CloseNow()
	secondDoc := f.historyHTTP(t, f.key, "GET", "/v1/responses/"+second, nil, 200)
	if !bytes.Contains(secondDoc["output"], []byte(`"namespace":"weather"`)) {
		t.Fatal("WS output lost in HTTP history", string(secondDoc["output"]))
	}
	third := f.historyHTTP(t, f.key, "POST", "/v1/responses", map[string]any{"model": "alias", "previous_response_id": second, "input": []any{map[string]any{"type": "function_call_output", "namespace": "weather", "name": "lookup", "call_id": "call_fixture", "output": "tool-result"}}}, 200)
	a = <-f.observations
	input := historyWire(t, a, 5)
	if string(input[3]["namespace"]) != `"weather"` || string(input[4]["namespace"]) != `"weather"` || string(input[3]["call_id"]) != string(input[4]["call_id"]) {
		t.Fatal("HTTP continuation lost namespace pair", string(a.JSON))
	}
	_ = json.Unmarshal(third["id"], &parent)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := f.runtime.Close(ctx); err != nil {
		t.Fatal(err)
	}
	f.front.Close()
	f.httpRuntime.Close()
	f.startPublicRuntimes(t, access.NewMemoryLimiter(), true)
	next := f.dial(t)
	sendStoredCreate(t, f, next, parent, "reopened", false)
	last := f.receiveStoredRound(t, next, parent)
	a = <-f.observations
	input = historyWire(t, a, 7)
	if string(input[3]["namespace"]) != `"weather"` || string(input[4]["namespace"]) != `"weather"` {
		t.Fatal("reopened context lost namespace")
	}
	items := f.historyHTTP(t, f.key, "GET", "/v1/responses/"+last+"/input_items?order=asc", nil, 200)
	if !bytes.Contains(items["data"], []byte(`"namespace":"weather"`)) {
		t.Fatal("input items lost namespace")
	}
	other, err := f.st.IssueToken(f.user, &store.TokenInput{ModelMode: access.All})
	if err != nil {
		t.Fatal(err)
	}
	f.historyHTTP(t, other.Key, "GET", "/v1/responses/"+second, nil, 404)
	f.historyHTTP(t, f.key, "POST", "/v1/responses", map[string]any{"model": "alias", "input": "reject", "tools": []any{map[string]any{"type": "namespace", "name": "weather", "tools": []any{map[string]any{"type": "custom", "name": "lookup"}}}}}, 400)
	if f.attempts.Load() != 4 || f.httpCalls.Load() != 2 {
		t.Fatal("replayed generation or refusal sent upstream")
	}
	var records []model.ResponseHistory
	if err := f.st.DB().Find(&records).Error; err != nil || len(records) != 4 {
		t.Fatal("history records", err, len(records))
	}
	for _, record := range records {
		if record.Busy != "" || bytes.Contains(record.Ciphertext, []byte("weather")) {
			t.Fatal("history encryption or terminal state")
		}
	}
}

func TestResponsesFunctionNamespaceConnectionLocalRound(t *testing.T) {
	f := newWSAssemblyFixture(t)
	c := f.dial(t)
	request := map[string]any{"type": "response.create", "model": "alias", "input": "call", "tools": namespaceTools()}
	if err := c.Write(f.ctx, ws.MessageText, wsAssemblyJSON(request)); err != nil {
		t.Fatal(err)
	}
	parent, _ := f.receiveRound(t, c)
	<-f.observations
	request = map[string]any{"type": "response.create", "model": "alias", "previous_response_id": parent, "input": []any{map[string]any{"type": "function_call_output", "namespace": "weather", "name": "lookup", "call_id": "call_fixture", "output": "result"}}}
	if err := c.Write(f.ctx, ws.MessageText, wsAssemblyJSON(request)); err != nil {
		t.Fatal(err)
	}
	f.receiveRound(t, c)
	observation := <-f.observations
	if !bytes.Contains(observation.JSON, []byte(`"namespace":"weather"`)) || observation.Previous != parent || f.attempts.Load() != 2 {
		t.Fatal("connection-local namespace result lost")
	}
}

func TestNativeBackgroundFunctionNamespaceSurvivesRestart(t *testing.T) {
	var posts atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/v1/responses" {
			t.Error("unexpected upstream action")
			w.WriteHeader(500)
			return
		}
		posts.Add(1)
		var body map[string]json.RawMessage
		if json.NewDecoder(r.Body).Decode(&body) != nil || string(body["background"]) != "true" || !bytes.Contains(body["tools"], []byte(`"type":"namespace"`)) || !bytes.Contains(body["input"], []byte(`"namespace":"weather"`)) || r.Header.Get("Authorization") != "Bearer upstream-private-secret" {
			t.Error("background namespace request lost")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"id":"resp_private","object":"response","model":"m","status":"completed","output":[{"type":"function_call","id":"fc","call_id":"call","namespace":"weather","name":"lookup","arguments":"{}"}],"usage":{"input_tokens":2,"output_tokens":1,"total_tokens":3}}`)
	}))
	t.Cleanup(upstream.Close)
	h := newBackgroundHarness(t, upstream.URL)
	body := string(wsAssemblyJSON(map[string]any{"model": "m", "background": true, "store": false, "input": []any{map[string]any{"type": "function_call_output", "call_id": "prior", "namespace": "weather", "output": "result"}}, "tools": namespaceTools()}))
	id := h.call("POST", "/v1/responses", body, "namespace-background", "").id(t)
	h.waitStage(id, "terminal")
	h.reopen()
	get := h.call("GET", "/v1/responses/"+id, "", "", "")
	if get.code != 200 || !bytes.Contains(get.body, []byte(`"namespace":"weather"`)) || bytes.Contains(get.body, []byte("resp_private")) {
		t.Fatal("background namespace projection lost", get.code, string(get.body))
	}
	if posts.Load() != 1 {
		t.Fatal("restart resubmitted known generation")
	}
}

func TestResponsesFunctionNamespaceHTTPJSONSSEHistory(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprint("stream=", stream), func(t *testing.T) {
			var sends atomic.Int64
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				n := sends.Add(1)
				var body struct {
					Input  []map[string]json.RawMessage
					Tools  json.RawMessage
					Stream bool
				}
				if json.NewDecoder(r.Body).Decode(&body) != nil {
					t.Error("invalid namespace request")
					w.WriteHeader(400)
					return
				}
				output := `[{"type":"message","id":"msg","role":"assistant","content":[{"type":"output_text","text":"done"}]}]`
				if n == 1 {
					if !bytes.Contains(body.Tools, []byte(`"type":"namespace"`)) {
						t.Error("namespace tools lost")
					}
					output = `[{"type":"function_call","id":"fc","call_id":"call","name":"lookup","namespace":"weather","arguments":"{}","future":"retained"}]`
				} else if n == 2 {
					if len(body.Input) != 3 || string(body.Input[1]["namespace"]) != `"weather"` || string(body.Input[2]["namespace"]) != `"weather"` || string(body.Input[1]["call_id"]) != string(body.Input[2]["call_id"]) || string(body.Input[1]["future"]) != `"retained"` {
						t.Errorf("namespace history lost: %+v", body.Input)
					}
				} else {
					t.Error("duplicate upstream send")
				}
				doc := fmt.Sprintf(`{"id":"resp_private_%d","object":"response","model":"m","status":"completed","output":%s,"usage":{"input_tokens":2,"output_tokens":1,"total_tokens":3}}`, n, output)
				if body.Stream {
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = fmt.Fprintf(w, "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":%s}\n\n", doc)
				} else {
					w.Header().Set("Content-Type", "application/json")
					_, _ = fmt.Fprint(w, doc)
				}
			}))
			t.Cleanup(upstream.Close)
			h := newBackgroundHarness(t, upstream.URL)
			root := h.call("POST", "/v1/responses", string(wsAssemblyJSON(map[string]any{"model": "m", "input": "root", "tools": namespaceTools(), "stream": stream})), "namespace-root", "")
			var id string
			if stream {
				_, data, ok := strings.Cut(string(root.body), "data: ")
				var event struct{ Response struct{ ID string } }
				if !ok || json.Unmarshal([]byte(strings.TrimSpace(data)), &event) != nil {
					t.Fatal("invalid namespace SSE", string(root.body))
				}
				id = event.Response.ID
			} else {
				id = root.id(t)
			}
			get := h.call("GET", "/v1/responses/"+id, "", "", "")
			if get.code != 200 || !bytes.Contains(get.body, []byte(`"namespace":"weather"`)) || !bytes.Contains(root.body, []byte(`"namespace":"weather"`)) {
				t.Fatal("namespace result lost", root.code, string(root.body), get.code, string(get.body))
			}
			child := h.call("POST", "/v1/responses", string(wsAssemblyJSON(map[string]any{"model": "m", "previous_response_id": id, "input": []any{map[string]any{"type": "function_call_output", "namespace": "weather", "call_id": "call", "output": "result"}}})), "namespace-child", "")
			if child.code != 200 || sends.Load() != 2 {
				t.Fatal("namespace continuation failed", child.code, string(child.body), sends.Load())
			}
		})
	}
}
