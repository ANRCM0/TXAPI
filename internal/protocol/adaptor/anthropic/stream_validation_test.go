package anthropic

import "testing"

func TestMessageDeltaRejectsMalformedPayloadWithoutPublishingUsage(t *testing.T) {
	for _, delta := range []string{`{"stop_reason":7}`, `"end_turn"`, `[]`, `false`} {
		t.Run(delta, func(t *testing.T) {
			s := newTranslator()
			chunks, err := s.ParseStreamLine([]byte(`data: {"type":"message_delta","delta":` + delta + `,"usage":{"output_tokens":4}}`))
			if err == nil || len(chunks) != 0 || s.finished || s.usage != nil {
				t.Fatalf("malformed delta accepted or changed state: chunks=%+v err=%v finished=%v usage=%+v", chunks, err, s.finished, s.usage)
			}
		})
	}
}

func TestMessageStartCannotRestartFinishedStream(t *testing.T) {
	s := newTranslator()
	for _, event := range []string{
		`{"type":"message_start","message":{"id":"original","model":"claude","usage":{"input_tokens":3}}}`,
		`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":2}}`,
	} {
		if _, err := s.ParseStreamLine([]byte("data: " + event)); err != nil {
			t.Fatal(err)
		}
	}
	chunks, err := s.ParseStreamLine([]byte(`data: {"type":"message_start","message":{"id":"replacement","model":"other","usage":{"input_tokens":100}}}`))
	if err == nil || len(chunks) != 0 || s.id != "original" || s.model != "claude" || s.usage.InputTokens != 3 || s.usage.OutputTokens != 2 {
		t.Fatalf("finished stream restarted: chunks=%+v err=%v translator=%+v", chunks, err, s)
	}
}

func TestMessageDeltaRetainsUsageOnlyAndDuplicateFinishPolicy(t *testing.T) {
	s := newTranslator()
	for i, event := range []string{
		`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":2}}`,
		`{"type":"message_delta","usage":{"output_tokens":3}}`,
		`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":4}}`,
		`{"type":"message_delta","delta":{"stop_reason":"end_turn"}}`,
	} {
		chunks, err := s.ParseStreamLine([]byte("data: " + event))
		if err != nil {
			t.Fatal(err)
		}
		finishes := 0
		for _, chunk := range chunks {
			if chunk.FinishReason != "" {
				finishes++
			}
		}
		wantFinishes := 0
		if i == 0 {
			wantFinishes = 1
		}
		if finishes != wantFinishes {
			t.Fatalf("event %d emitted %d finish chunks", i, finishes)
		}
		if i < 3 && (len(chunks) == 0 || chunks[len(chunks)-1].Usage == nil || chunks[len(chunks)-1].Usage.CompletionTokens != i+2) {
			t.Fatalf("event %d lost usage snapshot: %+v", i, chunks)
		}
		if i == 3 && len(chunks) != 0 {
			t.Fatalf("duplicate finish was republished: %+v", chunks)
		}
	}
}
