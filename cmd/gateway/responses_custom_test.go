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
)

func customTools() []any {
	return []any{map[string]any{"type": "namespace", "name": "text", "tools": []any{map[string]any{"type": "custom", "name": "edit", "format": map[string]any{"type": "grammar", "syntax": "regex", "definition": ".+"}}}}}
}

func TestResponsesCustomHTTPJSONSSEAndBackgroundReplay(t *testing.T) {
	for _, mode := range []string{"json", "sse", "background-sse"} {
		t.Run(mode, func(t *testing.T) {
			var sends atomic.Int64
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				n := sends.Add(1)
				var body struct {
					Input              json.RawMessage
					Tools              json.RawMessage
					Stream, Background bool
				}
				if r.Method != "POST" || json.NewDecoder(r.Body).Decode(&body) != nil {
					t.Error("unexpected custom upstream request")
					w.WriteHeader(400)
					return
				}
				output := []any{map[string]any{"type": "custom_tool_call", "id": "ctc", "call_id": "one", "name": "edit", "namespace": "text", "input": "raw 雪🌏\nsecond", "future": true}}
				if n == 1 {
					if !bytes.Contains(body.Tools, []byte(`"type":"custom"`)) {
						t.Error("custom declaration lost")
					}
				} else {
					var input []map[string]json.RawMessage
					if json.Unmarshal(body.Input, &input) != nil || len(input) != 3 || string(input[1]["input"]) != `"raw 雪🌏\nsecond"` || string(input[1]["namespace"]) != `"text"` || string(input[2]["namespace"]) != `"text"` || string(input[1]["call_id"]) != string(input[2]["call_id"]) {
						t.Errorf("custom history association changed: %s", body.Input)
					}
				}
				doc := map[string]any{"id": fmt.Sprint("resp_private_", n), "object": "response", "model": "m", "status": "completed", "output": output, "usage": nil}
				if !body.Stream {
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write(wsAssemblyJSON(doc))
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				created := map[string]any{"id": doc["id"], "object": "response", "model": "m", "status": "in_progress", "output": []any{}, "usage": nil}
				events := []map[string]any{
					{"type": "response.created", "response": created},
					{"type": "response.output_item.added", "item": map[string]any{"type": "custom_tool_call", "id": "ctc", "call_id": "one", "name": "edit", "namespace": "text", "input": ""}},
					{"type": "response.custom_tool_call_input.delta", "delta": "raw 雪🌏\n"},
					{"type": "response.custom_tool_call_input.delta", "delta": "second"},
					{"type": "response.custom_tool_call_input.done", "input": "raw 雪🌏\nsecond"},
					{"type": "response.output_item.done", "item": output[0]},
					{"type": "response.completed", "response": doc},
				}
				for i, event := range events {
					event["sequence_number"] = i
					data := append([]byte("event: "+event["type"].(string)+"\ndata: "), wsAssemblyJSON(event)...)
					data = append(data, '\n', '\n')
					for _, b := range data {
						_, _ = w.Write([]byte{b})
					}
					if f, ok := w.(http.Flusher); ok {
						f.Flush()
					}
				}
			}))
			t.Cleanup(upstream.Close)
			h := newBackgroundHarness(t, upstream.URL)
			request := map[string]any{"model": "m", "input": "root", "tools": customTools(), "stream": mode != "json"}
			if mode == "background-sse" {
				request["background"] = true
				request["store"] = false
			}
			first := h.call("POST", "/v1/responses", string(wsAssemblyJSON(request)), "custom-root", "")
			if first.code != 200 || !bytes.Contains(first.body, []byte(`"type":"custom_tool_call"`)) {
				t.Fatal("custom result failed", first.code, string(first.body))
			}
			id := first.header.Get("X-TXAPI-Response-ID")
			if mode == "json" {
				id = first.id(t)
			} else if id == "" {
				for _, line := range strings.Split(string(first.body), "\n") {
					if strings.HasPrefix(line, "data: ") {
						var event struct{ Response struct{ ID string } }
						_ = json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &event)
						if event.Response.ID != "" {
							id = event.Response.ID
						}
					}
				}
			}
			if id == "" {
				t.Fatal("custom response identity missing")
			}
			if mode == "background-sse" {
				h.waitStage(id, "terminal")
				h.reopen()
				replay := h.call("GET", "/v1/responses/"+id+"?stream=true&starting_after=1", "", "", "")
				if replay.code != 200 || !bytes.Contains(replay.body, []byte("response.custom_tool_call_input.delta")) || !bytes.Contains(replay.body, []byte(`"input":"raw 雪🌏\nsecond"`)) || bytes.Contains(replay.body, []byte(`"sequence_number":1,`)) {
					t.Fatal("custom durable replay lost", replay.code, string(replay.body))
				}
				if sends.Load() != 1 {
					t.Fatal("background replay resubmitted custom generation")
				}
				return
			}
			get := h.call("GET", "/v1/responses/"+id, "", "", "")
			if get.code != 200 || !bytes.Contains(get.body, []byte(`"input":"raw 雪🌏\nsecond"`)) {
				t.Fatal("custom history output changed", get.code, string(get.body))
			}
			child := h.call("POST", "/v1/responses", string(wsAssemblyJSON(map[string]any{"model": "m", "previous_response_id": id, "input": []any{map[string]any{"type": "custom_tool_call_output", "call_id": "one", "name": "edit", "namespace": "text", "output": "result"}}})), "custom-child", "")
			if child.code != 200 || sends.Load() != 2 {
				t.Fatal("custom continuation failed", child.code, string(child.body), sends.Load())
			}
		})
	}
}

func TestResponsesCustomStoredCrossModeAndComponentRestart(t *testing.T) {
	f := newWSAssemblyFixtureWithHistory(t, true, true)
	c := f.dial(t)
	request := map[string]any{"type": "response.create", "model": "alias", "store": true, "input": "custom", "tools": customTools()}
	if err := c.Write(f.ctx, ws.MessageText, wsAssemblyJSON(request)); err != nil {
		t.Fatal(err)
	}
	parent := f.receiveStoredRound(t, c, "")
	<-f.observations
	_ = c.CloseNow()
	get := f.historyHTTP(t, f.key, "GET", "/v1/responses/"+parent, nil, 200)
	if !bytes.Contains(get["output"], []byte(`"type":"custom_tool_call"`)) || !bytes.Contains(get["output"], []byte(`"input":"raw 雪🌏\nsecond"`)) {
		t.Fatal("custom WS history output changed")
	}
	child := f.historyHTTP(t, f.key, "POST", "/v1/responses", map[string]any{"model": "alias", "previous_response_id": parent, "input": []any{map[string]any{"type": "custom_tool_call_output", "namespace": "text", "call_id": "call_fixture", "output": "custom-result"}}}, 200)
	a := <-f.observations
	input := historyWire(t, a, 3)
	if string(input[1]["type"]) != `"custom_tool_call"` || string(input[2]["type"]) != `"custom_tool_call_output"` || string(input[1]["namespace"]) != string(input[2]["namespace"]) {
		t.Fatal("HTTP custom association changed", string(a.JSON))
	}
	_ = json.Unmarshal(child["id"], &parent)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := f.runtime.Close(ctx); err != nil {
		t.Fatal(err)
	}
	f.front.Close()
	f.httpRuntime.Close()
	f.startPublicRuntimes(t, access.NewMemoryLimiter(), true)
	next := f.dial(t)
	sendStoredCreate(t, f, next, parent, "after-custom-restart", false)
	f.receiveStoredRound(t, next, parent)
	a = <-f.observations
	input = historyWire(t, a, 5)
	if string(input[1]["input"]) != `"raw 雪🌏\nsecond"` || string(input[2]["output"]) != `"custom-result"` || f.attempts.Load() != 3 {
		t.Fatal("reopened custom context changed", string(a.JSON))
	}
}

func TestResponsesCustomCanceledAfterDeltaCannotReplay(t *testing.T) {
	f := newWSAssemblyFixtureWithHistory(t, true, true)
	c := f.dial(t)
	if err := c.Write(f.ctx, ws.MessageText, wsAssemblyJSON(map[string]any{"type": "response.create", "model": "alias", "store": true, "input": "block-custom", "tools": customTools()})); err != nil {
		t.Fatal(err)
	}
	id := f.storedCreated(t, c)
	for {
		_, data, err := c.Read(f.ctx)
		if err != nil {
			t.Fatal(err)
		}
		var event struct{ Type, Delta string }
		if json.Unmarshal(data, &event) != nil {
			t.Fatal("invalid custom event")
		}
		if event.Type == "response.custom_tool_call_input.delta" {
			if event.Delta != "raw 雪🌏\n" {
				t.Fatal("custom delta changed")
			}
			break
		}
	}
	<-f.blocked
	_ = c.CloseNow()
	f.waitReleased(t)
	deadline := time.Now().Add(3 * time.Second)
	for {
		var row model.ResponseHistory
		if err := f.st.DB().Where("id = ?", id).First(&row).Error; err != nil {
			t.Fatal(err)
		}
		if row.Busy == "uncertain" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("partial custom generation lost uncertainty", row.Busy)
		}
		time.Sleep(time.Millisecond)
	}
	f.historyHTTP(t, f.key, "GET", "/v1/responses/"+id, nil, 409)
	retry := f.dial(t)
	sendStoredCreate(t, f, retry, id, "do-not-replay", false)
	f.storedRefusal(t, retry)
	f.waitReleased(t)
	if f.attempts.Load() != 1 {
		t.Fatal("partial custom generation was replayed")
	}
}
