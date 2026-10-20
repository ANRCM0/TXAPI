package protocol

import (
	"bytes"
	"testing"
)

func TestEmbeddingsUsageRequiresExplicitInputAndTotal(t *testing.T) {
	for _, usage := range []string{`{}`, `{"prompt_tokens":11}`, `{"total_tokens":11}`, `{"prompt_tokens":null,"total_tokens":0}`, `{"prompt_tokens":0,"total_tokens":null}`, `{"Prompt_Tokens":0,"Total_Tokens":0}`} {
		t.Run(usage, func(t *testing.T) {
			body := []byte(`{"data":[{"embedding":"AQID"}],"usage":` + usage + `,"extension":"keep"}`)
			wire, observed, err := (embeddingsDecoder{}).Decode(body)
			if err != nil || observed != nil || !bytes.Equal(body, wire) {
				t.Fatalf("incomplete usage acquired a financial receipt: usage=%+v error=%v wire=%s", observed, err, wire)
			}
		})
	}
}

func TestEmbeddingsUsageValidatesKnownCountsAndPreservesWire(t *testing.T) {
	for _, tc := range []struct {
		name, usage string
		known       bool
		input       int
	}{
		{"input only", `{"prompt_tokens":11,"total_tokens":11}`, true, 11},
		{"zero", `{"prompt_tokens":0,"total_tokens":0}`, true, 0},
		{"explicit output zero", `{"prompt_tokens":11,"completion_tokens":0,"total_tokens":11}`, true, 11},
		{"null output", `{"prompt_tokens":11,"completion_tokens":null,"total_tokens":11}`, true, 11},
		{"null usage", `null`, false, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := []byte(" {\"data\":[{\"embedding\":[0.25,-0.5]}],\"usage\":" + tc.usage + ",\"extension\":{\"keep\":true}} \n")
			wire, observed, err := (embeddingsDecoder{}).Decode(body)
			if err != nil || !bytes.Equal(body, wire) || (observed != nil) != tc.known {
				t.Fatal("native JSON changed or known state was invented", observed, err, string(wire))
			}
			if tc.known && (observed.PromptTokens != tc.input || observed.TotalTokens != tc.input || observed.CompletionTokens != 0) {
				t.Fatal("input-only receipt changed", observed)
			}
		})
	}
	for _, body := range []string{`{"data":[],"extension":1}`, `{"data":[],"usage":null}`} {
		wire, observed, err := (embeddingsDecoder{}).Decode([]byte(body))
		if err != nil || observed != nil || string(wire) != body {
			t.Fatal("missing usage did not retain unknown", observed, err)
		}
	}
}

func TestEmbeddingsUsageRejectsInvalidProof(t *testing.T) {
	for _, body := range []string{
		`null`, `[]`, `true`, `{"data":`, `{"error":{"message":"failed"}}`,
		`{"usage":[]}`, `{"usage":5}`, `{"usage":"bad"}`,
		`{"usage":{"prompt_tokens":-1,"total_tokens":-1}}`,
		`{"usage":{"prompt_tokens":11,"total_tokens":12}}`,
		`{"usage":{"prompt_tokens":11,"completion_tokens":1,"total_tokens":12}}`,
		`{"usage":{"prompt_tokens":"11","total_tokens":11}}`,
		`{"usage":{"prompt_tokens":11.0,"total_tokens":11}}`,
		`{"usage":{"prompt_tokens":9223372036854775808,"total_tokens":0}}`,
		`{"usage":{"prompt_tokens":11,"total_tokens":11,"total_tokens":0}}`,
		`{"usage":{},"usage":{"prompt_tokens":0,"total_tokens":0}}`,
		`{"usage":{"prompt_tokens":null,"total_tokens":-1}}`,
		`{"usage":{"completion_tokens":1}}`,
	} {
		t.Run(body, func(t *testing.T) {
			_, observed, err := (embeddingsDecoder{}).Decode([]byte(body))
			if err == nil || observed != nil {
				t.Fatal("invalid proof produced a successful or known receipt", observed, err)
			}
		})
	}
}
