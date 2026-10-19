package responses

import (
	"strings"
	"testing"

	auth "minigateway/internal/foundation/contracts/upstreamauth/v3"
)

func TestResponsesValidatedTerminalPreservesOnlyEligibilityCodeBeforeSanitizing(t *testing.T) {
	for _, tc := range []struct {
		wire            string
		eligible, valid bool
	}{
		{"event: response.failed\ndata: {\"type\":\"response.failed\",\"response\":{\"id\":\"native\",\"object\":\"response\",\"status\":\"failed\",\"output\":[],\"error\":{\"code\":\"subscription_sharing_usage_limit_exceeded\",\"message\":\"secret-token\"}}}\n\n", true, true},
		{"event: error\ndata: {\"type\":\"error\",\"code\":\"subscription_sharing_usage_limit_exceeded\",\"message\":\"secret-token\"}\n\n", true, true},
		{"event: response.failed\ndata: {\"type\":\"response.failed\",\"response\":{\"id\":\"native\",\"object\":\"response\",\"status\":\"completed\",\"output\":[],\"error\":{\"code\":\"subscription_sharing_usage_limit_exceeded\"}}}\n\n", false, false},
		{"event: error\ndata: {\"type\":\"error\",\"code\":\"subscription_sharing_usage_limit_exceeded\"}\n", false, false},
		{"event: error\ndata: {\"type\":\"error\",\"message\":\"subscription_sharing_usage_limit_exceeded\",\"status\":429}\n\n", false, true},
	} {
		d := NewDecoderWithID("public")
		stream := d.NewStream()
		var output strings.Builder
		var failure error
		lines := strings.Split(tc.wire, "\n")
		for _, line := range lines[:len(lines)-1] {
			frames, err := stream.DecodeLine([]byte(line))
			if err != nil {
				failure = err
				break
			}
			for _, frame := range frames {
				output.Write(frame.Data)
			}
		}
		if failure == nil {
			_, failure = stream.End()
		}
		if (failure == nil) != tc.valid {
			t.Fatalf("terminal validation unexpected: %v", failure)
		}
		signal, ok := d.InferenceFailure()
		if ok != tc.eligible {
			t.Fatalf("eligibility=%v want%v", ok, tc.eligible)
		}
		if ok && (signal.Source != auth.ResponsesEvent || signal.Reason != auth.UsageLimited || signal.Scope != auth.Grant || signal.HTTPStatus != 429) {
			t.Fatalf("invalid signal: %+v", signal)
		}
		if strings.Contains(output.String(), "secret-token") || strings.Contains(output.String(), "subscription_sharing_usage_limit_exceeded") {
			t.Fatal("fixed observation leaked into downstream error")
		}
		d.NewStream()
		if _, ok := d.InferenceFailure(); ok {
			t.Fatal("new attempt inherited terminal eligibility")
		}
	}
}
