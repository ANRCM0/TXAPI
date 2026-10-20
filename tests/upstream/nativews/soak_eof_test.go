package main

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"testing"
	"time"
)

// Model the gateway's terminal event followed by HTTP-handler cleanup. The
// upstream fixture itself remains open until its client explicitly cancels.
func soakTestGateway(t *testing.T, client *http.Client, target string, tail http.HandlerFunc) http.Handler {
	t.Helper()
	u, _ := url.Parse(target)
	proxy := httputil.NewSingleHostReverseProxy(u)
	proxy.Transport = client.Transport
	original := proxy.Director
	proxy.Director = func(r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = r.Body.Close()
		raw = bytes.ReplaceAll(raw, []byte(`"model":"alias"`), []byte(`"model":"m"`))
		r.Body, r.ContentLength = io.NopCloser(bytes.NewReader(raw)), int64(len(raw))
		r.Header.Set("Authorization", "Bearer "+upstreamKey)
		r.Header["X-Forwarded-For"] = nil
		original(r)
	}
	proxy.ModifyResponse = func(response *http.Response) error {
		body := response.Body
		reader, writer := io.Pipe()
		response.Body = reader
		go func() {
			defer body.Close()
			defer writer.Close()
			scanner := bufio.NewScanner(body)
			for scanner.Scan() {
				line := scanner.Text()
				if _, err := fmt.Fprintln(writer, line); err != nil {
					return
				}
				if line == "data: [DONE]" {
					_, _ = fmt.Fprintln(writer)
					return
				}
			}
		}()
		return nil
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxy.ServeHTTP(w, r)
		tail(w, r)
	})
}

func TestSoakEOFRejectsTrailingExhaustedAndUnclosedBodies(t *testing.T) {
	for _, mode := range []string{"trailing", "limit", "unclosed"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			f := &fixture{root: ctx}
			provider := httptest.NewUnstartedServer(http.HandlerFunc(f.provider))
			provider.EnableHTTP2 = true
			provider.StartTLS()
			defer provider.Close()
			client := provider.Client()
			defer client.CloseIdleConnections()
			edge := httptest.NewUnstartedServer(soakTestGateway(t, client, provider.URL, func(w http.ResponseWriter, r *http.Request) {
				switch mode {
				case "trailing":
					_, _ = io.WriteString(w, "unexpected\n")
				case "limit":
					_, _ = io.WriteString(w, strings.Repeat(" ", 70<<10))
				case "unclosed":
					<-r.Context().Done()
				}
			}))
			edge.EnableHTTP2 = true
			edge.StartTLS()
			defer edge.Close()
			downstream := edge.Client()
			defer downstream.CloseIdleConnections()
			report := f.measureSoak(ctx, downstream, edge.URL, soakInput{Workers: 1, Protocol: 2}, time.Millisecond)
			if report.Started != 1 || report.Completed != 0 || len(report.Errors) == 0 || len(report.Samples) != 1 {
				t.Fatalf("bad HTTP completion accepted or retried: %+v", report)
			}
			sample := report.Samples[0]
			if sample.Error == "" || sample.Attempts != 1 || sample.HTTPBodyComplete || sample.CompleteNS <= 0 || len(sample.EventUpperNS) != 10 {
				t.Fatalf("terminal event or attempt evidence lost: %+v", sample)
			}
		})
	}
}

func TestDirectBaselineCancelsAtTerminalWithoutWaitingForHTTPClosure(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	tag := "h2-baseline-direct-eof"
	run := &baselineRun{records: map[string]*baselineRecord{tag: {drained: make(chan struct{})}}}
	f := &fixture{root: ctx, baseline: run}
	provider := httptest.NewUnstartedServer(http.HandlerFunc(f.provider))
	provider.EnableHTTP2 = true
	provider.StartTLS()
	defer provider.Close()
	client := provider.Client()
	defer client.CloseIdleConnections()
	started := time.Now()
	sample := run.callProtocol(ctx, client, provider.URL, upstreamKey, "m", tag, 2)
	if sample.Error != "" || sample.Attempts != 1 || !sample.Drained || sample.HTTPBodyComplete || time.Since(started) >= time.Second {
		t.Fatalf("direct terminal did not promptly cancel and drain: %+v", sample)
	}
}
