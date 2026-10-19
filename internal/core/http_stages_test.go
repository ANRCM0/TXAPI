package core

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

type stageDecoder struct{}

func (stageDecoder) Decode(b []byte) ([]byte, *Usage, error) { return b, nil, nil }
func (stageDecoder) NewStream() StreamDecoder                { return stageStream{} }

type stageStream struct{}

type lateStageDecoder struct{ stageDecoder }

func (lateStageDecoder) NewStream() StreamDecoder { return lateStageStream{} }

type lateStageStream struct{ stageStream }

func (lateStageStream) DecodeLine(b []byte) ([]Frame, error) {
	time.Sleep(100 * time.Millisecond)
	return []Frame{{Data: []byte("late"), Complete: true, EventComplete: true}}, nil
}

func (stageStream) DecodeLine(b []byte) ([]Frame, error) {
	switch string(b) {
	case "heartbeat":
		return []Frame{{EventComplete: true}}, nil
	case "text":
		return []Frame{{Data: []byte("text"), EffectiveOutput: true, EventComplete: true}}, nil
	case "terminal":
		return []Frame{{Data: []byte("done"), Complete: true, EventComplete: true}}, nil
	}
	return nil, nil
}
func (stageStream) End() ([]Frame, error) { return nil, nil }

func TestHTTPStageSocketWaitBudgets(t *testing.T) {
	for _, mode := range []string{"headers", "heartbeat-first", "partial-idle", "text-idle", "empty-terminal"} {
		t.Run(mode, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if mode == "headers" {
					<-r.Context().Done()
					return
				}
				w.WriteHeader(200)
				w.(http.Flusher).Flush()
				switch mode {
				case "empty-terminal":
					io.WriteString(w, "terminal\n")
					w.(http.Flusher).Flush()
				case "text-idle":
					io.WriteString(w, "text\n")
					w.(http.Flusher).Flush()
				case "partial-idle":
					io.WriteString(w, "half-event")
					w.(http.Flusher).Flush()
				case "heartbeat-first":
					ticker := time.NewTicker(10 * time.Millisecond)
					defer ticker.Stop()
					for {
						select {
						case <-r.Context().Done():
							return
						case <-ticker.C:
							io.WriteString(w, "heartbeat\n")
							w.(http.Flusher).Flush()
						}
					}
				}
				<-r.Context().Done()
			}))
			defer upstream.Close()
			executor := New(Options{})
			defer executor.Close()
			budgets := HTTPStageTimeouts{Headers: 100 * time.Millisecond}
			want := TimeoutHeaders
			if mode != "headers" {
				budgets.FirstEffectiveOutput = 100 * time.Millisecond
				budgets.StreamIdle = 300 * time.Millisecond
				want = TimeoutFirstEffectiveOutput
			}
			if mode == "partial-idle" || mode == "text-idle" {
				budgets.FirstEffectiveOutput = time.Second
				budgets.StreamIdle = 100 * time.Millisecond
				want = TimeoutStreamIdle
			}
			result := executor.Execute(context.Background(), httptest.NewRecorder(), Input{URL: upstream.URL, Stream: true, Decoder: stageDecoder{}, Timeout: 2 * time.Second, HTTPStageTimeouts: budgets})
			if mode == "empty-terminal" {
				if result.Termination != Success || !result.ProtocolComplete || result.TimeoutStage != "" {
					t.Fatalf("empty terminal: %+v", result)
				}
			} else if result.Termination != Deadline || result.TimeoutStage != want || result.ProtocolComplete {
				t.Fatalf("stage: %+v want %s", result, want)
			}
		})
	}
}

type stageOutput struct {
	state    OutputState
	pause    time.Duration
	deadline time.Time
	fail     bool
}

func (o *stageOutput) Commit(ctx context.Context, _ ResponseMetadata) error {
	o.state = OutputState{Committed: true, HTTPStatus: 200}
	return nil
}
func (o *stageOutput) Write(ctx context.Context, b []byte) (int, error) {
	if o.fail {
		<-ctx.Done()
		return 0, ctx.Err()
	}
	select {
	case <-time.After(o.pause):
		return len(b), nil
	case <-ctx.Done():
		return 0, ctx.Err()
	}
}
func (o *stageOutput) Flush(context.Context) error        { return nil }
func (o *stageOutput) State() OutputState                 { return o.state }
func (o *stageOutput) SetWriteDeadline(d time.Time) error { o.deadline = d; return nil }

func TestHTTPStagePausesUpstreamAndPreservesReceivedTerminalOnWriteFailure(t *testing.T) {
	for _, failed := range []bool{false, true} {
		t.Run(map[bool]string{false: "pause", true: "write"}[failed], func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if failed {
					io.WriteString(w, "terminal\n")
				} else {
					io.WriteString(w, "text\nterminal\n")
				}
				w.(http.Flusher).Flush()
				<-r.Context().Done()
			}))
			defer s.Close()
			e := New(Options{})
			defer e.Close()
			out := &stageOutput{pause: 150 * time.Millisecond, fail: failed}
			result := e.ExecuteTo(context.Background(), out, Input{URL: s.URL, Stream: true, Decoder: stageDecoder{}, Timeout: 2 * time.Second,
				HTTPStageTimeouts: HTTPStageTimeouts{FirstEffectiveOutput: 100 * time.Millisecond, StreamIdle: 100 * time.Millisecond, DownstreamWrite: 500 * time.Millisecond}})
			if failed {
				if result.Termination != ClientWriteError || result.TimeoutStage != TimeoutDownstreamWrite || !result.ProtocolComplete {
					t.Fatalf("write terminal: %+v", result)
				}
			} else if result.Termination != Success || result.TimeoutStage != "" {
				t.Fatalf("pause: %+v", result)
			}
		})
	}
}

func TestHTTPStageUnsupportedWriteRejectsBeforeDispatch(t *testing.T) {
	e := New(Options{})
	defer e.Close()
	result := e.Execute(context.Background(), httptest.NewRecorder(), Input{URL: "http://127.0.0.1:1", Decoder: stageDecoder{}, HTTPStageTimeouts: HTTPStageTimeouts{DownstreamWrite: time.Second}})
	if result.Phase != NotDispatched || result.Error == nil || !strings.Contains(result.Error.Error.Message, "write deadlines") {
		t.Fatalf("unsupported: %+v", result)
	}
}

func TestHTTPStageRejectsTerminalAfterTimeoutWon(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "terminal\n")
	}))
	defer upstream.Close()
	e := New(Options{})
	defer e.Close()
	result := e.Execute(context.Background(), httptest.NewRecorder(), Input{URL: upstream.URL, Stream: true, Decoder: lateStageDecoder{}, Timeout: time.Second,
		HTTPStageTimeouts: HTTPStageTimeouts{FirstEffectiveOutput: 20 * time.Millisecond}})
	if result.TimeoutStage != TimeoutFirstEffectiveOutput || result.Termination != Deadline || result.ProtocolComplete || result.Committed || result.BytesWritten != 0 {
		t.Fatalf("late terminal accepted after timeout: %+v", result)
	}
}

func TestHTTPStageWatchdogArbitrationAndJoin(t *testing.T) {
	// The protocol terminal wins before a late timer observation; downstream
	// delivery can still fail under its independently armed budget.
	_, w := newHTTPStageWatchdog(context.Background(), HTTPStageTimeouts{FirstEffectiveOutput: time.Hour, StreamIdle: time.Hour, DownstreamWrite: time.Hour})
	w.headers(false, true)
	w.progress(Frame{Complete: true})
	w.mu.Lock()
	w.first, w.idle = time.Now().Add(-time.Hour), time.Now().Add(-time.Hour)
	w.expireLocked(time.Now())
	if w.fired != "" {
		t.Fatal("late upstream timer changed terminal")
	}
	w.mu.Unlock()
	w.beginOutput()
	w.mu.Lock()
	w.write = time.Now().Add(-time.Second)
	w.expireLocked(time.Now())
	w.mu.Unlock()
	if stage := w.timeoutStage(); stage != TimeoutDownstreamWrite {
		t.Fatalf("terminal delivery timeout = %q", stage)
	}
	w.close()
	select {
	case <-w.done:
	default:
		t.Fatal("close returned without joining watchdog")
	}
}

func TestHTTPStageWatchdogCancelAndTotalDeadlineTakePrecedence(t *testing.T) {
	for _, canceled := range []bool{false, true} {
		ctx, cancel := context.WithCancel(context.Background())
		_, w := newHTTPStageWatchdog(ctx, HTTPStageTimeouts{Headers: time.Hour})
		w.headers(true, false)
		if canceled {
			cancel()
		}
		w.mu.Lock()
		w.header = time.Now().Add(-time.Second)
		if !canceled {
			w.total = w.header
		}
		w.expireLocked(time.Now())
		w.mu.Unlock()
		if stage := w.timeoutStage(); stage != "" {
			t.Errorf("outer cancellation/deadline acquired stage %q", stage)
		}
		w.close()
		cancel()
	}
}

func TestHTTPStageLateCanceledWriteCannotAcquireTimeoutAttribution(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	_, w := newHTTPStageWatchdog(ctx, HTTPStageTimeouts{DownstreamWrite: time.Second})
	w.beginOutput()
	cancel()
	<-w.done // cancellation was observed while the write budget was still live
	w.mu.Lock()
	w.write = time.Now().Add(-time.Second)
	w.mu.Unlock()
	w.outputFailed(context.Canceled)
	w.outputFailed(os.ErrDeadlineExceeded)
	w.endOutput()
	if stage := w.timeoutStage(); stage != "" {
		t.Fatalf("late non-timeout write acquired %q", stage)
	}
	w.close()
}

func TestHTTPStageWatchdogConcurrentTerminalCancelAndTimer(t *testing.T) {
	for i := 0; i < 100; i++ {
		parent, cancel := context.WithCancel(context.Background())
		_, w := newHTTPStageWatchdog(parent, HTTPStageTimeouts{FirstEffectiveOutput: time.Nanosecond, StreamIdle: time.Nanosecond})
		var group sync.WaitGroup
		group.Add(3)
		go func() { defer group.Done(); w.headers(false, true) }()
		go func() { defer group.Done(); w.progress(Frame{Complete: true}) }()
		go func() { defer group.Done(); cancel() }()
		group.Wait()
		w.close()
		if stage := w.timeoutStage(); stage != "" && stage != TimeoutFirstEffectiveOutput && stage != TimeoutStreamIdle {
			t.Fatalf("invalid winning stage: %q", stage)
		}
	}
}

func TestHTTPStageRealSocketWriteBudget(t *testing.T) {
	// More than the socket and HTTP/2 flow-control windows; the client observes
	// headers but deliberately never consumes the body.
	payload := strings.Repeat("x", 16<<20)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, payload)
	}))
	defer upstream.Close()
	for _, mode := range []string{"http", "tls", "http2"} {
		t.Run(mode, func(t *testing.T) {
			executor := New(Options{MaxResponseBytes: 128 << 20})
			defer executor.Close()
			results := make(chan Result, 1)
			downstream := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				results <- executor.Execute(r.Context(), w, Input{URL: upstream.URL, Decoder: stageDecoder{}, Timeout: 3 * time.Second,
					HTTPStageTimeouts: HTTPStageTimeouts{DownstreamWrite: 150 * time.Millisecond}})
			}))
			downstream.Config.ConnContext = func(ctx context.Context, conn net.Conn) context.Context {
				if tlsConn, ok := conn.(interface{ NetConn() net.Conn }); ok {
					conn = tlsConn.NetConn()
				}
				if tcp, ok := conn.(*net.TCPConn); ok {
					if err := tcp.SetWriteBuffer(1024); err != nil {
						t.Error(err)
					}
				}
				return ctx
			}
			if mode == "http" {
				downstream.Start()
			} else {
				downstream.EnableHTTP2 = mode == "http2"
				downstream.StartTLS()
			}
			defer downstream.Close()
			client := downstream.Client()
			transport := client.Transport.(*http.Transport).Clone()
			transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
				conn, err := (&net.Dialer{}).DialContext(ctx, network, address)
				if err == nil {
					if tcp, ok := conn.(*net.TCPConn); ok {
						if err = tcp.SetReadBuffer(1024); err != nil {
							conn.Close()
							return nil, err
						}
					}
				}
				return conn, err
			}
			client.Transport = transport
			defer transport.CloseIdleConnections()
			response, err := client.Get(downstream.URL)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			if mode == "http2" && response.ProtoMajor != 2 {
				t.Fatalf("expected HTTP/2, got %s", response.Proto)
			}
			select {
			case result := <-results:
				if mode == "http" && result.Termination == Success && result.BytesWritten == int64(len(payload)) {
					t.Skip("kernel accepted the complete HTTP/1 body; this run does not prove a blocked TCP writer")
				}
				if result.TimeoutStage != TimeoutDownstreamWrite || result.Termination != ClientWriteError || !result.Committed || !result.ProtocolComplete {
					t.Fatalf("blocked %s write: %+v", mode, result)
				}
			// Go's HTTP/1 TLS error path calls tls.Conn.Close, whose
			// close_notify teardown has an additional five-second bound.
			case <-time.After(8 * time.Second):
				t.Fatal("blocked writer did not exit")
			}
		})
	}
}
