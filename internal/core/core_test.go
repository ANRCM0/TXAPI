package core

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type rawDecoder struct{}

func (rawDecoder) Decode(b []byte) ([]byte, *Usage, error) { return b, nil, nil }
func (rawDecoder) NewStream() StreamDecoder                { return &rawStream{} }

type rawStream struct{ done bool }

func (s *rawStream) DecodeLine(b []byte) ([]Frame, error) {
	if string(b) == "data: [DONE]" {
		s.done = true
		return []Frame{{Complete: true}}, nil
	}
	return []Frame{{Data: append(append([]byte(nil), b...), '\n')}}, nil
}
func (s *rawStream) End() ([]Frame, error) {
	if !s.done {
		return nil, io.ErrUnexpectedEOF
	}
	return []Frame{{Data: []byte("data: [DONE]\n\n")}}, nil
}

func TestExecutorReusesConnections(t *testing.T) {
	var connections atomic.Int64
	upstream := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		io.WriteString(w, `{"ok":true}`)
	}))
	upstream.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			connections.Add(1)
		}
	}
	upstream.Start()
	defer upstream.Close()
	e := New(Options{})
	defer e.Close()
	for i := 0; i < 5; i++ {
		w := httptest.NewRecorder()
		result := e.Execute(context.Background(), w, Input{URL: upstream.URL, Body: []byte(`{}`), Decoder: rawDecoder{}, Replayable: true})
		if result.Error != nil || result.StatusCode != 200 || !result.Committed || w.Body.String() != `{"ok":true}` {
			t.Fatalf("result=%+v body=%s", result, w.Body.String())
		}
	}
	if connections.Load() != 1 {
		t.Fatalf("connections=%d want reused connection", connections.Load())
	}
}

func TestExecutorDeadlineAndPreCanceledRequest(t *testing.T) {
	var hits atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1); <-r.Context().Done() }))
	defer upstream.Close()
	e := New(Options{})
	defer e.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result := e.Execute(ctx, httptest.NewRecorder(), Input{URL: upstream.URL, Decoder: rawDecoder{}})
	if result.Termination != Canceled || result.Committed || hits.Load() != 0 {
		t.Fatalf("pre-canceled: %+v hits=%d", result, hits.Load())
	}
	result = e.Execute(context.Background(), httptest.NewRecorder(), Input{URL: upstream.URL, Decoder: rawDecoder{}, Timeout: 30 * time.Millisecond})
	if result.Termination != Deadline || result.StatusCode != 504 || result.Committed {
		t.Fatalf("deadline: %+v", result)
	}
}

func TestExecutorReportsCommittedStreamFailure(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "data: partial\n\n") }))
	defer upstream.Close()
	e := New(Options{})
	defer e.Close()
	w := httptest.NewRecorder()
	result := e.Execute(context.Background(), w, Input{URL: upstream.URL, Decoder: rawDecoder{}, Stream: true, Replayable: true})
	if result.Error == nil || !result.Committed || result.HTTPStatus != 200 || result.StatusCode != 502 {
		t.Fatalf("result=%+v", result)
	}
	if strings.Contains(w.Body.String(), "[DONE]") {
		t.Fatal("failed stream marked done")
	}
}

func TestExecutorBoundsNormalResponse(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, strings.Repeat("x", 128)) }))
	defer upstream.Close()
	e := New(Options{MaxResponseBytes: 64})
	defer e.Close()
	result := e.Execute(context.Background(), httptest.NewRecorder(), Input{URL: upstream.URL, Decoder: rawDecoder{}})
	if result.Error == nil || result.Committed || result.StatusCode != 502 {
		t.Fatalf("result=%+v", result)
	}
}

type failedWriter struct{ header http.Header }

func (w *failedWriter) Header() http.Header         { return w.header }
func (w *failedWriter) WriteHeader(int)             {}
func (w *failedWriter) Write(b []byte) (int, error) { return 1, io.ErrClosedPipe }
func TestExecutorDownstreamFailureKeepsCommittedState(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, `{"ok":true}`) }))
	defer upstream.Close()
	e := New(Options{})
	defer e.Close()
	w := &failedWriter{header: make(http.Header)}
	result := e.Execute(context.Background(), w, Input{URL: upstream.URL, Decoder: rawDecoder{}, Replayable: true})
	if !result.Committed || result.HTTPStatus != 200 || result.Termination != ClientWriteError || result.BytesWritten != 1 || result.Error == nil {
		t.Fatalf("downstream failure result=%+v", result)
	}
}
