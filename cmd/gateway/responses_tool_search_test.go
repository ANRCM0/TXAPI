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

func searchTools() []any {
	return []any{map[string]any{"type": "tool_search", "execution": "client", "parameters": map[string]any{"type": "object"}}}
}

func loadedSearchTools() []any {
	return []any{map[string]any{"type": "namespace", "name": "crm", "tools": []any{map[string]any{"type": "function", "name": "lookup", "parameters": map[string]any{"type": "object"}, "defer_loading": true}}}}
}

func searchOutput() map[string]any {
	return map[string]any{"type": "tool_search_output", "execution": "client", "status": "completed", "call_id": "call_fixture", "tools": loadedSearchTools(), "future": true}
}

func TestResponsesSearchHTTPJSONSSEAndDurableBackgroundReplay(t *testing.T) {
	for _, mode := range []string{"json", "sse", "background-sse"} {
		t.Run(mode, func(t *testing.T) {
			var sends atomic.Int64
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				n := sends.Add(1)
				var body struct {
					Input  json.RawMessage
					Stream bool
					Tools  json.RawMessage
				}
				if r.Method != "POST" || json.NewDecoder(r.Body).Decode(&body) != nil {
					t.Error("unexpected search RPC")
					w.WriteHeader(400)
					return
				}
				var input []map[string]json.RawMessage
				if json.Unmarshal(body.Input, &input) != nil {
					t.Error("search context is not array")
					w.WriteHeader(400)
					return
				}
				var output any = map[string]any{"type": "tool_search_call", "id": "tsc_one", "execution": "client", "call_id": "call_fixture", "status": "completed", "arguments": map[string]any{"query": "雪🌏"}, "future": true}
				if n == 1 {
					if !bytes.Contains(body.Tools, []byte(`"execution":"client"`)) || len(input) != 2 || string(input[1]["type"]) != `"additional_tools"` {
						t.Error("search declaration/additional tools lost")
					}
				} else if n == 2 {
					if len(input) != 4 || string(input[2]["arguments"]) != `{"query":"雪🌏"}` || string(input[2]["call_id"]) != string(input[3]["call_id"]) || !bytes.Contains(input[3]["tools"], []byte(`"defer_loading":true`)) || string(input[2]["future"]) != "true" {
						t.Error("search call/output context changed", string(body.Input))
					}
					output = map[string]any{"type": "function_call", "id": "fc_loaded", "call_id": "loaded", "namespace": "crm", "name": "lookup", "arguments": "{}"}
				} else {
					if len(input) != 6 || string(input[4]["namespace"]) != string(input[5]["namespace"]) || string(input[4]["call_id"]) != string(input[5]["call_id"]) {
						t.Error("loaded function association changed", string(body.Input))
					}
					output = map[string]any{"type": "message", "id": "msg_final", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": "loaded result"}}}
				}
				doc := map[string]any{"id": fmt.Sprint("resp_private_", n), "object": "response", "model": "m", "status": "completed", "output": []any{output}, "usage": nil}
				if !body.Stream {
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write(wsAssemblyJSON(doc))
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				created := map[string]any{"id": doc["id"], "object": "response", "model": "m", "status": "in_progress", "output": []any{}, "usage": nil}
				for i, event := range []map[string]any{{"type": "response.created", "response": created}, {"type": "response.output_item.added", "item": output}, {"type": "response.output_item.done", "item": output}, {"type": "response.completed", "response": doc}} {
					event["sequence_number"] = i
					data := append([]byte("data: "), wsAssemblyJSON(event)...)
					data = append(data, '\n', '\n')
					for _, b := range data {
						_, _ = w.Write([]byte{b})
					}
					w.(http.Flusher).Flush()
				}
			}))
			t.Cleanup(upstream.Close)
			h := newBackgroundHarness(t, upstream.URL)
			request := map[string]any{"model": "m", "input": []any{map[string]any{"role": "user", "content": "find"}, map[string]any{"type": "additional_tools", "role": "developer", "tools": loadedSearchTools()}}, "tools": searchTools(), "stream": mode != "json"}
			if mode == "background-sse" {
				request["background"] = true
				request["store"] = false
			}
			first := h.call("POST", "/v1/responses", string(wsAssemblyJSON(request)), "search-root", "")
			if first.code != 200 || !bytes.Contains(first.body, []byte(`"type":"tool_search_call"`)) {
				t.Fatal(first.code, string(first.body))
			}
			id := first.header.Get("X-TXAPI-Response-ID")
			if mode == "json" {
				id = first.id(t)
			} else if id == "" {
				for _, line := range strings.Split(string(first.body), "\n") {
					if strings.HasPrefix(line, "data: ") {
						var e struct{ Response struct{ ID string } }
						_ = json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &e)
						if e.Response.ID != "" {
							id = e.Response.ID
						}
					}
				}
			}
			if id == "" {
				t.Fatal("search identity missing")
			}
			if mode == "background-sse" {
				h.waitStage(id, "terminal")
				h.reopen()
				replay := h.call("GET", "/v1/responses/"+id+"?stream=true&starting_after=1", "", "", "")
				if replay.code != 200 || !bytes.Contains(replay.body, []byte(`"type":"tool_search_call"`)) || sends.Load() != 1 {
					t.Fatal("search replay resubmitted/lost output", replay.code, string(replay.body), sends.Load())
				}
				return
			}
			get := h.call("GET", "/v1/responses/"+id, "", "", "")
			if get.code != 200 || !bytes.Contains(get.body, []byte(`"arguments":{"query":"雪🌏"}`)) {
				t.Fatal("search history changed", string(get.body))
			}
			child := h.call("POST", "/v1/responses", string(wsAssemblyJSON(map[string]any{"model": "m", "previous_response_id": id, "input": []any{searchOutput()}})), "search-load", "")
			if child.code != 200 {
				t.Fatal("loaded definitions rejected", child.code, string(child.body))
			}
			last := h.call("POST", "/v1/responses", string(wsAssemblyJSON(map[string]any{"model": "m", "previous_response_id": child.id(t), "input": []any{map[string]any{"type": "function_call_output", "namespace": "crm", "call_id": "loaded", "output": "answer"}}})), "search-result", "")
			if last.code != 200 || sends.Load() != 3 {
				t.Fatal(last.code, string(last.body), sends.Load())
			}
		})
	}
}

func TestResponsesSearchWSHTTPAndRebuiltComponentsKeepLoadedDefinitions(t *testing.T) {
	f := newWSAssemblyFixtureWithHistory(t, true, true)
	c := f.dial(t)
	if err := c.Write(f.ctx, ws.MessageText, wsAssemblyJSON(map[string]any{"type": "response.create", "model": "alias", "store": true, "input": "find", "tools": searchTools()})); err != nil {
		t.Fatal(err)
	}
	parent := f.receiveStoredRound(t, c, "")
	<-f.observations
	_ = c.CloseNow()
	get := f.historyHTTP(t, f.key, "GET", "/v1/responses/"+parent, nil, 200)
	if !bytes.Contains(get["output"], []byte(`"type":"tool_search_call"`)) {
		t.Fatal("search WS call not readable")
	}
	child := f.historyHTTP(t, f.key, "POST", "/v1/responses", map[string]any{"model": "alias", "previous_response_id": parent, "input": []any{searchOutput(), map[string]any{"type": "additional_tools", "role": "developer", "tools": loadedSearchTools()}}}, 200)
	a := <-f.observations
	items := historyWire(t, a, 4)
	if string(items[1]["call_id"]) != string(items[2]["call_id"]) || !bytes.Contains(items[2]["tools"], []byte(`"defer_loading":true`)) {
		t.Fatal("search result not compiled", string(a.JSON))
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
	sendStoredCreate(t, f, next, parent, "reopened-search", false)
	f.receiveStoredRound(t, next, parent)
	a = <-f.observations
	items = historyWire(t, a, 6)
	if !bytes.Contains(items[1]["arguments"], []byte("雪🌏")) || string(items[3]["type"]) != `"additional_tools"` || string(items[2]["future"]) != "true" || f.attempts.Load() != 3 {
		t.Fatal("search loaded history lost", string(a.JSON))
	}
}

func TestResponsesSearchCancelledAfterCallRetainsUnknownAndCannotReplay(t *testing.T) {
	f := newWSAssemblyFixtureWithHistory(t, true, true)
	c := f.dial(t)
	if err := c.Write(f.ctx, ws.MessageText, wsAssemblyJSON(map[string]any{"type": "response.create", "model": "alias", "store": true, "input": "block-search", "tools": searchTools()})); err != nil {
		t.Fatal(err)
	}
	id := f.storedCreated(t, c)
	for {
		_, data, err := c.Read(f.ctx)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(data, []byte(`"type":"tool_search_call"`)) {
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
			t.Fatal("search uncertainty lost", row.Busy)
		}
		time.Sleep(time.Millisecond)
	}
	f.historyHTTP(t, f.key, "GET", "/v1/responses/"+id, nil, 409)
	retry := f.dial(t)
	sendStoredCreate(t, f, retry, id, "do-not-replay-search", false)
	f.storedRefusal(t, retry)
	f.waitReleased(t)
	if f.attempts.Load() != 1 {
		t.Fatal("uncertain search generation replayed")
	}
}
