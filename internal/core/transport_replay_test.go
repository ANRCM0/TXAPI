package core

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// A server accepts the complete POST on an already-used connection, then loses
// its response. A provider idempotency marker must not silently move a body
// replay below the gateway's separately authorized/costed attempt boundary.
func TestTransportReplayAcceptedPOSTIsNotHiddenInsideOneExecution(t *testing.T) {
	for _, tls := range []bool{false, true} {
		for _, replayable := range []bool{false, true} {
			for _, marker := range []string{"", "Idempotency-Key", "X-Idempotency-Key"} {
				name := "http"
				if tls {
					name = "tls"
				}
				if replayable {
					name += "/replayable"
				} else {
					name += "/nonreplayable"
				}
				if marker != "" {
					name += "/" + marker
				} else {
					name += "/no-marker"
				}
				t.Run(name, func(t *testing.T) {
					var accepted atomic.Int64
					upstream := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						body, err := io.ReadAll(r.Body)
						if err != nil {
							t.Error(err)
							return
						}
						if r.URL.Path == "/trial" {
							if string(body) != `{"request":"trial"}` {
								t.Errorf("body changed: %q", body)
							}
							if accepted.Add(1) == 1 {
								conn, _, err := w.(http.Hijacker).Hijack()
								if err != nil {
									t.Error(err)
									return
								}
								conn.Close()
								return
							}
						}
						io.WriteString(w, `{"ok":true}`)
					}))
					if tls {
						upstream.StartTLS()
					} else {
						upstream.Start()
					}
					defer upstream.Close()
					client := upstream.Client()
					client.Timeout = 3 * time.Second
					e := New(Options{Client: client})
					defer e.Close()
					warm := e.Execute(context.Background(), httptest.NewRecorder(), Input{URL: upstream.URL + "/warm", Body: []byte(`{"warm":true}`), Decoder: rawDecoder{}})
					if warm.Error != nil || !warm.ProtocolComplete {
						t.Fatalf("warm connection: %+v", warm)
					}
					var connections, reused, writes atomic.Int64
					ctx := httptrace.WithClientTrace(context.Background(), &httptrace.ClientTrace{
						GotConn: func(info httptrace.GotConnInfo) {
							connections.Add(1)
							if info.Reused {
								reused.Add(1)
							}
						},
						WroteRequest: func(httptrace.WroteRequestInfo) { writes.Add(1) },
					})
					headers := make(http.Header)
					if marker != "" {
						headers.Set(marker, "fixture-only-idempotency")
					}
					output := httptest.NewRecorder()
					result := e.Execute(ctx, output, Input{URL: upstream.URL + "/trial", Body: []byte(`{"request":"trial"}`), Header: headers, Replayable: replayable, Decoder: rawDecoder{}, Timeout: 2 * time.Second})
					if accepted.Load() != 1 || connections.Load() != 1 || reused.Load() != 1 || writes.Load() != 1 || result.Error == nil || result.Committed || result.ProtocolComplete || result.Phase != Dispatching || result.Replayable != replayable || result.StatusCode != 502 || output.Body.Len() != 0 {
						t.Fatalf("hidden transport replay/changed failure: accepted=%d connections=%d reused=%d writes=%d result=%+v body=%q", accepted.Load(), connections.Load(), reused.Load(), writes.Load(), result, output.Body.String())
					}
					if strings.Contains(result.Error.Error.Message, "fixture-only-idempotency") {
						t.Fatal("request header reached error")
					}
				})
			}
		}
	}
}

type zeroWriteFaultConn struct {
	net.Conn
	armed  *atomic.Bool
	failed *atomic.Int64
}

func (c *zeroWriteFaultConn) Write(p []byte) (int, error) {
	if c.armed.CompareAndSwap(true, false) {
		c.failed.Add(1)
		return 0, errors.New("fixture: reused socket accepted no bytes")
	}
	return c.Conn.Write(p)
}

func TestTransportReplayZeroWriteFailureReturnsToCoordinator(t *testing.T) {
	var accepted, failures, acquired, reused atomic.Int64
	var armed atomic.Bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		if r.URL.Path == "/trial" {
			accepted.Add(1)
		}
		io.WriteString(w, `{"ok":true}`)
	}))
	defer upstream.Close()
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		conn, err := (&net.Dialer{}).DialContext(ctx, network, address)
		if err != nil {
			return nil, err
		}
		return &zeroWriteFaultConn{Conn: conn, armed: &armed, failed: &failures}, nil
	}
	e := New(Options{Client: &http.Client{Transport: transport}})
	defer e.Close()
	warm := e.Execute(context.Background(), httptest.NewRecorder(), Input{URL: upstream.URL + "/warm", Body: []byte(`{"warm":true}`), Decoder: rawDecoder{}})
	if warm.Error != nil || !warm.ProtocolComplete {
		t.Fatalf("warm: %+v", warm)
	}
	ctx := httptrace.WithClientTrace(context.Background(), &httptrace.ClientTrace{GotConn: func(info httptrace.GotConnInfo) {
		acquired.Add(1)
		if info.Reused {
			reused.Add(1)
			armed.Store(true)
		}
	}})
	result := e.Execute(ctx, httptest.NewRecorder(), Input{URL: upstream.URL + "/trial", Body: []byte(`{"request":"trial"}`), Replayable: true, Decoder: rawDecoder{}, Timeout: 2 * time.Second})
	if failures.Load() != 1 || accepted.Load() != 0 || acquired.Load() != 1 || reused.Load() != 1 || result.Error == nil || result.Phase != Dispatching || result.Committed || result.ProtocolComplete || !result.Replayable {
		t.Fatalf("zero-write recovery escaped the attempt: accepted=%d failures=%d acquired=%d reused=%d result=%+v", accepted.Load(), failures.Load(), acquired.Load(), reused.Load(), result)
	}
}

func TestTransportFaultsBeforeHTTPAcceptanceKeepUnknownAndSanitize(t *testing.T) {
	t.Run("controlled-dns", func(t *testing.T) {
		var calls atomic.Int64
		transport := http.DefaultTransport.(*http.Transport).Clone()
		transport.Proxy = nil
		transport.DialContext = func(context.Context, string, string) (net.Conn, error) {
			calls.Add(1)
			return nil, &net.DNSError{Name: "fixture-sensitive.invalid", Err: "fixture-sensitive-detail", IsNotFound: true}
		}
		e := New(Options{Client: &http.Client{Transport: transport}})
		defer e.Close()
		r := e.Execute(context.Background(), httptest.NewRecorder(), Input{URL: "http://fixture-sensitive.invalid", Body: []byte(`{"request":"trial"}`), Decoder: rawDecoder{}, Timeout: time.Second})
		if calls.Load() != 1 || r.Error == nil || r.Phase != Dispatching || r.Committed || r.ProtocolComplete || r.StatusCode != 502 || strings.Contains(r.Error.Error.Message, "fixture-sensitive") {
			t.Fatalf("DNS facts/sanitization: calls=%d result=%+v", calls.Load(), r)
		}
	})
	t.Run("tls-untrusted", func(t *testing.T) {
		var accepted atomic.Int64
		upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { accepted.Add(1); io.WriteString(w, `{"ok":true}`) }))
		defer upstream.Close()
		e := New(Options{})
		defer e.Close()
		r := e.Execute(context.Background(), httptest.NewRecorder(), Input{URL: upstream.URL, Body: []byte(`{"request":"trial"}`), Decoder: rawDecoder{}, Timeout: time.Second})
		if accepted.Load() != 0 || r.Error == nil || r.Phase != Dispatching || r.Committed || r.ProtocolComplete || r.StatusCode != 502 || strings.Contains(r.Error.Error.Message, upstream.URL) {
			t.Fatalf("TLS facts/sanitization: accepted=%d result=%+v", accepted.Load(), r)
		}
	})
}
