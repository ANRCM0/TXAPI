package protocol

import "testing"

func TestChatCompletionEstimateUsesCompleteSSEEvents(t *testing.T) {
	wire := "\xef\xbb\xbf: keepalive\r\n\r\nid: first\r\nevent: message\r\n" +
		"data: {\"choices\":[{\"delta\":\r\ndata: {\"content\":\"杭州\"}}]}\r\n\r\n" +
		"data: [DONE]\r\n\r\n" +
		"data: {\"choices\":[{\"delta\":{\"content\":\"after terminal\"}}]}\n\n"
	if got := CompletionTextFromResponse([]byte(wire)); got != "杭州" {
		t.Fatalf("multiline completion estimate = %q", got)
	}
	for _, tail := range []string{"", "\n"} {
		wire := "data: {\"choices\":[{\"delta\":{\"content\":\"truncated\"}}]}" + tail
		if got := CompletionTextFromResponse([]byte(wire)); got != "" {
			t.Fatalf("partial tail contributed completion text: %q", got)
		}
	}
}

func TestResponsesCompletionEstimateUsesCompleteSSEEvents(t *testing.T) {
	deltas := ": keepalive\r\n\r\nevent: response.output_text.delta\r\n" +
		"data: {\"type\":\"response.output_text.delta\",\r\ndata: \"delta\":\"杭州\"}\r\n\r\n" +
		"data: {\"type\":\"response.function_call_arguments.delta\",\n" +
		"data: \"delta\":\"{}\"}\n\n"
	if got := responsesCompletionText(true, []byte(deltas)); got != "杭州{}" {
		t.Fatalf("multiline delta estimate = %q", got)
	}
	terminal := "data: {\"type\":\"response.completed\",\n" +
		"data: \"response\":{\"output\":[{\"content\":[{\"text\":\"完整回答\"}]}]}}\n\n"
	if got := responsesCompletionText(true, []byte(deltas+terminal)); got != "完整回答" {
		t.Fatalf("terminal output should replace delta estimate, got %q", got)
	}
	progress := "data: {\"type\":\"response.in_progress\",\"response\":{\"output\":[{\"content\":[{\"text\":\"部分回答\"}]}]}}\n\n"
	if got := responsesCompletionText(true, []byte(progress+deltas+terminal)); got != "完整回答" {
		t.Fatalf("nonterminal snapshot hid final output: %q", got)
	}
	partial := "data: {\"type\":\"response.output_text.delta\",\"delta\":\"truncated\"}\n"
	if got := responsesCompletionText(true, []byte(deltas+partial)); got != "杭州{}" {
		t.Fatalf("partial event contributed output: %q", got)
	}
}

func TestResponsesCustomCompletionEstimateRetainsInputWithoutDoneDuplication(t *testing.T) {
	body := `{"output":[{"type":"custom_tool_call","input":"raw 雪🌏\nsecond"},{"type":"function_call","arguments":"{}"}]}`
	if got := responsesCompletionText(false, []byte(body)); got != "raw 雪🌏\nsecond{}" {
		t.Fatal("native custom JSON omitted from legacy estimate", got)
	}
	delta := "data: {\"type\":\"response.custom_tool_call_input.delta\",\n" +
		"data: \"delta\":\"raw 雪🌏\\nsecond\"}\n\n"
	done := "data: {\"type\":\"response.custom_tool_call_input.done\",\"input\":\"raw 雪🌏\\nsecond\"}\n\n"
	if got := responsesCompletionText(true, []byte(delta+done)); got != "raw 雪🌏\nsecond" {
		t.Fatal("custom done duplicated delta estimate", got)
	}
	terminal := "data: {\"type\":\"response.completed\",\"response\":" + body + "}\n\n"
	if got := responsesCompletionText(true, []byte(delta+done+terminal)); got != "raw 雪🌏\nsecond{}" {
		t.Fatal("custom terminal estimate did not replace deltas", got)
	}
	if got := responsesCompletionText(true, []byte(delta+`data: {"type":"response.custom_tool_call_input.delta","delta":"partial"}`)); got != "raw 雪🌏\nsecond" {
		t.Fatal("partial custom event counted", got)
	}
}

func TestSearchCompletionEstimatePreservesJSONArgumentsAndDeduplicatesSnapshots(t *testing.T) {
	item := `{"type":"tool_search_call","call_id":"one","arguments":{"query":"雪🌏"}}`
	body := `{"output":[` + item + `,{"type":"function_call","arguments":"{}"},{"type":"message","content":[{"text":"answer"}]}]}`
	if got := responsesCompletionText(false, []byte(body)); got != `{"query":"雪🌏"}{}answer` {
		t.Fatal("object search arguments hid ordinary output", got)
	}
	wire := "data: {\"type\":\"response.output_item.added\",\"item\":" + item + "}\n\n" +
		"data: {\"type\":\"response.output_item.done\",\"item\":" + item + "}\n\n"
	if got := responsesCompletionText(true, []byte(wire)); got != `{"query":"雪🌏"}` {
		t.Fatal("search snapshots double counted", got)
	}
	if got := responsesCompletionText(true, []byte(wire+"data: {\"type\":\"response.completed\",\"response\":"+body+"}\n\n")); got != `{"query":"雪🌏"}{}answer` {
		t.Fatal("search terminal did not replace snapshots", got)
	}
}
