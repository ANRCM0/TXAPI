package core

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type testOutput struct {
	metadata ResponseMetadata
	state    OutputState
	data     bytes.Buffer
	commits  int
	flushes  int
	commit   func(context.Context) error
	write    func(context.Context, []byte) (int, error)
	flush    func(context.Context) error
}

func (o *testOutput) Commit(ctx context.Context, metadata ResponseMetadata) error {
	o.commits++
	o.metadata = metadata
	if o.commit != nil {
		return o.commit(ctx)
	}
	o.state.Committed = true
	return nil
}
func (o *testOutput) Write(ctx context.Context, data []byte) (int, error) {
	if o.write != nil {
		return o.write(ctx, data)
	}
	if o.data.Len()+len(data) > 1024 {
		return 0, io.ErrShortBuffer
	}
	return o.data.Write(data)
}
func (o *testOutput) Flush(ctx context.Context) error {
	o.flushes++
	if o.flush != nil {
		return o.flush(ctx)
	}
	return nil
}
func (o *testOutput) State() OutputState { return o.state }

func TestExecutorIndependentOutput(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(map[bool]string{false: "json", true: "stream"}[stream], func(t *testing.T) {
			body := `{"ok":true}`
			contentType := "application/json"
			if stream {
				body, contentType = "data: first\n\ndata: [DONE]\n\n", "text/event-stream"
			}
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, body) }))
			defer upstream.Close()
			e := New(Options{})
			defer e.Close()
			output := &testOutput{}
			result := e.ExecuteTo(context.Background(), output, Input{URL: upstream.URL, Decoder: rawDecoder{}, Stream: stream})
			if result.Error != nil || !result.Committed || result.HTTPStatus != 0 || !result.ProtocolComplete || result.Phase != ResponseReceived {
				t.Fatalf("output result: %+v", result)
			}
			if output.commits != 1 || output.metadata.StatusCode != 200 || output.metadata.ContentType != contentType || output.data.Len() == 0 {
				t.Fatalf("output: %+v", output)
			}
			if stream && (output.flushes == 0 || !strings.Contains(output.data.String(), "[DONE]")) {
				t.Fatal("stream was not flushed or completed")
			}
		})
	}
}

func TestExecutorOutputFailuresPreserveFacts(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, "data: first\n\ndata: [DONE]\n\n") }))
	defer upstream.Close()
	e := New(Options{})
	defer e.Close()
	for _, name := range []string{"commit_before", "commit_after", "uncommitted", "short_write", "invalid_count", "flush"} {
		t.Run(name, func(t *testing.T) {
			output := &testOutput{}
			switch name {
			case "commit_before", "commit_after":
				output.commit = func(context.Context) error {
					output.state.Committed = name == "commit_after"
					return io.ErrClosedPipe
				}
			case "uncommitted":
				output.commit = func(context.Context) error { return nil }
			case "short_write":
				output.write = func(context.Context, []byte) (int, error) { return 1, nil }
			case "invalid_count":
				output.write = func(context.Context, []byte) (int, error) { return -1, nil }
			case "flush":
				output.flush = func(context.Context) error { return io.ErrClosedPipe }
			}
			result := e.ExecuteTo(context.Background(), output, Input{URL: upstream.URL, Stream: true, Decoder: rawDecoder{}})
			wantCommitted := name != "commit_before" && name != "uncommitted"
			if result.Error == nil || result.Termination != ClientWriteError || result.Committed != wantCommitted || result.HTTPStatus != 0 || result.ProtocolComplete {
				t.Fatalf("output failure: %+v", result)
			}
			if strings.Contains(output.data.String(), "[DONE]") {
				t.Fatal("output failure fabricated a terminal event")
			}
		})
	}
}

func TestExecutorOutputCancellationAndPreconditions(t *testing.T) {
	var hits atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { hits.Add(1); io.WriteString(w, `{"ok":true}`) }))
	defer upstream.Close()
	e := New(Options{})
	defer e.Close()
	in := Input{URL: upstream.URL, Decoder: rawDecoder{}}
	if result := e.ExecuteTo(context.Background(), nil, in); result.StatusCode != 500 || result.Phase != NotDispatched {
		t.Fatalf("nil output: %+v", result)
	}
	used := &testOutput{state: OutputState{Committed: true}}
	if result := e.ExecuteTo(context.Background(), used, in); result.StatusCode != 500 || result.Phase != NotDispatched || !result.Committed {
		t.Fatalf("reused output: %+v", result)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if result := e.ExecuteTo(ctx, &testOutput{}, in); result.Termination != Canceled || result.Phase != NotDispatched || hits.Load() != 0 {
		t.Fatalf("canceled before execution: %+v hits=%d", result, hits.Load())
	}
	blocked, stop := context.WithCancel(context.Background())
	defer stop()
	entered := make(chan struct{})
	go func() {
		select {
		case <-entered:
			stop()
		case <-blocked.Done():
		}
	}()
	output := &testOutput{write: func(ctx context.Context, _ []byte) (int, error) {
		close(entered)
		<-ctx.Done()
		return 0, ctx.Err()
	}}
	in.Timeout = 5 * time.Second
	result := e.ExecuteTo(blocked, output, in)
	if result.Termination != Canceled || !result.Committed || result.StatusCode != 499 || hits.Load() != 1 {
		t.Fatalf("blocked output cancellation: %+v hits=%d", result, hits.Load())
	}
}

func TestHTTPOutputMetadata(t *testing.T) {
	recorder := httptest.NewRecorder()
	output := NewHTTPOutput(recorder)
	if err := output.Commit(context.Background(), ResponseMetadata{StatusCode: 200, ContentType: "text/event-stream", Values: map[string]string{"X-TXAPI-Billing-ID": "bill-one"}}); err != nil {
		t.Fatal(err)
	}
	output.Commit(context.Background(), ResponseMetadata{StatusCode: 503, ContentType: "application/json"})
	if recorder.Code != 200 || output.State().HTTPStatus != 200 || recorder.Header().Get("X-TXAPI-Billing-ID") != "bill-one" || recorder.Header().Get("X-Accel-Buffering") != "no" {
		t.Fatalf("metadata or monotonic state lost: %v %+v", recorder.Header(), output.State())
	}
	if err := output.Flush(context.Background()); err != nil || !recorder.Flushed {
		t.Fatal("HTTP flush was not forwarded")
	}
	if !errors.Is(output.SetWriteDeadline(time.Time{}), http.ErrNotSupported) {
		t.Fatal("unsupported deadline was not preserved")
	}
}
