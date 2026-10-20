package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	ws "github.com/coder/websocket"
	"minigateway/internal/model"
	"minigateway/internal/store"
)

const codexWSAnswer = "TXAPI fixture 你好🌏"

// Only the pinned CLI harness starts this process. The optional upstream
// resource factory remains confined to the existing production assembly test.
func TestResponsesCodexWSProcess(t *testing.T) {
	directory := os.Getenv("TXAPI_TEST_CODEX_WS_DIRECTORY")
	if directory == "" {
		t.Skip("only the bounded fixed Codex WS harness starts this helper")
	}
	kind := os.Getenv("TXAPI_TEST_CODEX_WS_CASE")
	if kind != "text" && kind != "function" && kind != "custom" && kind != "search" {
		t.Fatal("invalid controlled CLI case")
	}
	f := newWSAssemblyFixtureWithRecordLimit(t, true, true, 16)
	if _, err := f.st.UpdateChannel(f.channelID, &store.ChannelInput{Name: "codex-ws", Type: "openai", BaseURL: "https://api.openai.com", Models: []string{"cli-text", "cli-function-result", "cli-custom", "gpt-5.5"}, ModelMapping: map[string]string{"cli-text": "m", "cli-function-result": "m", "cli-custom": "m", "gpt-5.5": "m"}, Weight: 1, Status: "enabled"}); err != nil {
		t.Fatal(err)
	}
	channels, err := f.st.ChannelSnapshot()
	if err != nil || f.runtime.Publish(channels) != nil {
		t.Fatal("could not publish controlled CLI models", err)
	}
	var mu sync.Mutex
	var requests []json.RawMessage
	provider := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
			f.httpCalls.Add(1)
			http.Error(w, "the CLI fixture forbids HTTP fallback", 400)
			return
		}
		if r.Header.Get("Authorization") != "Bearer upstream-main-fixture" || r.Header.Get("Cookie") != "" {
			t.Error("CLI provider authentication or private-header boundary")
			w.WriteHeader(401)
			return
		}
		c, err := ws.Accept(w, r, &ws.AcceptOptions{OriginPatterns: []string{"*"}})
		if err != nil {
			t.Error(err)
			return
		}
		defer c.CloseNow()
		f.connections.Add(1)
		f.upstreamWorkers.Add(1)
		defer f.upstreamWorkers.Done()
		c.SetReadLimit(256 << 10)
		for round := 0; round < 5; round++ {
			message, data, err := c.Read(f.ctx)
			if err != nil {
				return
			}
			var request map[string]json.RawMessage
			if message != ws.MessageText || json.Unmarshal(data, &request) != nil || string(request["model"]) != `"m"` || string(request["store"]) != "false" || string(request["type"]) != `"response.create"` {
				t.Error("unexpected CLI native create")
				return
			}
			f.attempts.Add(1)
			mu.Lock()
			requests = append(requests, bytes.Clone(data))
			mu.Unlock()
			prewarm := string(request["generate"]) == "false"
			var items []map[string]json.RawMessage
			if json.Unmarshal(request["input"], &items) != nil {
				t.Error("CLI input was not a self-contained item array")
				return
			}
			resultSeen := false
			for _, item := range items {
				resultSeen = resultSeen || string(item["type"]) == `"function_call_output"` || string(item["type"]) == `"custom_tool_call_output"` || string(item["type"]) == `"tool_search_output"`
			}
			id := fmt.Sprintf("resp_cli_%d", round)
			output := []any{}
			if !prewarm {
				var item any = map[string]any{"type": "message", "id": fmt.Sprintf("msg_cli_%d", round), "status": "completed", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": codexWSAnswer, "annotations": []any{}}}}
				if !resultSeen {
					switch kind {
					case "function":
						item = map[string]any{"type": "function_call", "id": "fc_cli", "call_id": "call_cli", "name": "request_user_input", "arguments": `{"questions":[{"header":"Fixture","id":"fixture","question":"Synthetic question","options":[{"label":"One","description":"Fixture one"},{"label":"Two","description":"Fixture two"}]}]}`}
					case "custom":
						item = map[string]any{"type": "custom_tool_call", "id": "ctc_cli", "call_id": "call_cli", "name": "apply_patch", "input": "TXAPI deliberately invalid patch 雪🌏; no paths or patch commands"}
					case "search":
						item = map[string]any{"type": "tool_search_call", "id": "tsc_cli", "call_id": "call_cli", "execution": "client", "status": "completed", "arguments": map[string]any{"query": "txapi_fixture_never_registered_021", "limit": 1}}
					}
				}
				output = append(output, map[string]any{"type": "reasoning", "id": fmt.Sprintf("rs_cli_%d", round), "summary": []any{map[string]string{"type": "summary_text", "text": "fixture summary 雪🌏"}}, "encrypted_content": "fixture-opaque-only"}, item)
			}
			events := []map[string]any{}
			add := func(event map[string]any) { event["sequence_number"] = len(events); events = append(events, event) }
			document := map[string]any{"id": id, "object": "response", "model": "m", "status": "in_progress", "output": []any{}, "usage": nil}
			add(map[string]any{"type": "response.created", "response": document})
			for index, value := range output {
				item := value.(map[string]any)
				pending := map[string]any{}
				for k, v := range item {
					pending[k] = v
				}
				pending["status"] = "in_progress"
				switch item["type"] {
				case "reasoning":
					pending["summary"], pending["encrypted_content"] = []any{}, nil
				case "function_call":
					pending["arguments"] = ""
				case "custom_tool_call":
					pending["input"] = ""
				case "message":
					pending["content"] = []any{}
				}
				add(map[string]any{"type": "response.output_item.added", "output_index": index, "item": pending})
				switch item["type"] {
				case "reasoning":
					add(map[string]any{"type": "response.reasoning_summary_part.added", "item_id": item["id"], "output_index": index, "summary_index": 0, "part": map[string]string{"type": "summary_text", "text": ""}})
					add(map[string]any{"type": "response.reasoning_summary_text.delta", "item_id": item["id"], "output_index": index, "summary_index": 0, "delta": "fixture summary 雪🌏"})
					add(map[string]any{"type": "response.reasoning_summary_text.done", "item_id": item["id"], "output_index": index, "summary_index": 0, "text": "fixture summary 雪🌏"})
				case "function_call":
					add(map[string]any{"type": "response.function_call_arguments.delta", "item_id": item["id"], "output_index": index, "delta": item["arguments"]})
				case "custom_tool_call":
					add(map[string]any{"type": "response.custom_tool_call_input.delta", "item_id": item["id"], "output_index": index, "delta": item["input"]})
				case "message":
					add(map[string]any{"type": "response.content_part.added", "item_id": item["id"], "output_index": index, "content_index": 0, "part": map[string]any{"type": "output_text", "text": "", "annotations": []any{}}})
					add(map[string]any{"type": "response.output_text.delta", "item_id": item["id"], "output_index": index, "content_index": 0, "delta": codexWSAnswer})
				}
				add(map[string]any{"type": "response.output_item.done", "output_index": index, "item": item})
			}
			terminal := map[string]any{"id": id, "object": "response", "model": "m", "status": "completed", "output": output, "usage": map[string]int{"input_tokens": 2, "output_tokens": 1, "total_tokens": 3}}
			if prewarm {
				terminal["usage"] = map[string]int{"input_tokens": 0, "output_tokens": 0, "total_tokens": 0}
			}
			add(map[string]any{"type": "response.completed", "response": terminal})
			for _, event := range events {
				if c.Write(f.ctx, ws.MessageText, wsAssemblyJSON(event)) != nil {
					return
				}
			}
		}
	})
	f.nativeOverride.Store(&provider)
	data, _ := json.Marshal(map[string]string{"base": f.front.URL, "key": f.key})
	if err := os.WriteFile(filepath.Join(directory, "ready.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(directory, "done")); err == nil {
			break
		}
		select {
		case <-f.ctx.Done():
			t.Fatal("fixed CLI did not finish")
		case <-time.After(10 * time.Millisecond):
		}
	}
	f.waitReleased(t)
	mu.Lock()
	defer mu.Unlock()
	expected := 3
	if kind == "text" {
		expected = 2
	}
	if len(requests) != expected || f.attempts.Load() != int64(expected) || f.connections.Load() != 1 || f.httpCalls.Load() != 0 {
		t.Fatalf("WS create/reuse/HTTP fallback mismatch: %d/%d/%d/%d", len(requests), f.attempts.Load(), f.connections.Load(), f.httpCalls.Load())
	}
	for i, raw := range requests {
		var request struct {
			Generate *bool
			Previous string `json:"previous_response_id"`
			Input    []map[string]json.RawMessage
		}
		_ = json.Unmarshal(raw, &request)
		if i == 0 && (request.Generate == nil || *request.Generate || request.Previous != "") || i > 0 && request.Previous != fmt.Sprintf("resp_cli_%d", i-1) {
			t.Fatal("prewarm or native parent chain changed")
		}
		if i == 2 {
			var reasoning, result bool
			for _, item := range request.Input {
				reasoning = reasoning || string(item["type"]) == `"reasoning"` && string(item["encrypted_content"]) == `"fixture-opaque-only"`
				result = result || string(item["call_id"]) == `"call_cli"` && (string(item["type"]) == `"function_call_output"` || string(item["type"]) == `"custom_tool_call_output"` || string(item["type"]) == `"tool_search_output"`)
			}
			// Native parent reuse sends only the increment; the existing
			// response is held by the same upstream connection, not duplicated.
			if !result || reasoning {
				t.Fatal("tool result increment or native context duplication")
			}
		}
	}
	var rows []model.UsageAttemptReceipt
	if err := f.st.DB().Find(&rows).Error; err != nil || len(rows) != expected {
		t.Fatal("one usage receipt per real prewarm/generation", len(rows), err)
	}
	zero := 0
	for _, row := range rows {
		if !row.MayDispatch || row.TotalTokens == nil {
			t.Fatal("lost actual prewarm or generation usage")
		}
		if *row.TotalTokens == 0 {
			zero++
		}
	}
	if zero != 1 {
		t.Fatal("known zero prewarm was fabricated or lost")
	}
	var history int64
	if err := f.st.DB().Model(&model.ResponseHistory{}).Count(&history).Error; err != nil || history != 0 {
		t.Fatal("store=false CLI or prewarm leaked permanent history", history, err)
	}
	for _, table := range []string{"billing_reservations", "billing_attempts"} {
		var count int64
		if err := f.st.DB().Table(table).Count(&count).Error; err != nil || count != 0 {
			t.Fatal("disabled CLI changed financial authority", table, count, err)
		}
	}
}
