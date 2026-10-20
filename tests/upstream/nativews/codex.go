package main

import (
	"fmt"
	"strings"
)

const codexAnswer = "TXAPI fixture 你好🌏"

func codexModel(model string) bool {
	switch model {
	case "native-cli-text", "native-cli-function-result", "native-cli-custom", "native-cli-tool-search":
		return true
	}
	return false
}

// The deployed observer uses the client's real parent and tool result. It never
// executes a tool or treats a successful CLI exit as proof of native transport.
func (f *fixture) codexRound(body map[string]any, previous, id string, write func(any) error) bool {
	prewarm := body["generate"] == false
	parent, _ := body["previous_response_id"].(string)
	if parent != previous || prewarm != (previous == "") {
		f.reject("Codex prewarm or connection-local parent changed")
		return false
	}
	model := body["model"].(string)
	resultSeen := false
	for _, value := range body["input"].([]any) {
		item, _ := value.(map[string]any)
		if item["type"] == "function_call_output" || item["type"] == "custom_tool_call_output" || item["type"] == "tool_search_output" {
			if item["call_id"] != "call_codex_fixture" {
				f.reject("Codex result lost its call ID")
				return false
			}
			resultSeen = true
		}
	}
	output := []any{}
	if !prewarm {
		item := map[string]any{"type": "message", "id": "msg_" + id, "status": "completed", "role": "assistant",
			"content": []any{map[string]any{"type": "output_text", "text": codexAnswer, "annotations": []any{}}}}
		if !resultSeen {
			switch strings.TrimPrefix(model, "native-") {
			case "cli-function-result":
				item = map[string]any{"type": "function_call", "id": "fc_" + id, "call_id": "call_codex_fixture", "name": "request_user_input",
					"arguments": `{"questions":[{"header":"Fixture","id":"fixture","question":"Synthetic question","options":[{"label":"One","description":"Fixture one"},{"label":"Two","description":"Fixture two"}]}]}`}
			case "cli-custom":
				item = map[string]any{"type": "custom_tool_call", "id": "ctc_" + id, "call_id": "call_codex_fixture", "name": "apply_patch", "input": "TXAPI deliberately invalid patch 雪🌏; no paths or patch commands"}
			case "cli-tool-search":
				item = map[string]any{"type": "tool_search_call", "id": "tsc_" + id, "call_id": "call_codex_fixture", "execution": "client", "status": "completed", "arguments": map[string]any{"query": "txapi_fixture_never_registered_021", "limit": 1}}
			}
		}
		output = append(output, map[string]any{"type": "reasoning", "id": "rs_" + id, "summary": []any{map[string]string{"type": "summary_text", "text": "fixture summary 雪🌏"}}, "encrypted_content": "fixture-opaque-only"}, item)
	}
	events := []map[string]any{}
	add := func(value map[string]any) {
		value["sequence_number"] = len(events)
		value["stream_id"] = "main"
		events = append(events, value)
	}
	created := document(id, []any{})
	created["model"], created["status"], created["usage"] = model, "in_progress", nil
	add(map[string]any{"type": "response.created", "response": created})
	for index, value := range output {
		item := value.(map[string]any)
		pending := map[string]any{}
		for key, value := range item {
			pending[key] = value
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
			add(map[string]any{"type": "response.output_text.delta", "item_id": item["id"], "output_index": index, "content_index": 0, "delta": codexAnswer})
		}
		add(map[string]any{"type": "response.output_item.done", "output_index": index, "item": item})
	}
	terminal := document(id, output)
	terminal["model"] = model
	if prewarm {
		terminal["usage"] = map[string]int{"input_tokens": 0, "output_tokens": 0, "total_tokens": 0}
	}
	add(map[string]any{"type": "response.completed", "response": terminal})
	for _, event := range events {
		if err := write(event); err != nil {
			f.reject(fmt.Sprintf("Codex fixture write: %T", err))
			return false
		}
	}
	return true
}
