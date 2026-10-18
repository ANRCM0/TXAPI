package responses

import (
	"strings"
	"testing"

	v2 "minigateway/internal/foundation/contracts/execution/v2"
)

func TestEnvelopeUsageDistinguishesMissingPartialAndZero(t *testing.T) {
	for _, tc := range []struct {
		name, usage string
		want        v2.Completeness
		invalid     bool
	}{
		{"missing", "", v2.Unknown, false},
		{"empty", `,"usage":{}`, v2.Unknown, false},
		{"null", `,"usage":null`, v2.Unknown, false},
		{"partial zero", `,"usage":{"input_tokens":0}`, v2.Partial, false},
		{"partial output", `,"usage":{"output_tokens":2,"total_tokens":3}`, v2.Partial, false},
		{"complete zero", `,"usage":{"input_tokens":0,"output_tokens":0,"total_tokens":0}`, v2.Complete, false},
		{"inconsistent", `,"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":3}`, "", true},
		{"negative partial", `,"usage":{"input_tokens":-1}`, "", true},
		{"overflow", `,"usage":{"input_tokens":9223372036854775807,"output_tokens":1,"total_tokens":0}`, "", true},
		{"ambiguous field", `,"usage":{"input_tokens":1,"input_tokens":2}`, "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			document := `{"id":"native","object":"response","status":"completed","future":true` + tc.usage + `}`
			out, err := NewDecoder().DecodeEnvelope([]byte(document))
			if (err != nil) != tc.invalid {
				t.Fatalf("usage contract differs: envelope=%+v err=%v", out, err)
			}
			if tc.invalid {
				return
			}
			if out.Usage == nil || out.Usage.Completeness != tc.want || string(out.Data) != document || !out.ProtocolTerminal {
				t.Fatalf("lost facts or changed raw delivery: %+v", out)
			}
			if strings.Contains(tc.name, "zero") && (out.Usage.Counts.InputTokens == nil || *out.Usage.Counts.InputTokens != 0) {
				t.Fatal("explicit zero became an unknown field")
			}
		})
	}
	// Existing v1 callers retain their original partial-usage projection. The
	// additive v2 path explicitly validates every known count before reporting it.
	if _, usage, err := NewDecoder().Decode([]byte(`{"id":"native","object":"response","status":"completed","usage":{"input_tokens":-1}}`)); err != nil || usage != nil {
		t.Fatal("v1 partial-usage semantics were silently changed")
	}
}

func TestEnvelopeStreamKeepsPerEventFactsAndRejectsConflictingTerminal(t *testing.T) {
	decoder := NewDecoderWithID("public-id").NewEnvelopeStream()
	created := `{"type":"response.created","sequence_number":0,"response":{"id":"native-id","object":"response","status":"in_progress","usage":{"input_tokens":2}}}`
	completed := `{"type":"response.completed","sequence_number":1,"response":{"id":"native-id","object":"response","status":"completed","usage":{"input_tokens":2,"output_tokens":0,"total_tokens":2}}}`
	for i, event := range []string{created, completed} {
		if _, err := decoder.DecodeEnvelopes([]byte("data: " + event)); err != nil {
			t.Fatal(err)
		}
		frames, err := decoder.DecodeEnvelopes(nil)
		if err != nil || len(frames) != 1 || frames[0].Usage == nil || frames[0].Native.ID != "native-id" || strings.Contains(string(frames[0].Data), "native-id") || !strings.Contains(string(frames[0].Data), "public-id") {
			t.Fatalf("event facts and public delivery did not remain separate: frames=%+v err=%v", frames, err)
		}
		if frames[0].ProtocolTerminal != (i == 1) {
			t.Fatal("terminal fact attached to the wrong event")
		}
	}
	if frames, err := decoder.EndEnvelopes(); err != nil || len(frames) != 0 {
		t.Fatalf("End repeated the terminal event: %+v %v", frames, err)
	}
	if _, err := decoder.DecodeEnvelopes([]byte("data: " + completed)); err == nil {
		t.Fatal("conflicting terminal accepted")
	}
}
