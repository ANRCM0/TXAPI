package main

import (
	"bytes"
	"context"
	"crypto/tls"
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

func TestSoakRetainsEveryConcurrentResultAndNegotiatesEdgeProtocols(t *testing.T) {
	for _, protocol := range []int{1, 2} {
		t.Run(fmt.Sprint(protocol), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			f := &fixture{root: ctx}
			provider := httptest.NewUnstartedServer(http.HandlerFunc(f.provider))
			provider.EnableHTTP2 = true
			provider.StartTLS()
			t.Cleanup(provider.Close)
			t.Cleanup(cancel)
			upstream := provider.Client()
			t.Cleanup(upstream.CloseIdleConnections)
			u, _ := url.Parse(provider.URL)
			proxy := httputil.NewSingleHostReverseProxy(u)
			proxy.Transport = upstream.Transport
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
			edge := httptest.NewUnstartedServer(proxy)
			edge.EnableHTTP2 = true
			edge.StartTLS()
			t.Cleanup(edge.Close)
			client := edge.Client()
			if protocol == 1 {
				transport := client.Transport.(*http.Transport).Clone()
				transport.ForceAttemptHTTP2 = false
				transport.TLSNextProto = map[string]func(string, *tls.Conn) http.RoundTripper{}
				transport.TLSClientConfig.NextProtos = []string{"http/1.1"}
				client = &http.Client{Transport: transport}
			}
			t.Cleanup(client.CloseIdleConnections)
			report := f.measureSoak(ctx, client, edge.URL, soakInput{Workers: 2, Protocol: protocol}, 200*time.Millisecond)
			if len(report.Errors) != 0 || report.Started != 2 || report.Completed != 2 || len(report.Samples) != 2 || report.ActiveAfter != 0 {
				t.Fatalf("soak: %+v", report)
			}
			for _, sample := range report.Samples {
				if sample.Protocol != protocol || sample.ProviderProtocol != 2 || sample.Attempts != 1 || !sample.Drained || len(sample.EventUpperNS) != 10 {
					t.Fatalf("sample: %+v", sample)
				}
			}
			if f.baseline != nil || len(f.chat) != 0 || len(f.requests) != 0 || len(f.errors) != 0 {
				t.Fatal("soak retained active state or polluted normal observations")
			}
		})
	}
}

func TestSoakRetainsFailureAndStopsScheduling(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	f := &fixture{root: ctx}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "fixture failure", 503)
	}))
	defer server.Close()
	report := f.measureSoak(ctx, server.Client(), server.URL, soakInput{Workers: 1, Protocol: 1}, time.Second)
	if report.Started != 1 || report.Completed != 0 || len(report.Samples) != 1 || len(report.Errors) == 0 || report.Samples[0].Status != 503 {
		t.Fatalf("failure hidden: %+v", report)
	}
}

func TestSoakEndpointRejectsUnboundedDuplicateAndConcurrentRuns(t *testing.T) {
	directory := t.TempDir()
	if err := prepare(directory); err != nil {
		t.Fatal(err)
	}
	f := &fixture{}
	handler, err := f.soakHandler(directory)
	if err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{`{}`, `null`, `{"seconds":4,"workers":1,"protocol":2}`, `{"seconds":1801,"workers":1,"protocol":2}`,
		`{"seconds":5,"workers":3,"protocol":2}`, `{"seconds":5,"workers":1,"protocol":3}`,
		`{"seconds":5,"workers":1,"protocol":2,"extra":true}`, `{"seconds":5,"workers":1,"protocol":2} {}`,
		`{"seconds":5,"workers":1,"protocol":2}` + strings.Repeat(" ", 1024)} {
		w := httptest.NewRecorder()
		handler(w, httptest.NewRequest("POST", "/soak", strings.NewReader(body)))
		if w.Code != 400 || f.soakProfiles.Load() != 0 || f.soakActive.Load() {
			t.Fatal("invalid run accepted", body, w.Code)
		}
	}
	for _, active := range []bool{false, true} {
		f.soakActive.Store(active)
		f.soakProfiles.Store(1 << 2)
		w := httptest.NewRecorder()
		handler(w, httptest.NewRequest("POST", "/soak", strings.NewReader(`{"seconds":5,"workers":1,"protocol":2}`)))
		if w.Code != 409 {
			t.Fatal("duplicate/concurrent run accepted", w.Code)
		}
	}
}
