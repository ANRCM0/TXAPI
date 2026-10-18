package responses

import (
	"bytes"
	"fmt"
	"math"
	"strings"
	"testing"

	bg "minigateway/internal/foundation/contracts/background/v1"
	execution "minigateway/internal/foundation/contracts/execution/v1"
)

func backgroundEvent(t string, seq int, status string) string {
	if status == "" {
		return fmt.Sprintf(`{"type":%q,"sequence_number":%d,"delta":"hello","response_id":"resp_native","extension":{"opaque":"kept"}}`, t, seq)
	}
	return fmt.Sprintf(`{"type":%q,"sequence_number":%d,"response":{"id":"resp_native","object":"response","status":%q,"output":[],"usage":{"input_tokens":2,"output_tokens":1,"total_tokens":3}}}`, t, seq, status)
}

func backgroundFrame(d execution.StreamDecoder, event string) ([]execution.Frame, error) {
	for _, line := range []string{"data: " + event, ""} {
		f, e := d.DecodeLine([]byte(line))
		if e != nil || len(f) > 0 {
			return f, e
		}
	}
	return nil, nil
}

func TestBackgroundNativeStreamPreservesEventsAndResumesAfterExplicitZero(t *testing.T) {
	prepared, p := (BackgroundCodec{}).Prepare(bg.RemoteCommand{Action: bg.RemoteSubmit, Body: []byte(`{"model":"m","background":true,"stream":true,"store":false,"input":"x","tools":[{"type":"function","name":"f"}]}`)}, backgroundTarget())
	if p != nil || !prepared.Input.Stream || !bytes.Contains(prepared.Input.Body, []byte(`"background":true`)) || !bytes.Contains(prepared.Input.Body, []byte(`"stream":true`)) {
		t.Fatal("native stream disabled", p)
	}
	d := prepared.Input.Decoder.NewStream()
	for i, ev := range []string{backgroundEvent("response.created", 0, "queued"), backgroundEvent("response.in_progress", 1, "in_progress"), backgroundEvent("response.output_text.delta", 2, ""), backgroundEvent("response.function_call_arguments.delta", 3, ""), backgroundEvent("response.completed", 4, "completed")} {
		frames, e := backgroundFrame(d, ev)
		if e != nil || len(frames) != 1 || frames[0].Complete != (i == 4) || bytes.Contains(frames[0].Data, []byte("resp_native")) || !bytes.Contains(frames[0].Data, []byte("resp_local")) {
			t.Fatalf("event %d: %+v %v", i, frames, e)
		}
		o := prepared.Progress.Drain()
		if o.RemoteID != "resp_native" || len(o.Events) != 1 || o.Events[0].Sequence != uint64(i) || !bytes.Equal(o.Events[0].Data, frames[0].Data) {
			t.Fatal("observer lost sequence", o)
		}
		if i == 4 && (o.State != bg.RemoteSucceeded || o.Usage == nil || o.Usage.TotalTokens != 3) {
			t.Fatal("terminal usage missing", o)
		}
		if again := prepared.Progress.Drain(); len(again.Events) != 0 {
			t.Fatal("observer repeated event")
		}
	}
	if _, e := d.End(); e != nil {
		t.Fatal(e)
	}
	for _, after := range []*uint64{nil, new(uint64)} {
		resume, p := (BackgroundCodec{}).Prepare(bg.RemoteCommand{Action: bg.RemoteResume, RemoteID: "resp_native", After: after}, backgroundTarget())
		if p != nil || resume.Input.Method != "GET" || !resume.Input.Stream || resume.Input.Replayable || strings.Contains(resume.Input.URL, "starting_after=0") != (after != nil) {
			t.Fatal("absent/zero cursor collapsed", p, resume.Input.URL)
		}
		if _, e := backgroundFrame(resume.Input.Decoder.NewStream(), backgroundEvent("response.output_text.delta", 1, "")); e != nil {
			t.Fatal("bound resume cannot accept delta-only", e)
		}
	}
	bad := uint64(math.MaxInt64) + 1
	if _, p := (BackgroundCodec{}).Prepare(bg.RemoteCommand{Action: bg.RemoteResume, RemoteID: "resp_native", After: &bad}, backgroundTarget()); p == nil {
		t.Fatal("cursor overflow accepted")
	}
}

func TestBackgroundNativeStreamRejectsMalformedAndDoesNotInventCompletion(t *testing.T) {
	for name, body := range map[string]string{
		"missing sequence":    `{"type":"response.created","response_id":"resp_native"}`,
		"null sequence":       `{"type":"response.created","sequence_number":null,"response_id":"resp_native"}`,
		"negative sequence":   `{"type":"response.created","sequence_number":-1,"response_id":"resp_native"}`,
		"overflow sequence":   `{"type":"response.created","sequence_number":9223372036854775808,"response_id":"resp_native"}`,
		"fractional sequence": `{"type":"response.created","sequence_number":1.0,"response_id":"resp_native"}`,
		"duplicate JSON":      `{"type":"response.created","sequence_number":0,"sequence_number":1,"response_id":"resp_native"}`,
		"missing identity":    `{"type":"response.output_text.delta","sequence_number":0,"delta":"x"}`,
		"foreign identity":    strings.Replace(backgroundEvent("response.created", 0, "queued"), `"status":"queued"`, `"status":"queued","response_id":"not-envelope"`, 1) + "invalid",
		"fake terminal":       backgroundEvent("response.completed", 0, "queued"),
		"terminal in created": backgroundEvent("response.created", 0, "completed"),
		"standalone error":    `{"type":"error","sequence_number":0,"message":"secret"}`,
		"oversize":            fmt.Sprintf(`{"type":"response.created","sequence_number":0,"response_id":"resp_native","x":%q}`, strings.Repeat("x", 64<<10)),
	} {
		t.Run(name, func(t *testing.T) {
			p, _ := (BackgroundCodec{}).Prepare(bg.RemoteCommand{Action: bg.RemoteSubmit, Body: []byte(`{"model":"m","background":true,"stream":true,"input":"x"}`)}, backgroundTarget())
			if _, e := backgroundFrame(p.Input.Decoder.NewStream(), body); e == nil {
				t.Fatal("invalid background event accepted")
			}
		})
	}
	p, _ := (BackgroundCodec{}).Prepare(bg.RemoteCommand{Action: bg.RemoteSubmit, Body: []byte(`{"model":"m","background":true,"stream":true,"input":"x"}`)}, backgroundTarget())
	d := p.Input.Decoder.NewStream()
	if _, e := d.DecodeLine([]byte("data: " + backgroundEvent("response.created", 0, "queued"))); e != nil {
		t.Fatal(e)
	}
	if f, e := d.End(); e != nil || len(f) != 0 || len(p.Progress.Drain().Events) != 0 {
		t.Fatal("partial EOF became durable event or task completion")
	}
	d = p.Input.Decoder.NewStream()
	if _, e := backgroundFrame(d, backgroundEvent("response.created", 0, "queued")); e != nil {
		t.Fatal(e)
	}
	p.Progress.Drain()
	if f, e := d.End(); e != nil || len(f) != 0 {
		t.Fatal("RPC EOF incorrectly requires native terminal")
	}
	if _, e := backgroundFrame(d, backgroundEvent("response.in_progress", 2, "in_progress")); e != nil {
		t.Fatal(e)
	}
	if _, e := backgroundFrame(d, backgroundEvent("response.in_progress", 1, "in_progress")); e == nil {
		t.Fatal("backward native order accepted")
	}
	resume, _ := (BackgroundCodec{}).Prepare(bg.RemoteCommand{Action: bg.RemoteResume, RemoteID: "resp_native"}, backgroundTarget())
	d = resume.Input.Decoder.NewStream()
	if _, e := backgroundFrame(d, strings.Replace(backgroundEvent("response.created", 0, "queued"), "resp_native", "resp_other", 1)); e == nil {
		t.Fatal("foreign resumed ID accepted")
	}
	if _, e := d.DecodeLine([]byte(`{"status":"completed"}`)); e == nil {
		t.Fatal("JSON response treated as idle SSE")
	}
}
