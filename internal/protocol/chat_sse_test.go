package protocol

import (
	"bytes"
	"io"
	"strings"
	"testing"

	"minigateway/internal/protocol/adaptor/openai"
	"minigateway/internal/protocol/meta"
)

const chatFinishEvent = "data: {\"choices\":[{\"finish_reason\":\"stop\"}]}\n\n"

type chatByteReader struct{ reader io.Reader }

func (r chatByteReader) Read(p []byte) (int, error) { return r.reader.Read(p[:1]) }

func TestChatSSEMultilineFieldsAndSplitUTF8(t *testing.T) {
	wire := ": keepalive\r\n\r\nevent: message\r\nid: turn-1\r\nretry: 1000\r\nx-extension: unchanged\r\n" +
		"data: {\"choices\":[{\"delta\":{\"content\":\"杭州\",\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\"{}\"}}]},\r\n" +
		"data: \"finish_reason\":\"tool_calls\"}],\r\n" +
		"data: \"usage\":{\"prompt_tokens\":7,\"completion_tokens\":3,\"total_tokens\":10}}\r\n\r\n" +
		"event: message\r\nid: terminal\r\ndata: [DONE]\r\n\r\n"
	rec, err := callStreamResponse(chatByteReader{strings.NewReader(wire)})
	if err != nil {
		t.Fatalf("multiline stream failed: %v", err)
	}
	if want := strings.ReplaceAll(wire, "\r\n", "\n"); rec.Body.String() != want {
		t.Fatalf("fields or payload changed: got %q want %q", rec.Body.String(), want)
	}
	s := NewDecoder(&openai.Adaptor{}, &meta.Meta{}).NewStream()
	for _, line := range []string{`data: {"choices":[],`, `data: "usage":{"prompt_tokens":7,"completion_tokens":3,"total_tokens":10}}`} {
		if frames, err := s.DecodeLine([]byte(line)); err != nil || len(frames) != 0 {
			t.Fatalf("event observed before boundary: %+v %v", frames, err)
		}
	}
	frames, decodeErr := s.DecodeLine(nil)
	if decodeErr != nil || len(frames) != 1 || frames[0].Usage == nil || frames[0].Usage.TotalTokens != 10 {
		t.Fatalf("multiline usage lost: %+v %v", frames, decodeErr)
	}
}

func TestChatSSEFailedOrTruncatedEventsDoNotEmitDone(t *testing.T) {
	for name, tail := range map[string]string{
		"event error":         "event: error\ndata: {}\n\ndata: [DONE]\n\n",
		"multiline error":     "data: {\"error\":\ndata: {\"message\":\"failed\"}}\n\ndata: [DONE]\n\n",
		"malformed multiline": "data: {\ndata: broken}\n\ndata: [DONE]\n\n",
		"unterminated done":   "data: [DONE]\n",
		"unfinished event":    "data: {\"choices\":[]}",
	} {
		t.Run(name, func(t *testing.T) {
			rec, err := callStreamResponse(strings.NewReader(chatFinishEvent + tail))
			if err == nil || strings.Contains(rec.Body.String(), "[DONE]") {
				t.Fatalf("failed stream became success: %v %q", err, rec.Body.String())
			}
		})
	}
	rec, err := callStreamResponse(strings.NewReader("data: [DONE]\n\n"))
	if err == nil || strings.Contains(rec.Body.String(), "[DONE]") {
		t.Fatalf("DONE without finish became success: %v %q", err, rec.Body.String())
	}
}

func TestChatSSEEventBoundIncludesCommentsAndMetadata(t *testing.T) {
	s := NewDecoder(&openai.Adaptor{}, &meta.Meta{}).NewStream()
	line := append([]byte(": "), bytes.Repeat([]byte("x"), 1022)...)
	for i := 0; i < maxChatSSEEventBytes/len(line)+1; i++ {
		frames, err := s.DecodeLine(line)
		if len(frames) != 0 {
			t.Fatal("incomplete oversized event was emitted")
		}
		if err != nil {
			return
		}
	}
	t.Fatal("many short SSE fields bypassed the event budget")
}

func TestChatSSEEventsAreAttemptLocal(t *testing.T) {
	d := NewDecoder(&openai.Adaptor{}, &meta.Meta{})
	first, second := d.NewStream(), d.NewStream()
	if _, err := first.DecodeLine([]byte(`data: {"error":`)); err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(chatFinishEvent+"data: [DONE]\n\n", "\n") {
		frames, err := second.DecodeLine([]byte(line))
		if err != nil {
			t.Fatal(err)
		}
		if len(frames) > 0 && frames[0].Complete {
			break
		}
	}
	if _, err := second.End(); err != nil {
		t.Fatalf("other attempt contaminated valid event: %v", err)
	}
	if frames, err := first.End(); err == nil || len(frames) != 0 {
		t.Fatal("partial attempt inherited another attempt's terminal")
	}
}

func TestChatSSEBOMBlankEventsAndTerminalWhitespace(t *testing.T) {
	for name, prefix := range map[string]string{
		"BOM on its own line": "\xef\xbb\xbf\n\n\n",
		"BOM before data":     "\xef\xbb\xbf",
		"blank events":        "\n\n",
	} {
		t.Run(name, func(t *testing.T) {
			wire := prefix + chatFinishEvent + "data:  [DONE] \t\n\n"
			rec, err := callStreamResponse(strings.NewReader(wire))
			if err != nil || rec.Body.String() != wire {
				t.Fatalf("valid stream fields changed: %v %q", err, rec.Body.String())
			}
		})
	}
}

func TestChatSSETerminalDoesNotReadUntilEOF(t *testing.T) {
	// Any read past the fully delimited terminal returns an error. A decoder
	// that waits for EOF would turn this already complete stream into failure.
	wire := chatFinishEvent + "data: [DONE]\n\n"
	rec, err := callStreamResponse(&errorAfterData{data: []byte(wire)})
	if err != nil || rec.Body.String() != wire {
		t.Fatalf("terminal did not stop upstream reads: %v %q", err, rec.Body.String())
	}
}

func TestChatSSECompleteEventOwnsItsWireBytes(t *testing.T) {
	s := NewDecoder(&openai.Adaptor{}, &meta.Meta{}).NewStream()
	line := []byte(`data: {"choices":[{"delta":{"content":"first"}}]}`)
	if frames, err := s.DecodeLine(line); err != nil || len(frames) != 0 {
		t.Fatalf("partial event was emitted: %+v %v", frames, err)
	}
	first, err := s.DecodeLine(nil)
	if err != nil || len(first) != 1 {
		t.Fatalf("complete event missing: %+v %v", first, err)
	}
	want := string(line) + "\n\n"
	for i := range line {
		line[i] = 'x'
	}
	for _, next := range []string{": comment", "", `data: {"choices":[]}`, ""} {
		if _, err := s.DecodeLine([]byte(next)); err != nil {
			t.Fatal(err)
		}
	}
	if string(first[0].Data) != want {
		t.Fatalf("next event mutated prior event: %q", first[0].Data)
	}
}

func TestChatSSEEventBoundIncludesDataObservation(t *testing.T) {
	s := NewDecoder(&openai.Adaptor{}, &meta.Meta{}).NewStream()
	// Every data value is held in both the wire and the joined-data buffers.
	// Wire bytes alone fit; retaining both must exceed the event budget.
	line := append([]byte("data: "), bytes.Repeat([]byte("x"), maxChatSSEEventBytes/2)...)
	if frames, err := s.DecodeLine(line); err == nil || len(frames) != 0 {
		t.Fatal("joined-data copy bypassed the event budget")
	}
}
