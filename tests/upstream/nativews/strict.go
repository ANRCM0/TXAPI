package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/coder/websocket"
)

// This provider is intentionally separate from the disabled native fixture.
// Its transcript is observation only; the production SQLite ledger is authority.
type strictFixture struct {
	mu          sync.Mutex
	rows        []map[string]any
	jobs        map[string]map[string]any
	connections int
}

func strictInput(body map[string]any) string {
	if text, ok := body["input"].(string); ok {
		return text
	}
	items, _ := body["input"].([]any)
	if len(items) == 0 {
		return ""
	}
	last, _ := items[len(items)-1].(map[string]any)
	if text, ok := last["content"].(string); ok {
		return text
	}
	parts, _ := last["content"].([]any)
	if len(parts) == 0 {
		return ""
	}
	part, _ := parts[len(parts)-1].(map[string]any)
	text, _ := part["text"].(string)
	return text
}

func strictDocument(id, input string, background bool) map[string]any {
	d := document(id, textOutput(id))
	if strings.Contains(input, "missing") || strings.Contains(input, "block") || strings.Contains(input, "cancel") {
		delete(d, "usage")
	}
	if strings.Contains(input, "zero") {
		d["usage"] = map[string]any{"input_tokens": 0, "output_tokens": 0, "total_tokens": 0}
	}
	if background {
		d["status"], d["output"] = "queued", []any{}
		delete(d, "usage")
	}
	return d
}

func (f *strictFixture) record(method, id, input, transport string) {
	f.rows = append(f.rows, map[string]any{"method": method, "id": id, "input": input, "transport": transport})
}

func (f *strictFixture) stats(w http.ResponseWriter, _ *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	_ = json.NewEncoder(w).Encode(map[string]any{"requests": f.rows})
}

func (f *strictFixture) provider(w http.ResponseWriter, r *http.Request) {
	if !strings.HasPrefix(r.URL.Path, "/v1/responses") || r.Header.Get("Authorization") != "Bearer "+upstreamKey ||
		r.Header.Get("Cookie") != "" || r.Header.Get("X-Forwarded-For") != "" {
		http.Error(w, "strict fixture boundary", 403)
		return
	}
	if r.Method == http.MethodGet && r.URL.Path == "/v1/responses" {
		f.ws(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if r.URL.Path != "/v1/responses" {
		id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/v1/responses/"), "/cancel")
		f.mu.Lock()
		job := f.jobs[id]
		if len(f.rows) >= 256 {
			f.mu.Unlock()
			http.Error(w, "fixture capacity", 503)
			return
		}
		if job == nil {
			f.mu.Unlock()
			http.NotFound(w, r)
			return
		}
		input := job["input"].(string)
		f.record(r.Method, id, input, "http-control")
		d := strictDocument(id, input, false)
		if strings.HasSuffix(r.URL.Path, "/cancel") || job["cancelled"] == true {
			job["cancelled"] = true
			d["status"], d["output"] = "cancelled", []any{}
			delete(d, "usage")
		} else if strings.Contains(input, "block") || strings.Contains(input, "cancel") {
			d["status"], d["output"] = "in_progress", []any{}
			delete(d, "usage")
		}
		f.mu.Unlock()
		_ = json.NewEncoder(w).Encode(d)
		return
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, 4097))
	var body map[string]any
	if err != nil || len(raw) > 4096 || json.Unmarshal(raw, &body) != nil || body["model"] != "m" {
		http.Error(w, "strict fixture body", 400)
		return
	}
	input := strictInput(body)
	background := body["background"] == true
	f.mu.Lock()
	if len(f.rows) >= 256 {
		f.mu.Unlock()
		http.Error(w, "fixture capacity", 503)
		return
	}
	id := fmt.Sprintf("strict_%d", len(f.rows)+1)
	f.record(r.Method, id, input, "http")
	if background {
		f.jobs[id] = map[string]any{"input": input}
	}
	f.mu.Unlock()
	if strings.Contains(input, "block") && !background {
		<-r.Context().Done()
		return
	}
	d := strictDocument(id, input, background)
	if body["stream"] == true {
		w.Header().Set("Content-Type", "text/event-stream")
		raw, _ := json.Marshal(map[string]any{"type": "response.completed", "sequence_number": 0, "response": d})
		_, _ = fmt.Fprintf(w, "event: response.completed\ndata: %s\n\n", raw)
		return
	}
	_ = json.NewEncoder(w).Encode(d)
}

func (f *strictFixture) ws(w http.ResponseWriter, r *http.Request) {
	c, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer c.CloseNow()
	f.mu.Lock()
	f.connections++
	connectionID := f.connections
	f.mu.Unlock()
	c.SetReadLimit(4096)
	ctx, cancel := context.WithTimeout(r.Context(), 40*time.Second)
	defer cancel()
	for round := 0; round < 4; round++ {
		_, raw, err := c.Read(ctx)
		if err != nil {
			return
		}
		var body map[string]any
		if json.Unmarshal(raw, &body) != nil || body["model"] != "m" {
			return
		}
		input := strictInput(body)
		f.mu.Lock()
		if len(f.rows) >= 256 {
			f.mu.Unlock()
			return
		}
		id := fmt.Sprintf("strict_%d", len(f.rows)+1)
		f.record("CREATE", id, input, "ws")
		f.rows[len(f.rows)-1]["connection"] = connectionID
		f.mu.Unlock()
		d := strictDocument(id, input, false)
		created := strictDocument(id, input, true)
		created["status"] = "in_progress"
		for _, event := range []map[string]any{{"type": "response.created", "sequence_number": 0, "response": created},
			{"type": "response.completed", "sequence_number": 1, "response": d}} {
			raw, _ = json.Marshal(event)
			if c.Write(ctx, websocket.MessageText, raw) != nil {
				return
			}
		}
	}
}

func strictServe(directory, target string) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	f := &strictFixture{jobs: make(map[string]map[string]any)}
	proxy, err := proxyFor(target, &fixture{root: ctx})
	if err != nil {
		return err
	}
	defer proxy.Transport.(*http.Transport).CloseIdleConnections()
	stats := http.NewServeMux()
	stats.HandleFunc("/stats", f.stats)
	servers := []*http.Server{{Addr: ":443", Handler: http.HandlerFunc(f.provider), ReadHeaderTimeout: 5 * time.Second, TLSConfig: &tls.Config{MinVersion: tls.VersionTLS12}},
		{Addr: ":8081", Handler: proxy, ReadHeaderTimeout: 5 * time.Second}, {Addr: ":9911", Handler: stats, ReadHeaderTimeout: 5 * time.Second}}
	out := make(chan error, len(servers))
	for index, server := range servers {
		go func(index int, server *http.Server) {
			if index == 0 {
				out <- server.ListenAndServeTLS(filepath.Join(directory, "server.pem"), filepath.Join(directory, "server-key.pem"))
			} else {
				out <- server.ListenAndServe()
			}
		}(index, server)
	}
	select {
	case err = <-out:
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

func strictWSCall(ctx context.Context, client *http.Client, base, key, payload string, output io.Writer) error {
	var rounds []json.RawMessage
	if json.Unmarshal([]byte(payload), &rounds) != nil || len(rounds) < 1 || len(rounds) > 3 {
		return errors.New("strict client requires one to three rounds")
	}
	c, response, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(base, "http")+"/v1/responses", &websocket.DialOptions{HTTPClient: client, HTTPHeader: http.Header{"Authorization": {"Bearer " + key}}})
	if response != nil && response.Body != nil {
		defer response.Body.Close()
	}
	if err != nil {
		return err
	}
	defer c.CloseNow()
	c.SetReadLimit(512 << 10)
	for index, round := range rounds {
		if err = c.Write(ctx, websocket.MessageText, round); err != nil {
			return err
		}
		terminal := false
		for count := 0; count < 16; count++ {
			_, raw, err := c.Read(ctx)
			if err != nil {
				return err
			}
			var event map[string]any
			if json.Unmarshal(raw, &event) != nil {
				return errors.New("invalid strict WS event")
			}
			if err = json.NewEncoder(output).Encode(map[string]any{"round": index + 1, "event": event}); err != nil {
				return err
			}
			if event["type"] == "error" {
				return errors.New("strict WS public error")
			}
			if event["type"] == "response.completed" {
				terminal = true
				break
			}
		}
		if !terminal {
			return errors.New("strict WS event bound exceeded")
		}
	}
	return nil
}
