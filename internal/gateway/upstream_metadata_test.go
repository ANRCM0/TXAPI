package gateway

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"minigateway/internal/core"
)

func TestUpstreamMetadataBoundedSelectionAndCommit(t *testing.T) {
	for _, tc := range []struct {
		name          string
		headers       http.Header
		status, final int
		id, retry     string
	}{
		{"safe", http.Header{"X-Request-Id": {" req-1 "}, "Retry-After": {" 17 "}, "Set-Cookie": {"secret"}, "Authorization": {"Bearer secret"}}, 429, 429, "req-1", "17"},
		{"date", http.Header{"Request-Id": {"req-2"}, "Retry-After": {"Wed, 21 Oct 2026 07:28:00 GMT"}}, 503, 503, "req-2", "Wed, 21 Oct 2026 07:28:00 GMT"},
		{"status-changed", http.Header{"X-Request-Id": {"req-3"}, "Retry-After": {"2"}}, 503, 504, "req-3", ""},
		{"success-no-retry", http.Header{"X-Request-Id": {"req-4"}, "Retry-After": {"2"}}, 200, 200, "req-4", ""},
		{"both-ids", http.Header{"X-Request-Id": {"one"}, "Request-Id": {"two"}}, 200, 200, "", ""},
		{"duplicate-casing", http.Header{"X-Request-Id": {"one"}, "x-request-id": {"two"}, "Retry-After": {"1", "2"}}, 429, 429, "", ""},
		{"hop-by-hop", http.Header{"x-request-id": {"one"}, "Retry-After": {"2"}, "connection": {"x-REQUEST-id, Retry-after"}}, 429, 429, "", ""},
		{"connection-size", http.Header{"X-Request-Id": {"one"}, "Connection": {strings.Repeat("a", 4097)}}, 200, 200, "", ""},
		{"connection-options", http.Header{"X-Request-Id": {"one"}, "Connection": {strings.Repeat("a,", 64) + "a"}}, 200, 200, "", ""},
		{"control-and-overflow", http.Header{"X-Request-Id": {"one\r\nInjected: secret"}, "Retry-After": {"2147483648"}}, 503, 503, "", ""},
		{"oversize", http.Header{"X-Request-Id": {strings.Repeat("a", 129)}, "Retry-After": {strings.Repeat("0", 65)}}, 503, 503, "", ""},
		{"invalid", http.Header{"X-Request-Id": {"one,two"}, "Retry-After": {"-1"}}, 429, 429, "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			target := &observerOutput{}
			output := &executionOutput{target: target}
			output.observeUpstreamHeaders(tc.status, tc.headers)
			if target.commits != 0 {
				t.Fatal("header observation committed output")
			}
			if err := output.Commit(context.Background(), core.ResponseMetadata{StatusCode: tc.final}); err != nil {
				t.Fatal(err)
			}
			values := target.metadata.Values
			if values["X-TXAPI-Upstream-Request-ID"] != tc.id || values["Retry-After"] != tc.retry {
				t.Fatalf("headers=%v want ID=%q retry=%q", values, tc.id, tc.retry)
			}
			for key := range values {
				if key != "X-TXAPI-Upstream-Request-ID" && key != "Retry-After" {
					t.Fatal("unselected header leaked", key)
				}
			}
			output.observeUpstreamHeaders(200, http.Header{"X-Request-Id": {"late"}})
			if output.upstream.requestID != tc.id {
				t.Fatal("committed headers changed")
			}
		})
	}
}

func TestUpstreamMetadataLocalValuesHavePriority(t *testing.T) {
	target := &observerOutput{}
	output := &executionOutput{target: target}
	output.setMetadata("Retry-After", "7")
	output.setMetadata("X-Request-ID", "local")
	output.setMetadata("X-TXAPI-Billing-ID", "bill-local")
	output.observeUpstreamHeaders(429, http.Header{"X-Request-Id": {"upstream"}, "Retry-After": {"100"}, "X-TXAPI-Billing-ID": {"forged"}})
	if err := output.Commit(context.Background(), core.ResponseMetadata{StatusCode: 429}); err != nil {
		t.Fatal(err)
	}
	values := target.metadata.Values
	if values["Retry-After"] != "7" || values["X-Request-ID"] != "local" || values["X-TXAPI-Billing-ID"] != "bill-local" || values["X-TXAPI-Upstream-Request-ID"] != "upstream" {
		t.Fatal(values)
	}
}
