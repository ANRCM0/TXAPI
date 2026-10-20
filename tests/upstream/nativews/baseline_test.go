package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestDualTLSBaselinePairsUseCompleteEventsAndDrain(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	f := &fixture{root: ctx}
	provider := httptest.NewUnstartedServer(http.HandlerFunc(f.provider))
	provider.EnableHTTP2 = true
	provider.StartTLS()
	t.Cleanup(provider.Close)
	t.Cleanup(cancel)
	direct := provider.Client()
	t.Cleanup(direct.CloseIdleConnections)
	u, _ := url.Parse(provider.URL)
	proxy := httputil.NewSingleHostReverseProxy(u)
	proxy.Transport = direct.Transport
	original := proxy.Director
	proxy.Director = func(r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+baselineClientKey {
			t.Error("gateway fixture client auth")
		}
		raw, _ := io.ReadAll(r.Body)
		_ = r.Body.Close()
		raw = bytes.ReplaceAll(raw, []byte(`"model":"alias"`), []byte(`"model":"m"`))
		r.Body = io.NopCloser(bytes.NewReader(raw))
		r.ContentLength = int64(len(raw))
		r.Header.Set("Authorization", "Bearer "+upstreamKey)
		r.Header["X-Forwarded-For"] = nil
		original(r)
	}
	edge := httptest.NewUnstartedServer(proxy)
	edge.EnableHTTP2 = true
	edge.StartTLS()
	t.Cleanup(edge.Close)
	client := edge.Client()
	t.Cleanup(client.CloseIdleConnections)
	report := f.measureBaseline(ctx, direct, client, provider.URL, edge.URL, 3)
	if report.Planned != 6 || report.Started != 6 || report.Completed != 6 || report.Pairs != 3 || len(report.Errors) != 0 {
		t.Fatalf("report: %+v", report)
	}
	for index, row := range report.Samples {
		want := "direct"
		if (index/2+index%2)%2 == 1 {
			want = "gateway"
		}
		if row.Target != want || row.Protocol != 2 || row.ProviderProtocol != 2 || row.Attempts != 1 || !row.Drained || row.Error != "" {
			t.Fatalf("sample %+v", row)
		}
		if row.FirstBodyNS < 0 || row.FirstTextNS < row.FirstBodyNS || row.CompleteNS < row.FirstTextNS || len(row.EventLowerNS) != 10 {
			t.Fatalf("timing %+v", row)
		}
		for event, lower := range row.EventLowerNS {
			if lower > row.EventUpperNS[event] {
				t.Fatal("inverted observation interval")
			}
		}
	}
	if len(f.chat) != 0 || len(f.requests) != 0 || len(f.errors) != 0 {
		t.Fatal("baseline polluted original observations")
	}
}

func TestBaselineRejectsIncompleteFinishAndDeadline(t *testing.T) {
	for _, mode := range []string{"truncated", "invalid-finish", "invalid-usage", "deadline"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()
			tag := "h2-baseline-0-direct"
			record := &baselineRecord{drained: make(chan struct{})}
			run := &baselineRun{records: map[string]*baselineRecord{tag: record}}
			server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				run.mu.Lock()
				record.attempts = 1
				record.protocol = 2
				record.alpn = "h2"
				run.mu.Unlock()
				defer close(record.drained)
				w.Header().Set("Content-Type", "text/event-stream")
				text, _ := json.Marshal(strings.Join(baselinePieces, ""))
				_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":"+string(text)+"}}]}\n\n")
				_ = http.NewResponseController(w).Flush()
				if mode == "deadline" {
					<-r.Context().Done()
					return
				}
				if mode == "invalid-finish" {
					_, _ = io.WriteString(w, "data: [DONE]\n\n")
				}
				if mode == "invalid-usage" {
					_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":2,\"completion_tokens\":8,\"total_tokens\":999}}\n\ndata: [DONE]\n\n")
				}
			}))
			server.EnableHTTP2 = true
			server.StartTLS()
			t.Cleanup(server.Close)
			t.Cleanup(cancel)
			client := server.Client()
			t.Cleanup(client.CloseIdleConnections)
			started := time.Now()
			row := run.call(ctx, client, server.URL, upstreamKey, "m", tag)
			if !strings.Contains(row.Error, "incomplete or invalid stream") || !row.Drained || time.Since(started) > 2*time.Second {
				t.Fatalf("invalid stream accepted/not drained %+v", row)
			}
		})
	}
}

func TestBaselineAdmissionAndBoundedInputs(t *testing.T) {
	f := &fixture{}
	if report := f.measureBaseline(context.Background(), nil, nil, "", "", 21); len(report.Errors) != 1 || f.baseline != nil {
		t.Fatal("pair capacity bypass")
	}
	directory := t.TempDir()
	if err := prepare(directory); err != nil {
		t.Fatal(err)
	}
	handler, err := f.baselineHandler(directory)
	if err != nil {
		t.Fatal(err)
	}
	f.baselineStarted.Store(true)
	for _, test := range []struct {
		method, body string
		status       int
	}{{"GET", "{}", 400}, {"POST", `{"pairs":100}`, 400}, {"POST", "{} {}", 400}, {"POST", "null", 400}, {"POST", "{}" + strings.Repeat(" ", 1024), 400}, {"POST", "{}", 409}} {
		w := httptest.NewRecorder()
		handler(w, httptest.NewRequest(test.method, "/baseline", strings.NewReader(test.body)))
		if w.Code != test.status {
			t.Fatal(w.Code, test)
		}
	}
	run := &baselineRun{records: map[string]*baselineRecord{"h2-baseline-0-direct": {attempts: 1, drained: make(chan struct{})}}}
	for _, tag := range []string{"h2-baseline-0-direct", "h2-baseline-999-direct"} {
		w := httptest.NewRecorder()
		run.stream(w, httptest.NewRequest("POST", "/v1/chat/completions", nil), tag)
		if w.Code != 409 {
			t.Fatal(w.Code)
		}
	}
	if run.records["h2-baseline-0-direct"].attempts != 2 || len(run.records) != 1 {
		t.Fatal("duplicate not counted or unknown tag allocated")
	}
	if _, err := json.Marshal(baselineReport{}); err != nil {
		t.Fatal(err)
	}
}
