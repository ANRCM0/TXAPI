package protocol

import (
	"testing"

	v1 "minigateway/internal/foundation/contracts/execution/v1"
	v4 "minigateway/internal/foundation/contracts/execution/v4"
	"minigateway/internal/protocol/adaptor"
	"minigateway/internal/protocol/adaptor/anthropic"
	"minigateway/internal/protocol/adaptor/gemini"
	"minigateway/internal/protocol/adaptor/openai"
	"minigateway/internal/protocol/meta"
)

func streamProgress(t *testing.T, stream any) v4.Progress {
	t.Helper()
	p, ok := stream.(v4.StreamProgress)
	if !ok {
		t.Fatalf("stream %T has no progress facts", stream)
	}
	return p.Progress()
}

func progressEvent(t *testing.T, stream v1.StreamDecoder, line string) v4.Progress {
	t.Helper()
	before := streamProgress(t, stream)
	if _, err := stream.DecodeLine([]byte(line)); err != nil {
		t.Fatal(err)
	}
	if got := streamProgress(t, stream); got != before {
		t.Fatalf("partial event advanced progress: %+v -> %+v", before, got)
	}
	if _, err := stream.DecodeLine(nil); err != nil {
		t.Fatal(err)
	}
	return streamProgress(t, stream)
}

func TestStreamProgressChatEffectiveContentAndEventBoundary(t *testing.T) {
	for name, line := range map[string]string{
		"text":           `data: {"choices":[{"delta":{"content":"你好"}}]}`,
		"tool name":      `data: {"choices":[{"delta":{"tool_calls":[{"function":{"name":"lookup"}}]}}]}`,
		"tool arguments": `data: {"choices":[{"delta":{"tool_calls":[{"function":{"arguments":"{"}}]}}]}`,
		"function":       `data: {"choices":[{"delta":{"function_call":{"arguments":"{}"}}}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			d := NewDecoder(&openai.Adaptor{}, &meta.Meta{})
			s, independent := d.NewStream(), d.NewStream()
			for i, neutral := range []string{
				": heartbeat",
				`data: {"id":"id","choices":[{"delta":{"role":"assistant"}}]}`,
				`data: {"choices":[{"delta":{"content":"","tool_calls":[{"id":"id","function":{}}]}}]}`,
				`data: {"usage":{"total_tokens":1},"choices":[]}`,
			} {
				if got := progressEvent(t, s, neutral); got.EffectiveOutput || got.CompleteEvents != uint64(i+1) {
					t.Fatalf("neutral event became effective: %+v", got)
				}
			}
			if got := progressEvent(t, s, line); !got.EffectiveOutput || got.CompleteEvents != 5 {
				t.Fatalf("actual output missing: %+v", got)
			}
			if got := progressEvent(t, s, ": later heartbeat"); !got.EffectiveOutput || got.CompleteEvents != 6 {
				t.Fatalf("effective fact was not sticky: %+v", got)
			}
			if got := streamProgress(t, independent); got != (v4.Progress{}) {
				t.Fatalf("progress leaked across streams: %+v", got)
			}
		})
	}
}

func TestStreamProgressChatEmptyTerminalAndInvalidEvent(t *testing.T) {
	s := NewDecoder(&openai.Adaptor{}, &meta.Meta{}).NewStream()
	if _, err := s.DecodeLine(nil); err != nil || streamProgress(t, s).CompleteEvents != 0 {
		t.Fatal("empty delimiter invented an event")
	}
	progressEvent(t, s, `data: {"choices":[{"delta":{},"finish_reason":"stop"}]}`)
	if got := progressEvent(t, s, "data: [DONE]"); got.EffectiveOutput || got.CompleteEvents != 2 {
		t.Fatalf("empty terminal invented output: %+v", got)
	}
	if _, err := s.End(); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []string{`data: {`, `data: {"choices":[{"delta":{"content":3}}]}`, "data: [DONE]"} {
		s = NewDecoder(&openai.Adaptor{}, &meta.Meta{}).NewStream()
		if _, err := s.DecodeLine([]byte(invalid)); err != nil {
			t.Fatal(err)
		}
		if _, err := s.DecodeLine(nil); err == nil || streamProgress(t, s) != (v4.Progress{}) {
			t.Fatalf("invalid event advanced facts: %q %v %+v", invalid, err, streamProgress(t, s))
		}
	}
}

func TestStreamProgressConvertedCanonicalFacts(t *testing.T) {
	for _, tc := range []struct {
		name       string
		a          adaptor.Adaptor
		neutral    []string
		text, tool string
	}{
		{"anthropic", &anthropic.Adaptor{}, []string{`: heartbeat`, `data: {"type":"ping"}`, `data: {"type":"message_start","message":{"id":"m","role":"assistant","usage":{"input_tokens":1}}}`},
			`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":"hello"}}`,
			`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"t","name":"lookup","input":{}}}`},
		{"gemini", &gemini.Adaptor{}, []string{`: heartbeat`, `data: {"candidates":[{"content":{"role":"model","parts":[]}}]}`, `data: {"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":0,"totalTokenCount":1}}`},
			`data: {"candidates":[{"content":{"parts":[{"text":"hello"}]}}]}`,
			`data: {"candidates":[{"content":{"parts":[{"functionCall":{"name":"lookup","args":{}}}]}}]}`},
	} {
		for kind, line := range map[string]string{"text": tc.text, "tool": tc.tool} {
			t.Run(tc.name+"/"+kind, func(t *testing.T) {
				d := NewDecoder(tc.a, &meta.Meta{})
				s := d.NewStream()
				for i, neutral := range tc.neutral {
					if got := progressEvent(t, s, neutral); got.EffectiveOutput || got.CompleteEvents != uint64(i+1) {
						t.Fatalf("converted neutral: %+v", got)
					}
				}
				if got := progressEvent(t, s, line); !got.EffectiveOutput || got.CompleteEvents != uint64(len(tc.neutral)+1) {
					t.Fatalf("canonical output not observed: %+v", got)
				}
				if got := streamProgress(t, d.NewStream()); got != (v4.Progress{}) {
					t.Fatal("converted progress leaked")
				}
			})
		}
	}
}

func TestStreamProgressSIWCObservedWrappers(t *testing.T) {
	body := []byte(`{"model":"m","input":[{"role":"user","content":"hi"}],"store":false,"stream":true}`)
	request, err := ParseRequest(Responses, body)
	if err != nil {
		t.Fatal(err)
	}
	var siwc v1.Adapter
	for _, registration := range Protocols(nil) {
		if registration.ID == "openai_siwc" {
			siwc = registration.Adapter
		}
	}
	prepared, failure := siwc.Prepare(request, v1.Target{ChannelType: "openai_siwc", BaseURL: "https://api.openai.com/v1", ActualModel: "m", OriginModel: "m"})
	if failure != nil {
		t.Fatal(failure)
	}
	if got := progressEvent(t, prepared.Decoder.NewStream(), `data: {"type":"response.output_text.delta","delta":"hello"}`); !got.EffectiveOutput || got.CompleteEvents != 1 {
		t.Fatalf("SIWC observed stream lost facts: %+v", got)
	}
}

func TestStreamProgressRejectsUnfinishedNativeTerminal(t *testing.T) {
	s := NewDecoder(&anthropic.Adaptor{}, &meta.Meta{}).NewStream()
	if _, err := s.DecodeLine([]byte(`data: {"type":"message_stop"}`)); err != nil {
		t.Fatal(err)
	}
	frames, err := s.DecodeLine(nil)
	if err == nil || len(frames) != 0 || streamProgress(t, s) != (v4.Progress{}) {
		t.Fatalf("unfinished native terminal advanced completion: frames=%+v progress=%+v error=%v", frames, streamProgress(t, s), err)
	}
}
