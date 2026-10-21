package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const baselineClientKey = "nativews-deployment-client-fixture-only"

var baselinePieces = []string{"one", "\u96ea", "three", "\U0001f30f", "five", "six", "seven", "eight"}

type baselineWrite struct{ before, after time.Time }

type baselineRecord struct {
	attempts               int
	protocol               int
	peer, tlsVersion, alpn string
	writes                 []baselineWrite
	drained                chan struct{}
}

type baselineRun struct {
	mu      sync.Mutex
	records map[string]*baselineRecord
	delay   time.Duration
}

type baselineSample struct {
	Pair             int     `json:"pair"`
	Target           string  `json:"target"`
	Error            string  `json:"error,omitempty"`
	Status           int     `json:"status"`
	UsageID          string  `json:"usage_id,omitempty"`
	Protocol         int     `json:"protocol"`
	TLS              string  `json:"tls"`
	ALPN             string  `json:"alpn"`
	FirstBodyNS      int64   `json:"first_body_ns"`
	FirstTextNS      int64   `json:"first_text_ns"`
	CompleteNS       int64   `json:"complete_ns"`
	Attempts         int     `json:"attempts"`
	ProviderProtocol int     `json:"provider_protocol"`
	ProviderPeer     string  `json:"provider_peer"`
	ProviderTLS      string  `json:"provider_tls"`
	ProviderALPN     string  `json:"provider_alpn"`
	EventLowerNS     []int64 `json:"event_lower_ns"`
	EventUpperNS     []int64 `json:"event_upper_ns"`
	Drained          bool    `json:"drained"`
	HTTPBodyComplete bool    `json:"http_body_complete"`
}

type baselineReport struct {
	Planned           int              `json:"planned"`
	Started           int              `json:"started"`
	Completed         int              `json:"completed"`
	Pairs             int              `json:"paired_success_count"`
	Samples           []baselineSample `json:"samples"`
	PairedFirstBodyNS []int64          `json:"paired_first_body_ns"`
	PairedFirstTextNS []int64          `json:"paired_first_text_ns"`
	PairedCompleteNS  []int64          `json:"paired_complete_ns"`
	Errors            []string         `json:"errors"`
	TimingScope       string           `json:"timing_scope"`
}

func (f *fixture) baselineHandler(directory string) (http.HandlerFunc, error) {
	ca, err := os.ReadFile(filepath.Join(directory, "ca.pem"))
	if err != nil {
		return nil, err
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(ca) {
		return nil, errors.New("baseline CA unavailable")
	}
	return func(w http.ResponseWriter, r *http.Request) {
		var body map[string]json.RawMessage
		raw, readErr := io.ReadAll(io.LimitReader(r.Body, 1025))
		if r.Method != http.MethodPost || readErr != nil || len(raw) > 1024 || json.Unmarshal(raw, &body) != nil || body == nil || len(body) != 0 {
			http.Error(w, "POST empty JSON required", 400)
			return
		}
		if !f.baselineStarted.CompareAndSwap(false, true) {
			http.Error(w, "one bounded baseline per fixture", 409)
			return
		}
		newClient := func() *http.Client {
			return &http.Client{Transport: &http.Transport{Proxy: nil, ForceAttemptHTTP2: true,
				TLSClientConfig:     &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12},
				TLSHandshakeTimeout: 3 * time.Second, ResponseHeaderTimeout: 3 * time.Second, MaxConnsPerHost: 1}}
		}
		direct, gateway := newClient(), newClient()
		defer direct.CloseIdleConnections()
		defer gateway.CloseIdleConnections()
		ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
		defer cancel()
		w.Header().Set("Content-Type", "application/json")
		if http.NewResponseController(w).Flush() != nil {
			return
		}
		report := f.measureBaseline(ctx, direct, gateway, "https://api.openai.com", "https://127.0.0.1:8443", 20)
		_ = json.NewEncoder(w).Encode(report)
	}, nil
}

func (run *baselineRun) stream(w http.ResponseWriter, r *http.Request, tag string) {
	run.mu.Lock()
	record := run.records[tag]
	if record == nil {
		run.mu.Unlock()
		http.Error(w, "unknown baseline input", 409)
		return
	}
	record.attempts++
	if record.attempts != 1 {
		run.mu.Unlock()
		http.Error(w, "duplicate baseline input", 409)
		return
	}
	record.protocol, record.peer = r.ProtoMajor, r.RemoteAddr
	if r.TLS != nil {
		record.tlsVersion, record.alpn = tls.VersionName(r.TLS.Version), r.TLS.NegotiatedProtocol
	}
	run.mu.Unlock()
	defer close(record.drained)
	w.Header().Set("Content-Type", "text/event-stream")
	write := func(data string) bool {
		before := time.Now()
		_, err := fmt.Fprintf(w, "data: %s\n\n", data)
		if err != nil || http.NewResponseController(w).Flush() != nil {
			return false
		}
		after := time.Now()
		run.mu.Lock()
		record.writes = append(record.writes, baselineWrite{before, after})
		run.mu.Unlock()
		return true
	}
	for _, piece := range baselinePieces {
		raw, _ := json.Marshal(map[string]any{"id": "chat_baseline", "object": "chat.completion.chunk", "model": "m",
			"choices": []any{map[string]any{"index": 0, "delta": map[string]string{"content": piece}, "finish_reason": nil}}})
		if !write(string(raw)) {
			return
		}
		delay := run.delay
		if delay == 0 {
			delay = 5 * time.Millisecond
		}
		timer := time.NewTimer(delay)
		select {
		case <-r.Context().Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
	if !write(`{"id":"chat_baseline","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":8,"total_tokens":10}}`) || !write("[DONE]") {
		return
	}
	<-r.Context().Done()
}

type baselineReader struct {
	io.Reader
	first time.Time
}

func (r *baselineReader) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	if n > 0 && r.first.IsZero() {
		r.first = time.Now()
	}
	return n, err
}

func (f *fixture) measureBaseline(ctx context.Context, direct, gateway *http.Client, directBase, gatewayBase string, pairs int) baselineReport {
	report := baselineReport{Planned: pairs * 2, TimingScope: "same-process monotonic clocks; event interval [received-afterFlush, received-beforeWrite]; cold first pair retained; sequential alternating targets; gateway includes auth/admission/SQLite/usage and TLS reverse proxy"}
	if pairs < 1 || pairs > 20 {
		report.Errors = []string{"baseline pair limit 1..20"}
		return report
	}
	run := &baselineRun{records: make(map[string]*baselineRecord, pairs*2)}
	for pair := 0; pair < pairs; pair++ {
		for _, target := range []string{"direct", "gateway"} {
			run.records[fmt.Sprintf("h2-baseline-%d-%s", pair, target)] = &baselineRecord{drained: make(chan struct{})}
		}
	}
	f.mu.Lock()
	f.baseline = run
	f.mu.Unlock()
	for pair := 0; pair < pairs; pair++ {
		var samples [2]baselineSample
		for order := 0; order < 2; order++ {
			target := (order + pair) % 2
			name, base, model, key, client := "direct", directBase, "m", upstreamKey, direct
			if target == 1 {
				name, base, model, key, client = "gateway", gatewayBase, "alias", baselineClientKey, gateway
			}
			tag := fmt.Sprintf("h2-baseline-%d-%s", pair, name)
			sample := run.call(ctx, client, base, key, model, tag)
			sample.Pair, sample.Target = pair, name
			report.Started++
			if sample.Error == "" {
				report.Completed++
			} else {
				report.Errors = append(report.Errors, fmt.Sprintf("pair %d %s: %s", pair, name, sample.Error))
			}
			samples[target] = sample
			report.Samples = append(report.Samples, sample)
		}
		if samples[0].Error == "" && samples[1].Error == "" {
			report.Pairs++
			report.PairedFirstBodyNS = append(report.PairedFirstBodyNS, samples[1].FirstBodyNS-samples[0].FirstBodyNS)
			report.PairedFirstTextNS = append(report.PairedFirstTextNS, samples[1].FirstTextNS-samples[0].FirstTextNS)
			report.PairedCompleteNS = append(report.PairedCompleteNS, samples[1].CompleteNS-samples[0].CompleteNS)
		}
		if ctx.Err() != nil {
			break
		}
	}
	return report
}

func (run *baselineRun) call(ctx context.Context, client *http.Client, base, key, model, tag string) baselineSample {
	return run.callProtocol(ctx, client, base, key, model, tag, 2)
}

func (run *baselineRun) callProtocol(ctx context.Context, client *http.Client, base, key, model, tag string, protocol int) (sample baselineSample) {
	return run.callProtocolCompletion(ctx, client, base, key, model, tag, protocol, false)
}

func (run *baselineRun) callProtocolCompletion(ctx context.Context, client *http.Client, base, key, model, tag string, protocol int, waitEOF bool) (sample baselineSample) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	start := time.Now()
	payload, _ := json.Marshal(map[string]any{"model": model, "stream": true, "messages": []any{map[string]string{"role": "user", "content": tag}}})
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/v1/chat/completions", bytes.NewReader(payload))
	if err != nil {
		sample.Error = "request construction"
		return
	}
	request.Header.Set("Authorization", "Bearer "+key)
	request.Header.Set("Content-Type", "application/json")
	received := []time.Time{}
	defer func() {
		cancel()
		run.mu.Lock()
		record := run.records[tag]
		attempts := record.attempts
		run.mu.Unlock()
		if attempts != 0 {
			timer := time.NewTimer(time.Second)
			select {
			case <-record.drained:
				sample.Drained = true
			case <-timer.C:
			}
			timer.Stop()
		}
		run.mu.Lock()
		defer run.mu.Unlock()
		sample.Attempts, sample.ProviderProtocol, sample.ProviderPeer = record.attempts, record.protocol, record.peer
		sample.ProviderTLS, sample.ProviderALPN = record.tlsVersion, record.alpn
		if sample.Attempts != 1 || !sample.Drained {
			sample.Error += " upstream attempt/drain mismatch"
		}
		if sample.ProviderProtocol != 2 || sample.ProviderALPN != "h2" {
			sample.Error += " upstream HTTP2 required"
		}
		if len(received) != len(record.writes) || len(received) != 10 {
			sample.Error += " event observation mismatch"
			return
		}
		for index, arrival := range received {
			sample.EventLowerNS = append(sample.EventLowerNS, arrival.Sub(record.writes[index].after).Nanoseconds())
			sample.EventUpperNS = append(sample.EventUpperNS, arrival.Sub(record.writes[index].before).Nanoseconds())
		}
	}()
	response, err := client.Do(request)
	if err != nil {
		sample.Error = "request failed"
		return
	}
	defer response.Body.Close()
	sample.Status, sample.Protocol = response.StatusCode, response.ProtoMajor
	sample.UsageID = response.Header.Get("X-TXAPI-Usage-ID")
	if response.TLS != nil {
		sample.TLS, sample.ALPN = tls.VersionName(response.TLS.Version), response.TLS.NegotiatedProtocol
	}
	if response.StatusCode != 200 || response.ProtoMajor != protocol || protocol == 2 && sample.ALPN != "h2" {
		sample.Error = "status or verified HTTP2 mismatch"
		return
	}
	limited := &io.LimitedReader{R: response.Body, N: 64 << 10}
	reader := &baselineReader{Reader: limited}
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 4096), 32<<10)
	var data, text string
	finished, done := false, false
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "data: ") {
			if data != "" {
				sample.Error = "duplicate data line"
				break
			}
			data = strings.TrimPrefix(line, "data: ")
			continue
		}
		if line != "" {
			sample.Error = "unexpected SSE line"
			break
		}
		if data == "" {
			continue
		}
		arrival := time.Now()
		received = append(received, arrival)
		if data == "[DONE]" {
			done = finished
			sample.CompleteNS = arrival.Sub(start).Nanoseconds()
			// A soak worker reuses an admission slot only after the gateway
			// handler finishes. Direct fixtures intentionally stay open at DONE.
			if waitEOF && (scanner.Scan() || scanner.Err() != nil || limited.N == 0) {
				sample.Error = "trailing stream data or incomplete HTTP body"
				return
			}
			sample.HTTPBodyComplete = waitEOF
			break
		}
		var event struct {
			Choices []struct {
				Delta        struct{ Content string }
				FinishReason *string `json:"finish_reason"`
			}
			Usage *struct {
				Prompt     int `json:"prompt_tokens"`
				Completion int `json:"completion_tokens"`
				Total      int `json:"total_tokens"`
			}
		}
		if json.Unmarshal([]byte(data), &event) != nil || len(event.Choices) != 1 || finished {
			sample.Error = "invalid Chat event"
			break
		}
		choice := event.Choices[0]
		if choice.Delta.Content != "" {
			if text == "" {
				sample.FirstTextNS = arrival.Sub(start).Nanoseconds()
			}
			text += choice.Delta.Content
		}
		if choice.FinishReason != nil {
			finished = *choice.FinishReason == "stop" && event.Usage != nil && event.Usage.Prompt == 2 && event.Usage.Completion == 8 && event.Usage.Total == 10
		}
		data = ""
		if len(received) >= 10 {
			sample.Error = "event capacity"
			break
		}
	}
	if !reader.first.IsZero() {
		sample.FirstBodyNS = reader.first.Sub(start).Nanoseconds()
	}
	if scanner.Err() != nil || !done || text != strings.Join(baselinePieces, "") {
		sample.Error += " incomplete or invalid stream"
	}
	return
}
