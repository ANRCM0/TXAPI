package responses

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"

	v2 "minigateway/internal/foundation/contracts/execution/v2"
)

func compactionDocument(status, output, usage string) string {
	return `{"id":"native","object":"response","status":"` + status + `","output":` + output + `,"metadata":{"response_id":"business"}` + usage + `}`
}

func compactionStreamEvent(t *testing.T, stream *streamDecoder, event string) ([]byte, bool, error) {
	t.Helper()
	if _, err := stream.DecodeLine([]byte("data: " + event)); err != nil {
		return nil, false, err
	}
	frames, err := stream.DecodeLine(nil)
	if err != nil {
		return nil, false, err
	}
	if len(frames) != 1 {
		t.Fatalf("expected one complete SSE event, got %d", len(frames))
	}
	if stream.Progress().EffectiveOutput {
		t.Fatal("opaque compaction claimed effective text/tool output")
	}
	return frames[0].Data, frames[0].Complete, nil
}

func TestCompactionDecoderJSONRequiresOneUsableResult(t *testing.T) {
	for _, output := range []string{`[]`, `[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"fake compact"}]}]`,
		`[{"type":"compaction","id":"bare"}]`, `[` + compactionFixture + `,` + compactionFixture + `]`} {
		if _, _, err := NewCompactionDecoderWithID("public").Decode([]byte(compactionDocument("completed", output, ""))); err == nil {
			t.Fatal("completed compact accepted without exactly one usable result", output)
		}
	}
	for _, tc := range []struct {
		usage string
		want  v2.Completeness
	}{
		{"", v2.Unknown}, {`,"usage":{"input_tokens":4}`, v2.Partial},
		{`,"usage":{"input_tokens":4,"output_tokens":1,"total_tokens":5}`, v2.Complete},
	} {
		out, err := NewCompactionDecoderWithID("public").DecodeEnvelope([]byte(compactionDocument("completed", `[`+compactionFixture+`]`, tc.usage)))
		if err != nil || !out.ProtocolTerminal || out.Failed || out.Native.Status != "completed" || out.Usage == nil || out.Usage.Completeness != tc.want {
			t.Fatal(out, err)
		}
		if tc.want == v2.Partial && (out.Usage.Counts.OutputTokens != nil || out.Usage.Counts.TotalTokens != nil) {
			t.Fatal("partial compaction usage was completed")
		}
		var document map[string]json.RawMessage
		_ = json.Unmarshal(out.Data, &document)
		var items []json.RawMessage
		_ = json.Unmarshal(document["output"], &items)
		var before, after any
		_ = json.Unmarshal([]byte(compactionFixture), &before)
		_ = json.Unmarshal(items[0], &after)
		if !reflect.DeepEqual(before, after) || string(document["id"]) != `"public"` || !bytes.Contains(document["metadata"], []byte("business")) {
			t.Fatal("compaction identity rewrite changed opaque metadata", string(out.Data))
		}
	}
}

func TestCompactionDecoderSSERequiresOneDoneAndRealTerminal(t *testing.T) {
	added := `{"type":"response.output_item.added","output_index":0,"item":{"type":"compaction","id":"cmp_fixture"}}`
	done := `{"type":"response.output_item.done","output_index":0,"item":` + compactionFixture + `}`
	for _, output := range []string{`[]`, `[` + compactionFixture + `]`} {
		s := NewCompactionDecoderWithID("public").NewStream().(*streamDecoder)
		for _, event := range []string{added, `{"type":"response.compaction.in_progress","item_id":"cmp_fixture"}`, done} {
			if _, terminal, err := compactionStreamEvent(t, s, event); err != nil || terminal {
				t.Fatal("item progress became terminal", terminal, err)
			}
		}
		raw, terminal, err := compactionStreamEvent(t, s, `{"type":"response.completed","response":`+compactionDocument("completed", output, `,"usage":{"input_tokens":2}`)+`}`)
		if err != nil || !terminal || !bytes.Contains(raw, []byte("business")) {
			t.Fatal(string(raw), terminal, err)
		}
		if _, err := s.End(); err != nil {
			t.Fatal(err)
		}
		if s.owner.Observation().Usage != nil {
			t.Fatal("partial usage became a complete v1 quantity")
		}
	}
	t.Run("terminal snapshot alone", func(t *testing.T) {
		s := NewCompactionDecoderWithID("").NewStream().(*streamDecoder)
		if _, _, err := compactionStreamEvent(t, s, `{"type":"response.completed","response":`+compactionDocument("completed", `[`+compactionFixture+`]`, "")+`}`); err == nil {
			t.Fatal("missing item.done concealed by terminal snapshot")
		}
	})
	t.Run("duplicate done", func(t *testing.T) {
		s := NewCompactionDecoderWithID("").NewStream().(*streamDecoder)
		_, _, err := compactionStreamEvent(t, s, done)
		if err != nil {
			t.Fatal(err)
		}
		_, _, err = compactionStreamEvent(t, s, done)
		if err == nil {
			_, _, err = compactionStreamEvent(t, s, `{"type":"response.completed","response":`+compactionDocument("completed", `[]`, "")+`}`)
		}
		if err == nil {
			t.Fatal("duplicate compaction item.done accepted")
		}
	})
	for _, malformed := range []string{`{"type":"compaction","id":"bare"}`, `{"type":"compaction","encrypted_content":null}`} {
		s := NewCompactionDecoderWithID("").NewStream().(*streamDecoder)
		if _, _, err := compactionStreamEvent(t, s, `{"type":"response.output_item.done","item":`+malformed+`}`); err == nil {
			t.Fatal("malformed done accepted")
		}
	}
	t.Run("done without terminal", func(t *testing.T) {
		s := NewCompactionDecoderWithID("").NewStream().(*streamDecoder)
		if _, _, err := compactionStreamEvent(t, s, done); err != nil {
			t.Fatal(err)
		}
		if _, err := s.End(); err == nil {
			t.Fatal("done concealed truncated stream")
		}
	})
	t.Run("unterminated terminal event", func(t *testing.T) {
		s := NewCompactionDecoderWithID("").NewStream().(*streamDecoder)
		_, _, _ = compactionStreamEvent(t, s, done)
		_, _ = s.DecodeLine([]byte(`data: {"type":"response.completed","response":` + compactionDocument("completed", `[]`, "") + `}`))
		if _, err := s.End(); err == nil {
			t.Fatal("EOF dispatched an incomplete terminal event")
		}
	})
}

func TestCompactionDecoderFailureAndIncompleteRemainDistinct(t *testing.T) {
	for _, status := range []string{"failed", "incomplete", "cancelled"} {
		body := compactionDocument(status, `[]`, `,"error":{"code":"private_code","message":"private_message"}`)
		d := NewCompactionDecoderWithID("public")
		out, err := d.DecodeEnvelope([]byte(body))
		if err != nil || !out.ProtocolTerminal || out.Native.Status != status || out.Failed != (status != "incomplete") || bytes.Contains(out.Data, []byte("private_")) {
			t.Fatal(out, err)
		}
		s := NewCompactionDecoderWithID("public").NewStream().(*streamDecoder)
		raw, complete, err := compactionStreamEvent(t, s, `{"type":"response.`+status+`","response":`+body+`}`)
		if err != nil || !complete || s.owner.Observation().Status != status || bytes.Contains(raw, []byte("private_")) {
			t.Fatal(string(raw), complete, err)
		}
		if _, err := s.End(); err != nil {
			t.Fatal(err)
		}
	}
}
