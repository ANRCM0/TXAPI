package main

import (
	"context"
	"crypto/tls"
	"fmt"
	"net/http"
	"net/http/httptest"
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
			edge := httptest.NewUnstartedServer(soakTestGateway(t, upstream, provider.URL, func(w http.ResponseWriter, r *http.Request) {
				time.Sleep(200 * time.Millisecond)
			}))
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
				if report.ElapsedNS-sample.CompleteNS < int64(150*time.Millisecond) {
					t.Fatal("worker returned before delayed HTTP handler cleanup")
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
