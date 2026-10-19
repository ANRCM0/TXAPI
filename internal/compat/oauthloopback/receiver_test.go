package oauthloopback

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	auth "minigateway/internal/foundation/contracts/upstreamauth/v1"
	"minigateway/internal/protocol/openaiauth"
)

func opened(t *testing.T, ctx context.Context) (*session, auth.Flow) {
	t.Helper()
	p := openaiauth.New()
	value, err := New(p).Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	s := value.(*session)
	t.Cleanup(func() { _ = s.Close() })
	flow, err := p.Begin("", "1c343890-4575-4f24-925e-bfce1ed81833", s.RedirectURI(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return s, flow
}

func callback(flow auth.Flow) string {
	return url.Values{"state": {flow.State.Value()}, "code": {"secret-code"}, "client_id": {"oaiapp_issued"}}.Encode()
}

func client(t *testing.T) *http.Client {
	t.Helper()
	transport := &http.Transport{DisableKeepAlives: true}
	t.Cleanup(transport.CloseIdleConnections)
	return &http.Client{Transport: transport, Timeout: 3 * time.Second}
}

func readResponse(t *testing.T, response *http.Response) int {
	t.Helper()
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 4096))
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"secret-code", "access_denied", "secret-description", "state="} {
		if strings.Contains(string(body), secret) {
			t.Fatal("callback response disclosed input")
		}
	}
	if response.Header.Get("Cache-Control") != "no-store" || response.Header.Get("Referrer-Policy") != "no-referrer" || response.Header.Get("X-Content-Type-Options") != "nosniff" || !strings.Contains(response.Header.Get("Content-Security-Policy"), "default-src 'none'") {
		t.Fatal("callback privacy headers missing")
	}
	return response.StatusCode
}

func get(t *testing.T, c *http.Client, target string) int {
	t.Helper()
	r, err := c.Get(target)
	if err != nil {
		t.Fatal(err)
	}
	return readResponse(t, r)
}

func assertClosed(t *testing.T, s *session) {
	t.Helper()
	for name, done := range map[string]<-chan struct{}{"serve": s.serveDone, "watch": s.watchDone} {
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Fatalf("%s goroutine did not join", name)
		}
	}
	conn, err := net.DialTimeout("tcp4", s.host, 100*time.Millisecond)
	if err == nil {
		conn.Close()
		t.Fatal("callback listener remains reachable")
	}
}

func TestCallbackBeforeWaitAndWrongStateDoesNotConsume(t *testing.T) {
	s, flow := opened(t, context.Background())
	c := client(t)
	if get(t, c, s.RedirectURI()+"?"+callback(flow)) != 400 {
		t.Fatal("unbound callback accepted")
	}
	if err := s.Bind(flow); err != nil {
		t.Fatal(err)
	}
	if err := s.Bind(flow); err != auth.ErrConflict {
		t.Fatal("rebound flow accepted")
	}
	bad, _ := url.ParseQuery(callback(flow))
	bad.Set("state", "wrong")
	if get(t, c, s.RedirectURI()+"?"+bad.Encode()) != 400 {
		t.Fatal("wrong state accepted")
	}
	if get(t, c, s.RedirectURI()+"?"+callback(flow)) != 200 {
		t.Fatal("callback before Wait was lost")
	}
	code, err := s.Wait(context.Background())
	if err != nil || code.Value.Value() != "secret-code" || code.ClientID != "oaiapp_issued" {
		t.Fatal("callback result mismatch", err)
	}
	if _, err := s.Wait(context.Background()); err != auth.ErrConflict {
		t.Fatal("second Wait accepted")
	}
	assertClosed(t, s)
}

func TestMalformedRequestsDoNotConsumeFlow(t *testing.T) {
	s, flow := opened(t, context.Background())
	if err := s.Bind(flow); err != nil {
		t.Fatal(err)
	}
	c := client(t)
	for name, change := range map[string]func(*http.Request){
		"cross host":              func(r *http.Request) { r.Host = "localhost:" + strings.Split(s.host, ":")[1] },
		"wrong path":              func(r *http.Request) { r.URL.Path = "/callback" },
		"escaped path":            func(r *http.Request) { r.URL.RawPath = "/auth/%63allback" },
		"post":                    func(r *http.Request) { r.Method = "POST" },
		"body":                    func(r *http.Request) { r.Body = io.NopCloser(strings.NewReader("body")); r.ContentLength = 4 },
		"duplicate state":         func(r *http.Request) { r.URL.RawQuery += "&state=" + flow.State.Value() },
		"duplicate unknown":       func(r *http.Request) { r.URL.RawQuery += "&extra=1&extra=2" },
		"invalid escape":          func(r *http.Request) { r.URL.RawQuery += "&bad=%xx" },
		"wrong issuer with error": func(r *http.Request) { r.URL.RawQuery += "&error=access_denied&iss=https%3A%2F%2Fwrong.example" },
		"oversized query":         func(r *http.Request) { r.URL.RawQuery = strings.Repeat("x", 16*1024+1) },
	} {
		t.Run(name, func(t *testing.T) {
			r, err := http.NewRequest(http.MethodGet, s.RedirectURI()+"?"+callback(flow), nil)
			if err != nil {
				t.Fatal(err)
			}
			change(r)
			response, err := c.Do(r)
			if err != nil {
				t.Fatal(err)
			}
			if readResponse(t, response) != 400 {
				t.Fatal("malformed callback accepted")
			}
		})
	}
	if get(t, c, s.RedirectURI()+"?"+callback(flow)) != 200 {
		t.Fatal("invalid request consumed flow")
	}
	if _, err := s.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func rawRequest(t *testing.T, s *session, wire string) int {
	t.Helper()
	conn, err := net.DialTimeout("tcp4", s.host, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	if _, err := io.WriteString(conn, wire); err != nil {
		t.Fatal(err)
	}
	response, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatal(err)
	}
	return readResponse(t, response)
}

func TestAbsoluteURIAndChunkedBodyAreRejected(t *testing.T) {
	s, flow := opened(t, context.Background())
	if err := s.Bind(flow); err != nil {
		t.Fatal(err)
	}
	query := callback(flow)
	for _, wire := range []string{
		fmt.Sprintf("GET %s?%s HTTP/1.1\r\nHost: %s\r\nConnection: close\r\n\r\n", s.RedirectURI(), query, s.host),
		fmt.Sprintf("GET /auth/callback?%s HTTP/1.1\r\nHost: %s\r\nTransfer-Encoding: chunked\r\nConnection: close\r\n\r\n0\r\n\r\n", query, s.host),
	} {
		if rawRequest(t, s, wire) != 400 {
			t.Fatal("absolute URI/body accepted")
		}
	}
	req := httptest.NewRequest(http.MethodGet, s.RedirectURI()+"?"+query, nil)
	req.URL.Scheme = ""
	req.URL.Host = ""
	req.Host = s.host
	req.RemoteAddr = "203.0.113.5:1234"
	response := httptest.NewRecorder()
	s.handle(response, req)
	if response.Code != 400 {
		t.Fatal("nonloopback remote accepted")
	}
	if get(t, client(t), s.RedirectURI()+"?"+query) != 200 {
		t.Fatal("bad request consumed flow")
	}
	if _, err := s.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestCorrectOAuthErrorTerminatesFlow(t *testing.T) {
	s, flow := opened(t, context.Background())
	if err := s.Bind(flow); err != nil {
		t.Fatal(err)
	}
	c := client(t)
	query := url.Values{"state": {flow.State.Value()}, "error": {"access_denied"}, "error_description": {"secret-description"}, "iss": {"https://auth.openai.com"}}
	if get(t, c, s.RedirectURI()+"?"+query.Encode()) != 200 {
		t.Fatal("bound rejection not consumed")
	}
	if get(t, c, s.RedirectURI()+"?"+callback(flow)) != 409 {
		t.Fatal("rejected attempt reused")
	}
	if _, err := s.Wait(context.Background()); err != auth.ErrRejected {
		t.Fatal("OAuth rejection lost", err)
	}
	assertClosed(t, s)
}

func TestConcurrentCallbacksHaveOneWinner(t *testing.T) {
	s, flow := opened(t, context.Background())
	if err := s.Bind(flow); err != nil {
		t.Fatal(err)
	}
	c := client(t)
	results := make(chan int, 2)
	errors := make(chan error, 2)
	var workers sync.WaitGroup
	for range 2 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			r, err := c.Get(s.RedirectURI() + "?" + callback(flow))
			if err != nil {
				errors <- err
				return
			}
			defer r.Body.Close()
			_, _ = io.Copy(io.Discard, r.Body)
			results <- r.StatusCode
		}()
	}
	workers.Wait()
	close(results)
	close(errors)
	for err := range errors {
		t.Fatal(err)
	}
	counts := map[int]int{}
	for status := range results {
		counts[status]++
	}
	if counts[200] != 1 || counts[409] != 1 {
		t.Fatal("callback winner not exclusive", counts)
	}
	if _, err := s.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
	assertClosed(t, s)
}

func TestWaitCancelSecondWaitAndIdempotentClose(t *testing.T) {
	s, flow := opened(t, context.Background())
	if err := s.Bind(flow); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := s.Wait(ctx); done <- err }()
	deadline := time.Now().Add(time.Second)
	for {
		s.mu.Lock()
		waited := s.waited
		s.mu.Unlock()
		if waited {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("Wait not started")
		}
		time.Sleep(time.Millisecond)
	}
	if _, err := s.Wait(context.Background()); err != auth.ErrConflict {
		t.Fatal("concurrent Wait accepted")
	}
	cancel()
	select {
	case err := <-done:
		if err != context.Canceled {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("cancel did not finish Wait")
	}
	var workers sync.WaitGroup
	for range 4 {
		workers.Add(1)
		go func() { defer workers.Done(); _ = s.Close() }()
	}
	workers.Wait()
	assertClosed(t, s)
}

func TestOpenContextAndFlowDeadlineCloseWithoutCallback(t *testing.T) {
	t.Run("open canceled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := New(openaiauth.New()).Open(ctx); err != context.Canceled {
			t.Fatal(err)
		}
	})
	t.Run("unbound cancellation", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		s, _ := opened(t, ctx)
		cancel()
		assertClosed(t, s)
	})
	t.Run("flow deadline", func(t *testing.T) {
		s, flow := opened(t, context.Background())
		flow.ExpiresAt = time.Now().Add(50 * time.Millisecond)
		if err := s.Bind(flow); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Wait(context.Background()); err != context.DeadlineExceeded {
			t.Fatal(err)
		}
		assertClosed(t, s)
	})
	t.Run("server failure", func(t *testing.T) {
		s, flow := opened(t, context.Background())
		if err := s.Bind(flow); err != nil {
			t.Fatal(err)
		}
		_ = s.listener.Close()
		if _, err := s.Wait(context.Background()); err != auth.ErrUnavailable {
			t.Fatal(err)
		}
		assertClosed(t, s)
	})
}

func TestBindRejectsInvalidLifetimeAndURI(t *testing.T) {
	s, flow := opened(t, context.Background())
	for _, change := range []func(*auth.Flow){func(f *auth.Flow) { f.RedirectURI += "/wrong" }, func(f *auth.Flow) { f.State = auth.NewSecret("") }, func(f *auth.Flow) { f.ExpiresAt = time.Now().Add(-time.Second) }, func(f *auth.Flow) { f.ExpiresAt = time.Now().Add(11 * time.Minute) }} {
		candidate := flow
		change(&candidate)
		if err := s.Bind(candidate); err != auth.ErrInvalid {
			t.Fatal("invalid binding accepted", err)
		}
	}
	if _, err := s.Wait(context.Background()); err != auth.ErrInvalid {
		t.Fatal("unbound Wait accepted")
	}
	if err := s.Bind(flow); err != nil {
		t.Fatal(err)
	}
	_ = s.Close()
	assertClosed(t, s)
}

func TestCloseBoundsSlowHeaderConnection(t *testing.T) {
	s, _ := opened(t, context.Background())
	conn, err := net.DialTimeout("tcp4", s.host, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_, _ = io.WriteString(conn, "GET /auth/callback HTTP/1.1\r\nHost:")
	started := time.Now()
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if time.Since(started) > 3*time.Second {
		t.Fatal("Close exceeded bounded shutdown")
	}
	assertClosed(t, s)
}
