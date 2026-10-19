package responses

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"minigateway/internal/core"
	v1 "minigateway/internal/foundation/contracts/execution/v1"
	"minigateway/internal/gateway"
)

func TestResponsesSSEEOFRequiresDispatchedTerminal(t *testing.T) {
	terminal := `data: {"type":"response.completed","response":{"id":"native","object":"response","status":"completed"}}`
	for _, suffix := range []string{"", "\n", "\r\n"} {
		t.Run("missing blank line "+suffix, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(w, terminal+suffix)
			}))
			defer server.Close()
			e := core.New(core.Options{})
			defer e.Close()
			decoder := NewDecoder()
			rec := httptest.NewRecorder()
			result := e.Execute(context.Background(), rec, gateway.ToCoreInput(v1.Input{URL: server.URL, Stream: true, Decoder: decoder}))
			if result.Error == nil || result.ProtocolComplete || decoder.Observation().Status != "" || rec.Body.Len() != 0 {
				t.Fatalf("truncated terminal promoted to completion: result=%+v observation=%+v body=%s", result, decoder.Observation(), rec.Body)
			}
		})
	}
}

func TestResponsesSSEFragmentedCRLFMultilineAndComments(t *testing.T) {
	wire := ": heartbeat\r\n\r\n" +
		"event: response.output_text.delta\r\n" +
		"id: retained\r\n: comment between fields\r\n" +
		"data: {\"type\":\"response.output_text.delta\",\r\n" +
		"data: \"delta\":\"你好\",\"response_id\":\"native\"}\r\n\r\n" +
		"event: response.completed\r\n" +
		"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"native\",\"object\":\"response\",\"status\":\"completed\"}}\r\n\r\n"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		for i := 0; i < len(wire); i++ {
			_, _ = w.Write([]byte{wire[i]})
			w.(http.Flusher).Flush()
		}
	}))
	defer server.Close()
	e := core.New(core.Options{})
	defer e.Close()
	rec := httptest.NewRecorder()
	result := e.Execute(context.Background(), rec, gateway.ToCoreInput(v1.Input{URL: server.URL, Stream: true, Decoder: NewDecoder()}))
	if result.Error != nil || !result.ProtocolComplete || rec.Body.String() != strings.ReplaceAll(wire, "\r\n", "\n") {
		t.Fatalf("fragmented SSE changed: result=%+v body=%s", result, rec.Body)
	}
	// A line consumer retaining CR must also dispatch on a CRLF blank line.
	s := NewDecoder().NewStream()
	for _, line := range strings.Split(strings.TrimSuffix(wire, "\n"), "\n") {
		if _, err := s.DecodeLine([]byte(line)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.End(); err != nil {
		t.Fatal(err)
	}
}

func TestResponsesSSEWholeEventLimitIncludesBlankLine(t *testing.T) {
	// Two individually bounded comment lines exercise the aggregate limit,
	// including the delimiter that dispatches the complete event.
	for _, extra := range []int{0, 1} {
		s := NewDecoder().NewStream()
		first := ":" + strings.Repeat("a", maxEventBytes/2-1)
		second := ":" + strings.Repeat("b", maxEventBytes/2-4+extra)
		if _, err := s.DecodeLine([]byte(first)); err != nil {
			t.Fatal(err)
		}
		_, err := s.DecodeLine([]byte(second))
		if extra == 1 {
			if err == nil {
				t.Fatal("event exceeding aggregate limit accepted")
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		frames, err := s.DecodeLine(nil)
		if err != nil || len(frames) != 1 || len(frames[0].Data) != maxEventBytes || frames[0].Complete {
			t.Fatalf("bounded comment frame: frames=%d err=%v", len(frames), err)
		}
		if _, err := s.End(); err == nil {
			t.Fatal("comments incorrectly completed the response")
		}
	}
}

func TestResponsesSSETerminalEnvelopeFacts(t *testing.T) {
	for _, status := range []string{"completed", "incomplete", "failed", "cancelled", "error"} {
		t.Run(status, func(t *testing.T) {
			d := NewDecoderWithID("public")
			s := d.NewEnvelopeStream()
			created := `data: {"type":"response.created","response":{"id":"native","object":"response","status":"in_progress","usage":{"input_tokens":3}}}`
			if _, err := s.DecodeEnvelopes([]byte(created)); err != nil {
				t.Fatal(err)
			}
			if _, err := s.DecodeEnvelopes(nil); err != nil {
				t.Fatal(err)
			}
			terminal := `data: {"type":"response.` + status + `","response":{"id":"native","object":"response","status":"` + status + `","error":{"message":"secret"}}}`
			if status == "error" {
				terminal = `data: {"type":"error","response_id":"native","message":"secret"}`
			}
			if frames, err := s.DecodeEnvelopes([]byte(terminal)); err != nil || len(frames) != 0 {
				t.Fatalf("event emitted before delimiter: %v %v", frames, err)
			}
			frames, err := s.DecodeEnvelopes(nil)
			if err != nil || len(frames) != 1 {
				t.Fatalf("terminal frames: %v %v", frames, err)
			}
			frame := frames[0]
			wantStatus := status
			if status == "error" {
				wantStatus = "failed"
			}
			wantFailed := wantStatus == "failed" || wantStatus == "cancelled"
			if !frame.ProtocolTerminal || frame.Failed != wantFailed || frame.Native == nil || frame.Native.ID != "native" || frame.Native.Status != wantStatus || frame.Usage != nil && frame.Usage.Counts.InputTokens != nil {
				t.Fatalf("incorrect terminal facts: %+v", frame)
			}
			if strings.Contains(string(frame.Data), "native") || strings.Contains(string(frame.Data), "secret") || !strings.Contains(string(frame.Data), "public") {
				t.Fatalf("identity or error leaked: %s", frame.Data)
			}
			if frames, err := s.EndEnvelopes(); err != nil || len(frames) != 0 {
				t.Fatalf("terminal repeated at EOF: %v %v", frames, err)
			}
		})
	}
}
