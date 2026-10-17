package websocket

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	ws "github.com/coder/websocket"
)

func testOptions() Options {
	return Options{Connections: 4, MaxMessageBytes: 1 << 20, HandshakeTimeout: time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: time.Second, MaxLifetime: time.Minute}
}

func testManager(t *testing.T, opts Options) *Manager {
	t.Helper()
	m, err := New(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := m.Close(ctx); err != nil {
			t.Error(err)
		}
	})
	return m
}

func dial(t *testing.T, url string, opts *ws.DialOptions) *ws.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, _, err := ws.Dial(ctx, "ws"+strings.TrimPrefix(url, "http"), opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.CloseNow() })
	return c
}

func receiveConn(t *testing.T, ch <-chan *Conn) *Conn {
	t.Helper()
	select {
	case c := <-ch:
		return c
	case <-time.After(5 * time.Second):
		t.Fatal("accept did not complete")
		return nil
	}
}

func waitClosed(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("connection did not close")
	}
}

func TestConnectionSurvivesHTTPContextAndShutdownUntilManagerClosesIt(t *testing.T) {
	m := testManager(t, testOptions())
	accepted := make(chan *Conn, 1)
	requests := make(chan context.Context, 1)
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := m.Accept(w, r)
		if err != nil {
			t.Error(err)
			return
		}
		accepted <- c
		requests <- r.Context()
	}))
	defer s.Close()
	client := dial(t, s.URL, nil)
	server := receiveConn(t, accepted)
	request := <-requests
	waitClosed(t, request.Done())
	if server.Context().Err() != nil {
		t.Fatal("HTTP handler return canceled upgraded connection")
	}
	if err := s.Config.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if m.Snapshot().Active != 1 {
		t.Fatal("HTTP shutdown pretended to own upgraded sockets")
	}
	read := make(chan error, 1)
	go func() {
		typ, data, err := server.Read(context.Background())
		if err == nil && (typ != Text || string(data) != "alive") {
			err = ErrInvalidMessage
		}
		read <- err
	}()
	if err := client.Write(context.Background(), ws.MessageText, []byte("alive")); err != nil {
		t.Fatal(err)
	}
	if err := <-read; err != nil {
		t.Fatal(err)
	}
	if err := m.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	waitClosed(t, server.Done())
	if got := m.Snapshot(); got != (Snapshot{}) {
		t.Fatalf("manager leaked connection: %+v", got)
	}
}

func TestFragmentsBinaryTextControlFramesAndMessageLimit(t *testing.T) {
	opts := testOptions()
	opts.MaxMessageBytes = 128
	m := testManager(t, opts)
	accepted := make(chan *Conn, 1)
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := m.Accept(w, r)
		if err == nil {
			accepted <- c
		}
	}))
	defer s.Close()
	client := dial(t, s.URL, nil)
	server := receiveConn(t, accepted)
	type readResult struct {
		data []byte
		err  error
	}
	result := make(chan readResult, 1)
	go func() { _, data, err := server.Read(context.Background()); result <- readResult{data: data, err: err} }()
	// Ping also needs the initiating peer's reader to consume the pong.
	client.CloseRead(context.Background())
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := client.Ping(ctx); err != nil {
		t.Fatalf("reader did not process control frame: %v", err)
	}
	writer, err := client.Writer(ctx, ws.MessageBinary)
	if err != nil {
		t.Fatal(err)
	}
	for _, part := range [][]byte{{0, 255, 1}, {2, 3, 4}} {
		if _, err := writer.Write(part); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if got := <-result; got.err != nil || !bytes.Equal(got.data, []byte{0, 255, 1, 2, 3, 4}) {
		t.Fatalf("fragments changed: %v error=%v", got.data, got.err)
	}
	oversized := make(chan error, 1)
	go func() { _, _, err := server.Read(context.Background()); oversized <- err }()
	_ = client.Write(ctx, ws.MessageBinary, bytes.Repeat([]byte("x"), 129))
	if err := <-oversized; !errors.Is(err, ErrMessageLimit) {
		t.Fatalf("message limit: %v", err)
	}
	waitClosed(t, server.Done())
}

func TestOriginPolicyIsExactAndCannotUseForwardedHeadersToBypassIt(t *testing.T) {
	opts := testOptions()
	opts.Origins = []string{"https://console.example"}
	m := testManager(t, opts)
	opts.Origins[0] = "https://attacker.example"
	handled := make(chan struct{}, 16)
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() { handled <- struct{}{} }()
		c, err := m.Accept(w, r)
		if err == nil {
			c.Close()
		}
	}))
	defer s.Close()
	for _, origin := range []string{"https://attacker.example", "null", "http://console.example", "https://console.example.evil", "https://console.example/", "https://user@console.example", "https://console.example#"} {
		_, resp, err := ws.Dial(context.Background(), "ws"+strings.TrimPrefix(s.URL, "http"), &ws.DialOptions{HTTPHeader: http.Header{"Origin": {origin}, "X-Forwarded-Proto": {"https"}}})
		if err == nil || resp == nil || resp.StatusCode != 403 {
			t.Fatalf("unsafe origin accepted: %q %v", origin, err)
		}
		<-handled
	}
	for _, origin := range []string{s.URL, "https://console.example"} {
		c, _, err := ws.Dial(context.Background(), "ws"+strings.TrimPrefix(s.URL, "http"), &ws.DialOptions{HTTPHeader: http.Header{"Origin": {origin}}})
		if err != nil {
			t.Fatalf("valid origin rejected: %s %v", origin, err)
		}
		c.CloseNow()
		<-handled
	}
	if m.Snapshot() != (Snapshot{}) {
		t.Fatal("origin failure leaked connection slots")
	}
}

func TestTLSSameOriginAndIPv6OriginCanonicalization(t *testing.T) {
	m := testManager(t, testOptions())
	s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := m.Accept(w, r)
		if err == nil {
			c.Close()
		}
	}))
	defer s.Close()
	c, _, err := ws.Dial(context.Background(), "wss"+strings.TrimPrefix(s.URL, "https"), &ws.DialOptions{HTTPClient: s.Client(), HTTPHeader: http.Header{"Origin": {s.URL}}})
	if err != nil {
		t.Fatal(err)
	}
	c.CloseNow()
	if got, ok := canonicalOrigin("https://[::1]:8443"); !ok || got != "https://[::1]:8443" {
		t.Fatalf("IPv6 origin: %q %v", got, ok)
	}
}

func TestReadOwnershipCancellationAndInvalidUTF8(t *testing.T) {
	m := testManager(t, testOptions())
	accepted := make(chan *Conn, 1)
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := m.Accept(w, r)
		if err == nil {
			accepted <- c
		}
	}))
	defer s.Close()
	client := dial(t, s.URL, nil)
	server := receiveConn(t, accepted)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := server.Write(ctx, Text, []byte("not sent")); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err := server.Write(context.Background(), Text, []byte{255}); !errors.Is(err, ErrInvalidMessage) {
		t.Fatal(err)
	}
	first := make(chan error, 1)
	go func() { _, _, err := server.Read(context.Background()); first <- err }()
	deadline := time.Now().Add(time.Second)
	for !server.reading.Load() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if _, _, err := server.Read(context.Background()); !errors.Is(err, ErrConcurrentRead) {
		t.Fatalf("concurrent read stole a message: %v", err)
	}
	if err := client.Write(context.Background(), ws.MessageText, []byte{255}); err != nil {
		t.Fatal(err)
	}
	if err := <-first; !errors.Is(err, ErrInvalidMessage) {
		t.Fatalf("invalid UTF-8 converted/accepted: %v", err)
	}
	waitClosed(t, server.Done())
}

func TestPeerReasonsAreNotExposedAndReadDeadlineClosesSocket(t *testing.T) {
	for _, timeout := range []bool{false, true} {
		t.Run(map[bool]string{false: "peer-reason", true: "deadline"}[timeout], func(t *testing.T) {
			opts := testOptions()
			opts.ReadTimeout = 100 * time.Millisecond
			m := testManager(t, opts)
			accepted := make(chan *Conn, 1)
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				c, err := m.Accept(w, r)
				if err == nil {
					accepted <- c
				}
			}))
			defer s.Close()
			client := dial(t, s.URL, nil)
			server := receiveConn(t, accepted)
			if !timeout {
				go client.Close(ws.StatusPolicyViolation, "private-token-content")
			}
			_, _, err := server.Read(context.Background())
			if timeout && !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("read timeout: %v", err)
			}
			if !timeout && (!errors.Is(err, ErrClosed) || strings.Contains(err.Error(), "private")) {
				t.Fatalf("peer error leaked: %v", err)
			}
			waitClosed(t, server.Done())
		})
	}
}

type smallSendBuffer struct{ http.ResponseWriter }

func (w smallSendBuffer) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w smallSendBuffer) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	c, r, err := http.NewResponseController(w.ResponseWriter).Hijack()
	if tcp, ok := c.(*net.TCPConn); ok {
		_ = tcp.SetWriteBuffer(1024)
	}
	return c, r, err
}

func TestRealSlowReaderBoundsWriteAndReleasesConnection(t *testing.T) {
	opts := testOptions()
	opts.WriteTimeout = 150 * time.Millisecond
	m := testManager(t, opts)
	accepted := make(chan *Conn, 1)
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := m.Accept(smallSendBuffer{w}, r)
		if err == nil {
			accepted <- c
		}
	}))
	defer s.Close()
	transport := &http.Transport{DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		c, err := (&net.Dialer{}).DialContext(ctx, network, address)
		if tcp, ok := c.(*net.TCPConn); ok {
			_ = tcp.SetReadBuffer(1024)
		}
		return c, err
	}}
	defer transport.CloseIdleConnections()
	_ = dial(t, s.URL, &ws.DialOptions{HTTPClient: &http.Client{Transport: transport}})
	server := receiveConn(t, accepted)
	var err error
	for range 16 {
		err = server.Write(context.Background(), Binary, bytes.Repeat([]byte("x"), 1<<20))
		if err != nil {
			break
		}
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("slow peer had unbounded output or wrong failure: %v", err)
	}
	waitClosed(t, server.Done())
	if m.Snapshot().Active != 0 {
		t.Fatal("slow peer retained physical capacity")
	}
}

func TestCapacityFailedHandshakeAndConcurrentClose(t *testing.T) {
	opts := testOptions()
	opts.Connections = 1
	m := testManager(t, opts)
	accepted := make(chan *Conn, 1)
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := m.Accept(w, r)
		if err == nil {
			accepted <- c
		}
	}))
	defer s.Close()
	resp, err := http.Get(s.URL)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 426 || m.Snapshot() != (Snapshot{}) {
		t.Fatalf("failed handshake retained reservation: %d %+v", resp.StatusCode, m.Snapshot())
	}
	_ = dial(t, s.URL, nil)
	server := receiveConn(t, accepted)
	_, resp, err = ws.Dial(context.Background(), "ws"+strings.TrimPrefix(s.URL, "http"), nil)
	if err == nil || resp == nil || resp.StatusCode != 503 {
		t.Fatalf("connection cap bypassed: %v", err)
	}
	var wg sync.WaitGroup
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			server.Close()
			if err := m.Close(context.Background()); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if m.Snapshot() != (Snapshot{}) {
		t.Fatal("concurrent close leaked reservation")
	}
}

type delayedHijacker struct {
	http.ResponseWriter
	entered chan<- struct{}
	release <-chan struct{}
}

func (w delayedHijacker) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w delayedHijacker) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	close(w.entered)
	<-w.release
	return http.NewResponseController(w.ResponseWriter).Hijack()
}

func TestShutdownJoinsPendingHandshakeAndFailsClosedWithoutDeadlines(t *testing.T) {
	m := testManager(t, testOptions())
	unsupported := httptest.NewRecorder()
	if _, err := m.Accept(unsupported, httptest.NewRequest(http.MethodGet, "http://example.test", nil)); !errors.Is(err, ErrHandshake) || unsupported.Code != 503 || m.Snapshot() != (Snapshot{}) {
		t.Fatalf("unsupported handshake bypassed deadline/retained capacity: %v %+v", err, m.Snapshot())
	}
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	accepted := make(chan error, 1)
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		_, err := m.Accept(delayedHijacker{ResponseWriter: w, entered: entered, release: release}, req)
		accepted <- err
	}))
	defer s.Close()
	defer unblock()
	dialed := make(chan struct{})
	go func() {
		defer close(dialed)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		c, _, _ := ws.Dial(ctx, "ws"+strings.TrimPrefix(s.URL, "http"), nil)
		if c != nil {
			_ = c.CloseNow()
		}
	}()
	waitClosed(t, entered)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if err := m.Close(ctx); !errors.Is(err, context.DeadlineExceeded) || m.Snapshot().Pending != 1 {
		t.Fatalf("shutdown declared blocked handshake finished: %v %+v", err, m.Snapshot())
	}
	unblock()
	if err := m.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := <-accepted; err == nil {
		t.Fatal("handshake registered after transport shutdown")
	}
	waitClosed(t, dialed)
	if m.Snapshot() != (Snapshot{}) {
		t.Fatal("pending handshake reservation was not joined")
	}
}

func TestInvalidTransportConfigurationAndLifetimeExpiry(t *testing.T) {
	for _, modify := range []func(*Options){func(o *Options) { o.Connections = 0 }, func(o *Options) { o.MaxMessageBytes = 0 }, func(o *Options) { o.Connections = 4096 }, func(o *Options) { o.Origins = []string{"https://*"} }, func(o *Options) { o.WriteTimeout = 0 }, func(o *Options) { o.MaxLifetime = 0 }} {
		o := testOptions()
		modify(&o)
		if m, err := New(context.Background(), o); err == nil {
			m.Close(context.Background())
			t.Fatal("unbounded transport config accepted")
		}
	}
	opts := testOptions()
	opts.ReadTimeout = 20 * time.Millisecond
	opts.MaxLifetime = 50 * time.Millisecond
	m := testManager(t, opts)
	accepted := make(chan *Conn, 1)
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := m.Accept(w, r)
		if err == nil {
			accepted <- c
		}
	}))
	defer s.Close()
	_ = dial(t, s.URL, nil)
	server := receiveConn(t, accepted)
	waitClosed(t, server.Done())
	if !errors.Is(server.Context().Err(), context.DeadlineExceeded) {
		t.Fatal("absolute connection lifetime was renewed implicitly")
	}
}
