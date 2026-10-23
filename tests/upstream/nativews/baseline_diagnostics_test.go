package main

import (
	"context"
	"crypto/tls"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type baselineFailureTransport struct{ tail error }
type baselineFailureReader struct{ err error }

func (r baselineFailureReader) Read([]byte) (int, error) { return 0, r.err }

func (t baselineFailureTransport) RoundTrip(*http.Request) (*http.Response, error) {
	body := io.MultiReader(strings.NewReader("data: {\"choices\":[{\"delta\":{\"content\":\"one\"},\"finish_reason\":null}]}\n\n"), baselineFailureReader{t.tail})
	return &http.Response{StatusCode: 200, ProtoMajor: 2, Header: make(http.Header), Body: io.NopCloser(body),
		TLS: &tls.ConnectionState{Version: tls.VersionTLS13, NegotiatedProtocol: "h2"}}, nil
}

func TestBaselineFailureObservesReadStateBeforeCleanupCancellation(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{{"eof", io.EOF}, {"unexpected_eof", io.ErrUnexpectedEOF}, {"deadline", context.DeadlineExceeded}} {
		t.Run(tc.name, func(t *testing.T) {
			drained := make(chan struct{})
			close(drained)
			now := time.Now()
			run := &baselineRun{records: map[string]*baselineRecord{"partial": {
				attempts: 1, protocol: 2, tlsVersion: "TLS 1.3", alpn: "h2", drained: drained,
				writes: []baselineWrite{{before: now, after: now}},
			}}}
			sample := run.callProtocolCompletion(context.Background(), &http.Client{Transport: baselineFailureTransport{tc.err}},
				"https://fixture.invalid", "fixture-only", "m", "partial", 2, true)
			if sample.ReadOutcome != tc.name || sample.ContextState != "active" || sample.ReadElapsedNS < 0 ||
				sample.ReceivedEvents != 1 || sample.ProviderEvents != 1 || sample.Error == "" ||
				sample.CompleteNS != 0 || sample.HTTPBodyComplete || !sample.Drained {
				t.Fatalf("partial read was hidden or attributed to cleanup: %+v", sample)
			}
		})
	}
}
