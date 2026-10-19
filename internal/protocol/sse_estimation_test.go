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
