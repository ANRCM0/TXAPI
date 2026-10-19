package responses

import (
	"testing"

	v1 "minigateway/internal/foundation/contracts/execution/v1"
	v2 "minigateway/internal/foundation/contracts/execution/v2"
	v3 "minigateway/internal/foundation/contracts/execution/v3"
	v4 "minigateway/internal/foundation/contracts/execution/v4"
)

func TestResponsesProgressAllStreamPorts(t *testing.T) {
	for _, port := range []string{"frames", "facts", "usage"} {
		for _, output := range []string{
			`{"type":"response.output_text.delta","delta":"hello"}`,
			`{"type":"response.function_call_arguments.delta","delta":"{"}`,
			`{"type":"response.output_item.added","item":{"type":"function_call","name":"lookup","arguments":""}}`,
		} {
			t.Run(port+output, func(t *testing.T) {
				d := NewDecoder()
				var stream any
				switch port {
				case "frames":
					stream = d.NewStream()
				case "facts":
					stream = d.NewEnvelopeStream()
				case "usage":
					stream = d.NewUsageEnvelopeStream()
				}
				progress := stream.(v4.StreamProgress)
				decode := func(line string) error {
					switch s := stream.(type) {
					case v1.StreamDecoder:
						_, err := s.DecodeLine([]byte(line))
						return err
					case v2.StreamDecoder:
						_, err := s.DecodeEnvelopes([]byte(line))
						return err
					case v3.StreamDecoder:
						_, err := s.DecodeUsageEnvelopes([]byte(line))
						return err
					}
					panic("missing port")
				}
				for i, line := range []string{": heartbeat", `data: {"type":"response.in_progress"}`, `data: {"type":"response.output_text.delta","delta":""}`, `data: {"type":"response.output_item.added","item":{"type":"function_call","id":"id","name":"","arguments":""}}`} {
					if err := decode(line); err != nil {
						t.Fatal(err)
					}
					if got := progress.Progress(); got.CompleteEvents != uint64(i) || got.EffectiveOutput {
						t.Fatalf("partial/neutral progress: %+v", got)
					}
					if err := decode(""); err != nil {
						t.Fatal(err)
					}
					if got := progress.Progress(); got.CompleteEvents != uint64(i+1) || got.EffectiveOutput {
						t.Fatalf("neutral progress: %+v", got)
					}
				}
				if err := decode("data: " + output); err != nil {
					t.Fatal(err)
				}
				if progress.Progress().EffectiveOutput {
					t.Fatal("partial output event counted")
				}
				if err := decode(""); err != nil {
					t.Fatal(err)
				}
				if got := progress.Progress(); !got.EffectiveOutput || got.CompleteEvents != 5 {
					t.Fatalf("actual progress missing: %+v", got)
				}
				if err := decode(": heartbeat"); err != nil {
					t.Fatal(err)
				}
				if err := decode(""); err != nil {
					t.Fatal(err)
				}
				if !progress.Progress().EffectiveOutput {
					t.Fatal("effective fact not sticky")
				}
				if fresh := d.NewStream().(v4.StreamProgress).Progress(); fresh != (v4.Progress{}) {
					t.Fatal("progress leaked across streams")
				}
			})
		}
	}
}

func TestResponsesProgressEmptyTerminalAndInvalidEvent(t *testing.T) {
	s := NewDecoder().NewStream()
	p := s.(v4.StreamProgress)
	terminal := `data: {"type":"response.completed","response":{"id":"r","object":"response","status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":0,"total_tokens":1}}}`
	if _, err := s.DecodeLine([]byte(terminal)); err != nil {
		t.Fatal(err)
	}
	if got := p.Progress(); got != (v4.Progress{}) {
		t.Fatal("partial terminal counted")
	}
	frames, err := s.DecodeLine(nil)
	if err != nil || len(frames) != 1 || !frames[0].Complete || p.Progress().EffectiveOutput || p.Progress().CompleteEvents != 1 {
		t.Fatalf("empty legal terminal: %+v %v %+v", frames, err, p.Progress())
	}
	for _, event := range []string{`data: {`, `data: {"type":"response.output_text.delta","delta":3}`, `event: wrong`} {
		s = NewDecoder().NewStream()
		if _, err := s.DecodeLine([]byte(event)); err != nil {
			t.Fatal(err)
		}
		_, err := s.DecodeLine(nil)
		if event != "event: wrong" && err == nil {
			t.Fatal("invalid event accepted")
		}
		if event != "event: wrong" && s.(v4.StreamProgress).Progress() != (v4.Progress{}) {
			t.Fatal("invalid event counted")
		}
	}
}
