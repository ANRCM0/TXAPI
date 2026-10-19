package protocol

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"minigateway/internal/core"
	execv1 "minigateway/internal/foundation/contracts/execution/v1"
	"minigateway/internal/gateway"
	"minigateway/internal/protocol/adaptor"
	"minigateway/internal/protocol/adaptor/anthropic"
	"minigateway/internal/protocol/adaptor/gemini"
	"minigateway/internal/protocol/meta"
)

const anthropicConvertedFinish = "data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"}}\n\n"
const anthropicConvertedStop = "data: {\"type\":\"message_stop\"}\n\n"
const geminiConvertedFinish = "data: {\"candidates\":[{\"finishReason\":\"STOP\"}]}\n\n"

func convertedResponse(a adaptor.Adaptor, body io.Reader) (*httptest.ResponseRecorder, *core.Error) {
	rec := httptest.NewRecorder()
	e := core.New(core.Options{Client: &http.Client{Transport: staticResponse{body: body}}})
	defer e.Close()
	result := e.Execute(context.Background(), rec, gateway.ToCoreInput(execv1.Input{
		URL: "http://upstream.test", Stream: true,
		Decoder: NewDecoder(a, &meta.Meta{IsStream: true, ActualModel: "test-model"}),
	}))
	return rec, result.Error
}

func TestConvertedSSEMultilineUTF8AndFields(t *testing.T) {
	for name, tc := range map[string]struct {
		a    adaptor.Adaptor
		wire string
	}{
		"anthropic": {&anthropic.Adaptor{}, "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\ndata: \"content_block\":{\"type\":\"text\",\"text\":\"杭州\"}}\n\n" +
			"data: {\"type\":\"content_block_stop\",\"index\":0}\n\n" + anthropicConvertedFinish + anthropicConvertedStop},
		"gemini": {&gemini.Adaptor{}, "data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"杭州\"}]},\ndata: \"finishReason\":\"STOP\"}]}\n\n"},
	} {
		t.Run(name, func(t *testing.T) {
			wire := "\xef\xbb\xbf: keepalive\n\n\nid: event-1\nretry: 5\nx-extra: ignored\n" + tc.wire
			rec, err := convertedResponse(tc.a, chatByteReader{strings.NewReader(strings.ReplaceAll(wire, "\n", "\r\n"))})
			if err != nil || !strings.Contains(rec.Body.String(), "杭州") || strings.Count(rec.Body.String(), "data: [DONE]") != 1 {
				t.Fatalf("complete multiline conversion failed: %v %q", err, rec.Body.String())
			}
		})
	}
}

func TestConvertedSSEIncompleteEventsAndErrorsNeverEmitDone(t *testing.T) {
	for name, tc := range map[string]struct {
		a      adaptor.Adaptor
		prefix string
	}{
		"anthropic": {&anthropic.Adaptor{}, anthropicConvertedFinish},
		"gemini":    {&gemini.Adaptor{}, geminiConvertedFinish},
	} {
		for kind, tail := range map[string]string{
			"unclosed comment": ": pending\n",
			"unclosed done":    "data: [DONE]\n",
			"unclosed event":   "data: {}\n",
			"metadata error":   "event: error\ndata: {\"message\":\"failed\"}\n\n",
			"multiline error":  "data: {\"error\":\ndata: {\"message\":\"failed\"}}\n\n",
			"malformed JSON":   "data: {\ndata: broken}\n\n",
		} {
			t.Run(name+"/"+kind, func(t *testing.T) {
				rec, err := convertedResponse(tc.a, strings.NewReader(tc.prefix+tail))
				if err == nil || strings.Contains(rec.Body.String(), "[DONE]") {
					t.Fatalf("failed stream became success: %v %q", err, rec.Body.String())
				}
			})
		}
	}
	for name, wire := range map[string]string{
		"unclosed native stop": anthropicConvertedFinish + strings.TrimSuffix(anthropicConvertedStop, "\n"),
		"DONE bypass":          anthropicConvertedFinish + "data: [DONE]\n\n",
		"no finish":            anthropicConvertedStop,
	} {
		t.Run(name, func(t *testing.T) {
			rec, err := convertedResponse(&anthropic.Adaptor{}, strings.NewReader(wire))
			if err == nil || strings.Contains(rec.Body.String(), "[DONE]") {
				t.Fatalf("invalid native terminal accepted: %v %q", err, rec.Body.String())
			}
		})
	}
}

func TestConvertedSSEGeminiUsageAfterFinishAndEOF(t *testing.T) {
	wire := geminiConvertedFinish + "data: {\"usageMetadata\":{\"promptTokenCount\":3,\ndata: \"candidatesTokenCount\":4,\"totalTokenCount\":7}}\n\n"
	rec, err := convertedResponse(&gemini.Adaptor{}, strings.NewReader(wire))
	if err != nil || !strings.Contains(rec.Body.String(), `"total_tokens":7`) || strings.Count(rec.Body.String(), "[DONE]") != 1 {
		t.Fatalf("finish/usage/EOF policy changed: %v %q", err, rec.Body.String())
	}
}

func TestConvertedSSEAnthropicTerminalStopsBeforeEOF(t *testing.T) {
	rec, err := convertedResponse(&anthropic.Adaptor{}, &errorAfterData{data: []byte(anthropicConvertedFinish + anthropicConvertedStop)})
	if err != nil || strings.Count(rec.Body.String(), "[DONE]") != 1 {
		t.Fatalf("native terminal waited for EOF: %v %q", err, rec.Body.String())
	}
}

func TestConvertedSSEEventBudgetAndBoundary(t *testing.T) {
	for name, line := range map[string]string{
		"data copy": "data: " + strings.Repeat("x", maxConvertedSSEEventBytes/2),
		"metadata":  ":" + strings.Repeat("x", maxConvertedSSEEventBytes-1),
	} {
		t.Run(name, func(t *testing.T) {
			s := NewDecoder(&gemini.Adaptor{}, &meta.Meta{}).NewStream()
			frames, err := s.DecodeLine([]byte(line))
			if len(frames) != 0 {
				t.Fatal("partial event emitted")
			}
			if err == nil {
				_, err = s.DecodeLine(nil)
			}
			if err == nil {
				t.Fatal("event budget bypassed")
			}
		})
	}
	s := NewDecoder(&gemini.Adaptor{}, &meta.Meta{}).NewStream()
	for _, line := range []string{"data: {\"candidates\":[", "data: {\"finishReason\":\"STOP\"}]}"} {
		if frames, err := s.DecodeLine([]byte(line)); err != nil || len(frames) != 0 {
			t.Fatalf("event observed before boundary: %+v %v", frames, err)
		}
	}
	if frames, err := s.DecodeLine(nil); err != nil || len(frames) == 0 {
		t.Fatalf("event missing at boundary: %+v %v", frames, err)
	}
}

func TestConvertedSSEAttemptLocalAndManyMetadataLines(t *testing.T) {
	d := NewDecoder(&gemini.Adaptor{}, &meta.Meta{})
	partial, independent := d.NewStream(), d.NewStream()
	if frames, err := partial.DecodeLine([]byte(`data: {"error":`)); err != nil || len(frames) != 0 {
		t.Fatalf("partial event was observed: %+v %v", frames, err)
	}
	for _, line := range strings.Split(strings.TrimSuffix(geminiConvertedFinish, "\n"), "\n") {
		if _, err := independent.DecodeLine([]byte(line)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := independent.End(); err != nil {
		t.Fatalf("another attempt contaminated complete stream: %v", err)
	}
	if _, err := partial.End(); err == nil {
		t.Fatal("partial stream inherited another attempt's finish")
	}
	s := d.NewStream()
	line := []byte(":" + strings.Repeat("x", 1023))
	for i := 0; i <= maxConvertedSSEEventBytes/len(line); i++ {
		if frames, err := s.DecodeLine(line); err != nil {
			if len(frames) != 0 {
				t.Fatal("oversized event emitted")
			}
			return
		}
	}
	t.Fatal("many small metadata fields bypassed event limit")
}

func TestConvertedSSEAnthropicMultilineToolArguments(t *testing.T) {
	wire := "data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"call-1\",\"name\":\"lookup\",\"input\":{}}}\n\n" +
		"data: {\"type\":\"content_block_delta\",\"index\":0,\ndata: \"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{\\\"city\\\":\\\"杭州\\\"}\"}}\n\n" +
		"data: {\"type\":\"content_block_stop\",\"index\":0}\n\n" +
		strings.ReplaceAll(anthropicConvertedFinish, "end_turn", "tool_use") + anthropicConvertedStop
	rec, err := convertedResponse(&anthropic.Adaptor{}, chatByteReader{strings.NewReader(wire)})
	if err != nil {
		t.Fatal(err)
	}
	var arguments string
	for _, line := range strings.Split(rec.Body.String(), "\n") {
		if !strings.HasPrefix(line, "data: {") {
			continue
		}
		var chunk struct {
			Choices []struct {
				Delta struct {
					Calls []struct {
						Function struct {
							Arguments string `json:"arguments"`
						} `json:"function"`
					} `json:"tool_calls"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &chunk); err != nil {
			t.Fatal(err)
		}
		for _, choice := range chunk.Choices {
			for _, call := range choice.Delta.Calls {
				arguments += call.Function.Arguments
			}
		}
	}
	if arguments != `{"city":"杭州"}` {
		t.Fatalf("tool arguments changed: %q", arguments)
	}
}
