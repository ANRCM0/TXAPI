package openaiauth

import (
	"strings"
	"testing"

	auth "minigateway/internal/foundation/contracts/upstreamauth/v3"
)

func TestInferenceFailureClassificationUsesStatusAndBoundedWhitelistOnly(t *testing.T) {
	for _, source := range []auth.Source{auth.ModelsHTTP, auth.ResponsesHTTP} {
		for _, status := range []int{401, 403} {
			s, ok := ClassifyInferenceFailure(status, []byte(`{"error":{"code":"secret-id-token","message":"subscription_sharing_usage_limit_exceeded"}}`), true, source)
			if !ok || s.Reason != auth.AuthorizationReview || s.Scope != auth.Grant || s.HTTPStatus != status || s.Source != source || s.ProviderCode != "" {
				t.Fatalf("authorization classification: %+v", s)
			}
		}
		for _, tc := range []struct {
			body            string
			complete, usage bool
		}{
			{`{"error":{"code":"subscription_sharing_usage_limit_exceeded","message":"secret"}}`, true, true},
			{`{"error":{"code":"subscription_sharing_usage_limit_exceeded"}}`, false, false},
			{`{"error":{"code":"subscription_sharing_usage_limit_exceeded","code":"other"}}`, true, false},
			{`{"error":{"message":"subscription_sharing_usage_limit_exceeded"}}`, true, false},
			{`{"code":"subscription_sharing_usage_limit_exceeded"}`, true, false},
			{`{"error":{"code":"arbitrary-private-provider-code"}}`, true, false},
			{`{"error":{"code":"subscription_sharing_usage_limit_exceeded"},"padding":"` + strings.Repeat("x", 64<<10) + `"}`, true, false},
		} {
			s, ok := ClassifyInferenceFailure(429, []byte(tc.body), tc.complete, source)
			if !ok {
				t.Fatal("429 missing fixed model classification")
			}
			if tc.usage {
				if s.Reason != auth.UsageLimited || s.Scope != auth.Grant || s.ProviderCode != sharingUsageLimit {
					t.Fatalf("usage classification: %+v", s)
				}
			} else if s.Reason != auth.RateLimitUnknown || s.Scope != auth.Model || s.ProviderCode != "" {
				t.Fatalf("unknown rate classification: %+v", s)
			}
		}
		for _, status := range []int{200, 400, 404, 408, 422, 500, 503} {
			if _, ok := ClassifyInferenceFailure(status, []byte(`{"error":{"code":"subscription_sharing_usage_limit_exceeded"}}`), true, source); ok {
				t.Fatalf("status %d incorrectly penalized", status)
			}
		}
	}
}

func TestInferenceEventClassificationRequiresExactTerminalCode(t *testing.T) {
	for _, body := range []string{
		`{"type":"response.failed","response":{"status":"failed","error":{"code":"subscription_sharing_usage_limit_exceeded"}}}`,
		`{"type":"error","code":"subscription_sharing_usage_limit_exceeded"}`,
	} {
		s, ok := ClassifyInferenceFailure(0, []byte(body), true, auth.ResponsesEvent)
		if !ok || s.HTTPStatus != 429 || s.Reason != auth.UsageLimited || s.Scope != auth.Grant || s.ProviderCode != sharingUsageLimit {
			t.Fatalf("event classification: %+v", s)
		}
		if _, ok := ClassifyInferenceFailure(0, []byte(body), false, auth.ResponsesEvent); ok {
			t.Fatal("incomplete event classified")
		}
	}
	for _, body := range []string{
		`{"type":"error","message":"subscription_sharing_usage_limit_exceeded","status":429}`,
		`{"type":"error","code":"rate_limit_exceeded","status":429}`,
		`{"type":"response.completed","response":{"status":"completed","error":{"code":"subscription_sharing_usage_limit_exceeded"}}}`,
		`{"type":"response.failed","response":{"status":"completed","error":{"code":"subscription_sharing_usage_limit_exceeded"}}}`,
		`{"type":"response.output_text.delta","code":"subscription_sharing_usage_limit_exceeded"}`,
	} {
		if _, ok := ClassifyInferenceFailure(429, []byte(body), true, auth.ResponsesEvent); ok {
			t.Fatal("event inferred from an untrusted alias or message")
		}
	}
}
