package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"minigateway/internal/access"
	"minigateway/internal/billing"
	"minigateway/internal/config"
	"minigateway/internal/controlplane"
	authentication "minigateway/internal/foundation/identity"
	"minigateway/internal/foundation/persistence"
	"minigateway/internal/gateway"
	"minigateway/internal/management"
	"minigateway/internal/model"
	"minigateway/internal/protocol"
	"minigateway/internal/server"
	"minigateway/internal/server/middleware"
	"minigateway/internal/store"
)

// These are network/ownership regressions, not throughput or memory rankings.
// Every generation crosses actual TLS sockets, production auth/admission and
// the existing executor. TLS trust is restricted to httptest's certificate.
type p0NetworkAttempt struct {
	hits   atomic.Int64
	writes atomic.Int64
	active atomic.Int64
	proto  atomic.Int64
}

type p0NetworkFixture struct {
	upstream, gateway, proxy *httptest.Server
	client                   *http.Client
	capacity                 *access.Capacity
	key                      string
	mu                       sync.Mutex
	attempts                 map[string]*p0NetworkAttempt
	stop                     chan struct{}
	gatewayH2, proxyH2       atomic.Int64
}

func p0NetworkWait(t *testing.T, why string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatal(why)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func (f *p0NetworkFixture) attempt(id string) *p0NetworkAttempt {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.attempts[id] == nil {
		f.attempts[id] = &p0NetworkAttempt{}
	}
	return f.attempts[id]
}

func p0NetworkTLS(handler http.Handler) *httptest.Server {
	s := httptest.NewUnstartedServer(handler)
	s.EnableHTTP2 = true
	s.Config.ErrorLog = log.New(io.Discard, "", 0)
	s.StartTLS()
	return s
}

func (f *p0NetworkFixture) upstreamHandler(t *testing.T) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer synthetic-network-upstream" || r.URL.Path != "/v1/chat/completions" {
			t.Error("unexpected upstream authentication/path")
			w.WriteHeader(400)
			return
		}
		var body struct {
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		if json.NewDecoder(r.Body).Decode(&body) != nil || len(body.Messages) != 1 {
			t.Error("unexpected upstream body")
			w.WriteHeader(400)
			return
		}
		id := body.Messages[0].Content
		a := f.attempt(id)
		a.hits.Add(1)
		a.active.Add(1)
		a.proto.Store(int64(r.ProtoMajor))
		defer a.active.Add(-1)
		w.Header().Set("Content-Type", "text/event-stream")
		chunk := "data: {\"id\":\"chat_network\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"你好🌏\"},\"finish_reason\":null}]}\n\n"
		if strings.HasPrefix(id, "flood-") {
			// Bounded generated content, no unbounded producer queue. A small
			// receiver window below must stop these real socket writes before cap.
			chunk = "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"" + strings.Repeat("x", 32<<10) + "\"},\"finish_reason\":null}]}\n\n"
			for i := 0; i < 1024; i++ {
				select {
				case <-r.Context().Done():
					return
				case <-f.stop:
					return
				default:
				}
				if _, err := io.WriteString(w, chunk); err != nil {
					return
				}
				if http.NewResponseController(w).Flush() != nil {
					return
				}
				a.writes.Add(1)
			}
			return
		}
		if _, err := io.WriteString(w, chunk); err != nil {
			return
		}
		if http.NewResponseController(w).Flush() != nil {
			return
		}
		a.writes.Add(1)
		if !strings.HasPrefix(id, "cancel-") {
			io.WriteString(w, "data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
			if http.NewResponseController(w).Flush() != nil {
				return
			}
		}
		// Keep the upstream transport open after semantic completion as well as
		// during cancelled requests: EOF alone cannot make these tests pass.
		select {
		case <-r.Context().Done():
		case <-f.stop:
		}
	})
}

func newP0NetworkFixture(t *testing.T, total time.Duration, keySlots int) *p0NetworkFixture {
	t.Helper()
	f := &p0NetworkFixture{attempts: make(map[string]*p0NetworkAttempt), stop: make(chan struct{})}
	st, err := store.Open(config.DatabaseConfig{Path: filepath.Join(t.TempDir(), "network.db"), CredentialKeysFile: runtimeKeyFile(t, "network-credentials", 103, 107)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	owner, err := st.BootstrapAdmin("network-admin", "synthetic-network-password")
	if err != nil {
		t.Fatal(err)
	}
	key, err := st.IssueToken(owner.ID, &store.TokenInput{Name: "network-key", ModelMode: access.All})
	if err != nil {
		t.Fatal(err)
	}
	f.key = key.Key
	f.upstream = p0NetworkTLS(f.upstreamHandler(t))
	t.Cleanup(f.upstream.Close)
	if _, err := st.CreateChannel(&store.ChannelInput{Name: "network-upstream", Type: "openai", BaseURL: f.upstream.URL, APIKey: "synthetic-network-upstream", Models: []string{"m"}, Weight: 1, Status: model.StatusEnabled}); err != nil {
		t.Fatal(err)
	}
	limiter := access.NewMemoryLimiter()
	t.Cleanup(limiter.(interface{ Close() }).Close)
	f.capacity, err = access.NewCapacity(access.CapacityConfig{Instance: keySlots + 2, User: keySlots + 1, Key: keySlots, Channel: keySlots})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(f.capacity.Close)
	upstreamClient := f.upstream.Client()
	upstreamClient.Timeout = 12 * time.Second
	gw, err := gateway.New(gateway.Options{Protocols: protocol.Protocols(nil), Operations: protocol.Operations(), Channels: st.Channels(),
		BillingMode: billing.ModeDisabled, RetryTimes: 3, TotalTimeout: total, AttemptTimeout: 12 * time.Second,
		Limiter: limiter, Admission: gateway.CapacityAdmission{Capacity: f.capacity}, Client: upstreamClient})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(gw.Close)
	engine := server.NewEngine(server.Deps{Generation: gateway.NewHTTPExecutor(gw), TokenAuth: authentication.NewTokenAuthenticator(st),
		Control: controlplane.New(persistence.Control(st)), Metrics: gw.Metrics(), AdminAuth: middleware.AdminAuth("synthetic-network-admin"),
		Configuration: management.NewConfiguration(persistence.Configuration(st), nil, nil),
		RateLimiter:   limiter, RateGate: access.NewRateGate(limiter)})
	f.gateway = p0NetworkTLS(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ProtoMajor == 2 {
			f.gatewayH2.Add(1)
		}
		engine.ServeHTTP(w, r)
	}))
	t.Cleanup(f.gateway.Close)
	upstreamURL, _ := url.Parse(f.gateway.URL)
	proxy := httputil.NewSingleHostReverseProxy(upstreamURL)
	proxy.Transport = f.gateway.Client().Transport
	proxy.FlushInterval = -1
	proxy.ErrorLog = log.New(io.Discard, "", 0)
	f.proxy = p0NetworkTLS(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ProtoMajor == 2 {
			f.proxyH2.Add(1)
		}
		proxy.ServeHTTP(w, r)
	}))
	t.Cleanup(f.proxy.Close)
	f.client = f.proxy.Client()
	f.client.Timeout = 12 * time.Second
	// Last registered cleanup runs first, ensuring held upstream handlers and
	// raw-client writes cannot keep server.Close waiting after a failed assertion.
	t.Cleanup(func() {
		close(f.stop)
		f.proxy.CloseClientConnections()
		f.gateway.CloseClientConnections()
		f.upstream.CloseClientConnections()
	})
	return f
}

func p0NetworkBody(id string) []byte {
	body, _ := json.Marshal(map[string]any{"model": "m", "stream": true, "messages": []map[string]string{{"role": "user", "content": id}}})
	return body
}

func (f *p0NetworkFixture) request(t *testing.T, ctx context.Context, id string) *http.Response {
	t.Helper()
	r, err := http.NewRequestWithContext(ctx, http.MethodPost, f.proxy.URL+"/v1/chat/completions", bytes.NewReader(p0NetworkBody(id)))
	if err != nil {
		t.Fatal(err)
	}
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer "+f.key)
	response, err := f.client.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func (f *p0NetworkFixture) drained(t *testing.T, id string) {
	t.Helper()
	a := f.attempt(id)
	p0NetworkWait(t, "upstream/production admission did not drain: "+id, func() bool {
		s := f.capacity.Snapshot()
		return a.active.Load() == 0 && s.Requests == 0 && s.Attempts == 0 && s.Users == 0 && s.Keys == 0 && s.Channels == 0
	})
	if a.hits.Load() != 1 || a.proto.Load() != 2 {
		t.Fatalf("upstream attempt/protocol changed: %d/%d", a.hits.Load(), a.proto.Load())
	}
}

func (f *p0NetworkFixture) probe(t *testing.T, id string) {
	t.Helper()
	r := f.request(t, context.Background(), id)
	body, err := io.ReadAll(r.Body)
	r.Body.Close()
	if err != nil || r.StatusCode != 200 || r.ProtoMajor != 2 || !bytes.Contains(body, []byte("你好🌏")) || bytes.Count(body, []byte("[DONE]")) != 1 {
		t.Fatalf("HTTP2 SSE content/terminal: status=%d proto=%d err=%v body=%q", r.StatusCode, r.ProtoMajor, err, body)
	}
	f.drained(t, id)
}

func TestP0NetworkTLSHTTP2ReverseProxyTerminalAndAdmission(t *testing.T) {
	f := newP0NetworkFixture(t, 5*time.Second, 1)
	f.probe(t, "terminal-first")
	f.probe(t, "terminal-after-release")
	if f.gatewayH2.Load() != 2 || f.proxyH2.Load() != 2 {
		t.Fatal("reverse proxy did not negotiate HTTP2 on each TLS hop")
	}
}

func TestP0NetworkSlowSocketTotalDeadlineDrains(t *testing.T) {
	for _, slowRead := range []bool{false, true} {
		t.Run(fmt.Sprint("slow-read-", slowRead), func(t *testing.T) {
			f := newP0NetworkFixture(t, 2500*time.Millisecond, 1)
			address := strings.TrimPrefix(f.proxy.URL, "https://")
			raw, err := net.DialTimeout("tcp", address, 3*time.Second)
			if err != nil {
				t.Fatal(err)
			}
			tcp := raw.(*net.TCPConn)
			if err := tcp.SetReadBuffer(1024); err != nil {
				raw.Close()
				t.Fatal(err)
			}
			tlsConfig := f.client.Transport.(*http.Transport).TLSClientConfig.Clone()
			tlsConfig.NextProtos = []string{"http/1.1"}
			tlsConfig.ServerName = "example.com"
			// httptest's certificate includes example.com. Verification remains on.
			conn := tls.Client(raw, tlsConfig)
			defer conn.Close()
			conn.SetDeadline(time.Now().Add(10 * time.Second))
			if err := conn.Handshake(); err != nil {
				t.Fatal(err)
			}
			id := fmt.Sprint("flood-", slowRead)
			body := p0NetworkBody(id)
			if _, err := fmt.Fprintf(conn, "POST /v1/chat/completions HTTP/1.1\r\nHost: %s\r\nAuthorization: Bearer %s\r\nContent-Type: application/json\r\nContent-Length: %d\r\n\r\n%s", address, f.key, len(body), body); err != nil {
				t.Fatal(err)
			}
			a := f.attempt(id)
			p0NetworkWait(t, "unread socket never reached upstream", func() bool { return a.writes.Load() > 4 })
			readerDone := make(chan struct{})
			readContext, stopRead := context.WithCancel(context.Background())
			defer stopRead()
			if slowRead {
				go func() {
					defer close(readerDone)
					one := make([]byte, 1)
					for {
						if readContext.Err() != nil {
							return
						}
						if _, err := conn.Read(one); err != nil {
							return
						}
						select {
						case <-readContext.Done():
							return
						case <-time.After(20 * time.Millisecond):
						}
					}
				}()
			}
			// Observe an actual stalled upstream write while the socket remains
			// open and the gateway has not reached its deadline. No in-memory sink.
			stalled := false
			for deadline := time.Now().Add(1800 * time.Millisecond); time.Now().Before(deadline); {
				before := a.writes.Load()
				time.Sleep(100 * time.Millisecond)
				if before > 4 && before == a.writes.Load() && a.active.Load() == 1 {
					stalled = true
					break
				}
			}
			if !stalled {
				t.Fatal("socket did not demonstrate backpressure before deadline")
			}
			f.drained(t, id)
			if a.writes.Load() >= 1024 {
				t.Fatal("fixture exhausted bounded stream instead of cancellation")
			}
			stopRead()
			conn.Close()
			if slowRead {
				select {
				case <-readerDone:
				case <-time.After(time.Second):
					t.Fatal("slow reader did not join")
				}
			}
			f.probe(t, "probe-after-slow-socket")
		})
	}
}

func TestP0NetworkBoundedCancellationStormDrains(t *testing.T) {
	const count = 4
	f := newP0NetworkFixture(t, 8*time.Second, count)
	responses := make([]*http.Response, count)
	cancels := make([]context.CancelFunc, count)
	for i := 0; i < count; i++ {
		ctx, cancel := context.WithCancel(context.Background())
		cancels[i] = cancel
		defer cancel()
		responses[i] = f.request(t, ctx, fmt.Sprint("cancel-", i))
		if responses[i].StatusCode != 200 || responses[i].ProtoMajor != 2 {
			t.Fatal("storm request not admitted over HTTP2")
		}
		reader := bufio.NewReader(responses[i].Body)
		line, err := reader.ReadString('\n')
		if err != nil || !strings.Contains(line, "你好🌏") {
			t.Fatal("cancel before first real upstream output", err)
		}
	}
	if s := f.capacity.Snapshot(); s.Requests != count || s.Attempts != count {
		t.Fatalf("storm leases missing: %+v", s)
	}
	// Prove saturation rejects before the Provider and preserves occupied leases.
	rejected := f.request(t, context.Background(), "must-not-send")
	io.Copy(io.Discard, rejected.Body)
	rejected.Body.Close()
	if rejected.StatusCode != 429 || f.attempt("must-not-send").hits.Load() != 0 {
		t.Fatal("full Key capacity reached upstream")
	}
	var wg sync.WaitGroup
	for i := range cancels {
		wg.Add(1)
		go func(i int) { defer wg.Done(); cancels[i](); responses[i].Body.Close() }(i)
	}
	wg.Wait()
	for i := 0; i < count; i++ {
		f.drained(t, fmt.Sprint("cancel-", i))
	}
	f.probe(t, "probe-after-cancel-storm")
}
