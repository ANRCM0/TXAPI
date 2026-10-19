package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// This only checks the deployment harness: its CA, protocol fixture, client and
// Upgrade proxy. It is not evidence about the shipped gateway or Docker wiring.
func TestIsolatedFixtureTLSAndProxy(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	directory := t.TempDir()
	if err := prepare(directory); err != nil {
		t.Fatal(err)
	}
	certificate, err := tls.LoadX509KeyPair(filepath.Join(directory, "server.pem"), filepath.Join(directory, "server-key.pem"))
	if err != nil {
		t.Fatal(err)
	}
	ca, err := os.ReadFile(filepath.Join(directory, "ca.pem"))
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(ca) {
		t.Fatal("fixture CA missing")
	}
	f := &fixture{root: ctx}
	provider := httptest.NewUnstartedServer(http.HandlerFunc(f.provider))
	provider.TLS = &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{certificate}}
	provider.StartTLS()
	t.Cleanup(provider.Close)
	transport := &http.Transport{Proxy: nil, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots},
		DialContext: func(ctx context.Context, _, address string) (net.Conn, error) {
			if address != "api.openai.com:443" {
				t.Error("unexpected fixture TLS destination", address)
			}
			return (&net.Dialer{}).DialContext(ctx, "tcp", provider.Listener.Addr().String())
		}}
	t.Cleanup(transport.CloseIdleConnections)
	client := &http.Client{Transport: transport}
	requestHTTP, err := http.NewRequestWithContext(ctx, "POST", "https://api.openai.com/v1/responses",
		strings.NewReader(`{"model":"m","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"HTTP root"}]}],"store":true}`))
	if err != nil {
		t.Fatal(err)
	}
	requestHTTP.Header.Set("Authorization", "Bearer "+upstreamKey)
	response, err := client.Do(requestHTTP)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatal("fixture rejected local-history HTTP input", response.StatusCode)
	}
	connection, _, err := websocket.Dial(ctx, "wss://api.openai.com/v1/responses", &websocket.DialOptions{HTTPClient: client,
		HTTPHeader: http.Header{"Authorization": {"Bearer " + upstreamKey}}})
	if err != nil {
		t.Fatal("fixture signed TLS handshake", err)
	}
	request := `{"type":"response.create","model":"m","input":[{"role":"user","content":"fixture"}],"store":false,"stream_id":"main"}`
	if err := connection.Write(ctx, websocket.MessageText, []byte(request)); err != nil {
		t.Fatal(err)
	}
	terminal := false
	for count := 0; count < 8 && !terminal; count++ {
		_, raw, err := connection.Read(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var event struct{ Type string }
		if json.Unmarshal(raw, &event) != nil {
			t.Fatal("fixture emitted invalid event")
		}
		terminal = event.Type == "response.completed"
	}
	_ = connection.CloseNow()
	if !terminal {
		t.Fatal("fixture terminal missing")
	}
	// Simulate only the provider wire contract so the fixture client and proxy
	// can be checked on Windows before the real Linux image is available.
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Header.Del("Cookie")
		r.Header.Del("X-Forwarded-For")
		f.provider(w, r)
	}))
	t.Cleanup(backend.Close)
	proxy, err := proxyFor(backend.URL, f)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(proxy.Transport.(*http.Transport).CloseIdleConnections)
	front := httptest.NewServer(proxy)
	t.Cleanup(front.Close)
	var events bytes.Buffer
	if err := wsCall(ctx, front.Client(), front.URL, upstreamKey, request, "terminal", &events); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(events.Bytes(), []byte("你好🌏")) || f.upgrades.Load() != 1 {
		t.Fatal("proxy failed to forward complete fixture events")
	}
	// The certificate must be bound to the fixture's official-address alias.
	parsed, err := x509.ParseCertificate(certificate.Certificate[0])
	if err != nil || parsed.VerifyHostname("other.invalid") == nil {
		t.Fatal("fixture certificate accepted a different host")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.requests) != 3 || len(f.errors) != 0 || f.requests[0].InputKind != "array" || !f.requests[0].Store || f.requests[1].Store {
		t.Fatal("fixture observations did not match controlled requests", f.errors)
	}
}
