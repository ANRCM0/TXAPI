// nativews is an isolated deployment fixture, never a production provider.
// The container network owns the api.openai.com alias and ephemeral CA. Neither
// the gateway dialer nor the host's DNS/trust configuration is replaced.
package main

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/coder/websocket"
)

const upstreamKey = "nativews-deployment-provider-fixture-only"

type observation struct {
	Transport string          `json:"transport"`
	Input     json.RawMessage `json:"input"`
	InputKind string          `json:"input_kind"`
	Model     string          `json:"model"`
	Previous  string          `json:"previous"`
	Store     bool            `json:"store"`
	Tools     json.RawMessage `json:"tools,omitempty"`
}

type fixture struct {
	root     context.Context
	mu       sync.Mutex
	requests []observation
	errors   []string
	active   atomic.Int64
	serial   atomic.Int64
	upgrades atomic.Int64
}

func (f *fixture) reject(message string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.errors) < 16 {
		f.errors = append(f.errors, message)
	}
}

func (f *fixture) stats(w http.ResponseWriter, _ *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	_ = json.NewEncoder(w).Encode(map[string]any{"requests": f.requests, "errors": f.errors,
		"active": f.active.Load(), "proxy_upgrades": f.upgrades.Load()})
}

func (f *fixture) observe(raw []byte, transport string) (map[string]any, string, error) {
	var body map[string]any
	if len(raw) > 256<<10 || json.Unmarshal(raw, &body) != nil {
		return nil, "", errors.New("invalid fixture request")
	}
	model, _ := body["model"].(string)
	previous, _ := body["previous_response_id"].(string)
	stored, ok := body["store"].(bool)
	items, array := body["input"].([]any)
	if model != "m" || !ok || previous != "" || stored != (transport == "http") {
		return nil, "", errors.New("mapped model, local parent and transport-specific store required")
	}
	kind := "array"
	if !array {
		text, valid := body["input"].(string)
		if transport != "http" || !valid || text == "" {
			return nil, "", errors.New("self-contained input string or complete item array required")
		}
		// A root HTTP request can legally retain its string representation.
		// Normalize only the observation, retaining the original wire kind.
		kind = "string"
		items = []any{map[string]any{"type": "message", "role": "user", "content": text}}
	}
	if len(items) == 0 || len(items) > 1024 {
		return nil, "", errors.New("complete context item capacity")
	}
	if transport == "ws" && (body["type"] != "response.create" || body["stream_id"] != "main") {
		return nil, "", errors.New("invalid native round envelope")
	}
	last, _ := items[len(items)-1].(map[string]any)
	text, _ := last["content"].(string)
	if parts, ok := last["content"].([]any); ok && len(parts) > 0 {
		part, _ := parts[len(parts)-1].(map[string]any)
		text, _ = part["text"].(string)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.requests) >= 64 {
		return nil, "", errors.New("fixture request capacity exhausted")
	}
	input, _ := json.Marshal(items)
	tools, _ := json.Marshal(body["tools"])
	f.requests = append(f.requests, observation{Transport: transport, Input: input, InputKind: kind, Model: model, Previous: previous, Store: stored, Tools: tools})
	return body, text, nil
}

func document(id string, output []any) map[string]any {
	return map[string]any{"id": id, "object": "response", "created_at": 1, "model": "m", "status": "completed",
		"output": output, "error": nil, "incomplete_details": nil,
		"usage": map[string]any{"input_tokens": 2, "output_tokens": 1, "total_tokens": 3}}
}

func textOutput(id string) []any {
	return []any{map[string]any{"id": "msg_" + id, "type": "message", "role": "assistant", "status": "completed",
		"content": []any{map[string]any{"type": "output_text", "text": "你好🌏", "annotations": []any{}}}}}
}

func (f *fixture) provider(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/v1/responses" || r.Header.Get("Authorization") != "Bearer "+upstreamKey ||
		r.Header.Get("Cookie") != "" || r.Header.Get("X-Forwarded-For") != "" || r.Header.Get("Proxy-Authorization") != "" {
		f.reject("provider path, authentication or private-header boundary")
		http.Error(w, "fixture boundary", 403)
		return
	}
	if r.Method == http.MethodPost {
		raw, err := io.ReadAll(io.LimitReader(r.Body, (256<<10)+1))
		if err == nil {
			_, _, err = f.observe(raw, "http")
		}
		if err != nil {
			f.reject("HTTP fixture: " + err.Error())
			http.Error(w, "fixture request", 400)
			return
		}
		id := fmt.Sprintf("native_%d", f.serial.Add(1))
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(document(id, textOutput(id)))
		return
	}
	connection, err := websocket.Accept(w, r, nil)
	if err != nil {
		f.reject("provider WebSocket upgrade")
		return
	}
	connection.SetReadLimit(256 << 10)
	ctx, cancel := context.WithTimeout(f.root, 40*time.Second)
	stop := context.AfterFunc(ctx, func() { _ = connection.CloseNow() })
	f.active.Add(1)
	defer func() { cancel(); stop(); _ = connection.CloseNow(); f.active.Add(-1) }()
	write := func(value any) error {
		raw, err := json.Marshal(value)
		if err != nil {
			return err
		}
		return connection.Write(ctx, websocket.MessageText, raw)
	}
	for {
		typ, raw, err := connection.Read(ctx)
		if err != nil {
			return
		}
		body, text, err := f.observe(raw, "ws")
		if err != nil || typ != websocket.MessageText {
			if err != nil {
				f.reject("WS fixture: " + err.Error())
			} else {
				f.reject("WS fixture requires text frame")
			}
			return
		}
		id := fmt.Sprintf("native_%d", f.serial.Add(1))
		created := document(id, []any{})
		created["status"] = "in_progress"
		if write(map[string]any{"type": "response.created", "stream_id": "main", "sequence_number": 0, "response": created}) != nil {
			return
		}
		if text == "block" {
			// The next read watches for the actual peer disconnect. There is
			// exactly one reader, including at this intentional in-flight gate.
			_, _, _ = connection.Read(ctx)
			return
		}
		output := textOutput(id)
		events := []map[string]any{{"type": "response.output_text.delta", "sequence_number": 1, "delta": "你好🌏"}}
		if tools, ok := body["tools"].([]any); ok && len(tools) > 0 {
			item := map[string]any{"id": "fc_" + id, "type": "function_call", "call_id": "call_" + id, "name": "weather", "arguments": "{}", "status": "completed"}
			output = []any{item}
			events = []map[string]any{
				{"type": "response.output_item.added", "sequence_number": 1, "output_index": 0, "item": map[string]any{"id": "fc_" + id, "type": "function_call", "call_id": "call_" + id, "name": "weather", "arguments": ""}},
				{"type": "response.function_call_arguments.delta", "sequence_number": 2, "output_index": 0, "item_id": "fc_" + id, "delta": "{"},
				{"type": "response.function_call_arguments.delta", "sequence_number": 3, "output_index": 0, "item_id": "fc_" + id, "delta": "}"},
				{"type": "response.function_call_arguments.done", "sequence_number": 4, "output_index": 0, "item_id": "fc_" + id, "arguments": "{}"},
				{"type": "response.output_item.done", "sequence_number": 5, "output_index": 0, "item": item},
			}
		}
		for _, event := range events {
			event["stream_id"] = "main"
			if write(event) != nil {
				return
			}
		}
		status := "completed"
		terminal := document(id, output)
		if text == "failed" || text == "incomplete" {
			status = text
			terminal["status"], terminal["usage"] = status, nil
		}
		if write(map[string]any{"type": "response." + status, "stream_id": "main", "sequence_number": 6, "response": terminal}) != nil {
			return
		}
	}
}

func prepare(directory string) error {
	if err := os.MkdirAll(directory, 0755); err != nil {
		return err
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	ca := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "TXAPI ephemeral isolated test CA"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature}
	caDER, err := x509.CreateCertificate(rand.Reader, ca, ca, &key.PublicKey, key)
	if err != nil {
		return err
	}
	server := &x509.Certificate{SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "api.openai.com"}, DNSNames: []string{"api.openai.com"},
		NotBefore: ca.NotBefore, NotAfter: ca.NotAfter, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	serverDER, err := x509.CreateCertificate(rand.Reader, server, ca, &key.PublicKey, key)
	if err != nil {
		return err
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return err
	}
	for name, block := range map[string]*pem.Block{"ca.pem": {Type: "CERTIFICATE", Bytes: caDER}, "server.pem": {Type: "CERTIFICATE", Bytes: serverDER}, "server-key.pem": {Type: "PRIVATE KEY", Bytes: keyDER}} {
		if err := os.WriteFile(filepath.Join(directory, name), pem.EncodeToMemory(block), 0600); err != nil {
			return err
		}
	}
	// The gateway is non-root; only the public fixture CA needs to be readable.
	return os.Chmod(filepath.Join(directory, "ca.pem"), 0644)
}

func proxyFor(target string, f *fixture) (*httputil.ReverseProxy, error) {
	u, err := url.Parse(target)
	if err != nil || u.Scheme != "http" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("explicit internal proxy target required")
	}
	proxy := httputil.NewSingleHostReverseProxy(u)
	proxy.Transport = &http.Transport{Proxy: nil, ResponseHeaderTimeout: 5 * time.Second}
	proxy.ModifyResponse = func(response *http.Response) error {
		if response.StatusCode == http.StatusSwitchingProtocols {
			f.upgrades.Add(1)
		}
		return nil
	}
	return proxy, nil
}

func serve(directory, target string) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	f := &fixture{root: ctx}
	proxy, err := proxyFor(target, f)
	if err != nil {
		return err
	}
	defer proxy.Transport.(*http.Transport).CloseIdleConnections()
	servers := []*http.Server{
		{Addr: ":443", Handler: http.HandlerFunc(f.provider), ReadHeaderTimeout: 5 * time.Second, TLSConfig: &tls.Config{MinVersion: tls.VersionTLS12}},
		{Addr: ":8081", Handler: proxy, ReadHeaderTimeout: 5 * time.Second},
		{Addr: ":9911", Handler: http.HandlerFunc(f.stats), ReadHeaderTimeout: 5 * time.Second},
	}
	errorsOut := make(chan error, len(servers))
	for index, server := range servers {
		go func(index int, server *http.Server) {
			if index == 0 {
				errorsOut <- server.ListenAndServeTLS(filepath.Join(directory, "server.pem"), filepath.Join(directory, "server-key.pem"))
			} else {
				errorsOut <- server.ListenAndServe()
			}
		}(index, server)
	}
	select {
	case err = <-errorsOut:
	case <-ctx.Done():
	}
	stop()
	for _, server := range servers {
		_ = server.Close()
	}
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func httpCall(ctx context.Context, client *http.Client, base, method, path, key, payload string) error {
	request, err := http.NewRequestWithContext(ctx, method, base+path, strings.NewReader(payload))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	if key != "" {
		request.Header.Set("Authorization", "Bearer "+key)
	}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, 512<<10))
	if err != nil {
		return err
	}
	var body any
	if json.Unmarshal(raw, &body) != nil {
		body = string(raw)
	}
	return json.NewEncoder(os.Stdout).Encode(map[string]any{"status": response.StatusCode, "body": body})
}

func wsCall(ctx context.Context, client *http.Client, base, key, payload, until string, output io.Writer) error {
	connection, response, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(base, "http")+"/v1/responses", &websocket.DialOptions{HTTPClient: client,
		HTTPHeader: http.Header{"Authorization": {"Bearer " + key}, "Cookie": {"private-client-cookie"}, "X-Forwarded-For": {"192.0.2.1"}}})
	if err != nil {
		if response != nil && response.Body != nil {
			_ = response.Body.Close()
		}
		return errors.New("fixture client WebSocket handshake failed")
	}
	defer connection.CloseNow()
	connection.SetReadLimit(512 << 10)
	if err := connection.Write(ctx, websocket.MessageText, []byte(payload)); err != nil {
		return err
	}
	created := false
	for count := 0; count < 64; count++ {
		_, raw, err := connection.Read(ctx)
		if err != nil {
			if until == "disconnect" && created && ctx.Err() == nil {
				_, err = io.WriteString(output, "{\"disconnected\":true}\n")
				return err
			}
			return err
		}
		var event struct{ Type string }
		if json.Unmarshal(raw, &event) != nil || !json.Valid(raw) {
			return errors.New("invalid public WebSocket event")
		}
		if _, err := io.Copy(output, bytes.NewReader(append(raw, '\n'))); err != nil {
			return err
		}
		created = created || event.Type == "response.created"
		if event.Type == "error" {
			if until == "error" {
				return nil
			}
			return errors.New("unexpected public WebSocket error")
		}
		if strings.HasPrefix(event.Type, "response.") && (event.Type == "response.completed" || event.Type == "response.failed" || event.Type == "response.incomplete") {
			if until != "terminal" {
				return errors.New("unexpected public WebSocket terminal")
			}
			return nil
		}
	}
	return errors.New("fixture client event capacity exhausted")
}

func main() {
	mode := flag.String("mode", "serve", "prepare|serve|http|ws")
	directory := flag.String("directory", "/fixtures", "ephemeral fixture directory")
	target := flag.String("target", "http://gateway:8080", "explicit internal reverse-proxy target")
	base := flag.String("base", "http://127.0.0.1:8081", "client base URL inside the isolated network")
	method := flag.String("method", "GET", "HTTP method")
	path := flag.String("path", "/healthz", "HTTP path")
	key := flag.String("key", "", "synthetic fixture bearer")
	payload := flag.String("payload", "", "synthetic JSON request")
	until := flag.String("until", "terminal", "WS terminal|disconnect|error")
	flag.Parse()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	transport := &http.Transport{Proxy: nil, ResponseHeaderTimeout: 5 * time.Second}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 25 * time.Second}
	var err error
	switch *mode {
	case "prepare":
		err = prepare(*directory)
	case "serve":
		err = serve(*directory, *target)
	case "http":
		err = httpCall(ctx, client, *base, *method, *path, *key, *payload)
	case "ws":
		err = wsCall(ctx, client, *base, *key, *payload, *until, os.Stdout)
	default:
		err = errors.New("unknown fixture mode")
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
