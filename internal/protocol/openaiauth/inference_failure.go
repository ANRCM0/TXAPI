package openaiauth

import (
	"encoding/json"
	"net/http"

	auth "minigateway/internal/foundation/contracts/upstreamauth/v3"
	"minigateway/internal/protocol/jsonutil"
)

const sharingUsageLimit = "subscription_sharing_usage_limit_exceeded"

// ClassifyInferenceFailure accepts facts from the fixed SIWC wire only. Error
// messages never identify a reason, scope, code, or reset time.
func ClassifyInferenceFailure(status int, body []byte, complete bool, source auth.Source) (auth.FailureSignal, bool) {
	signal := auth.FailureSignal{HTTPStatus: status, Source: source}
	if source != auth.ModelsHTTP && source != auth.ResponsesHTTP && source != auth.ResponsesEvent {
		return auth.FailureSignal{}, false
	}
	code := ""
	maximum := 64 << 10
	if source == auth.ResponsesEvent {
		maximum = 1 << 20
	}
	if complete && len(body) <= maximum && jsonutil.Validate(body) == nil {
		var envelope struct {
			Type  string `json:"type"`
			Code  string `json:"code"`
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
			Response struct {
				Status string `json:"status"`
				Error  struct {
					Code string `json:"code"`
				} `json:"error"`
			} `json:"response"`
		}
		if json.Unmarshal(body, &envelope) == nil {
			if source == auth.ResponsesEvent {
				switch {
				case envelope.Type == "response.failed" && envelope.Response.Status == "failed":
					code = envelope.Response.Error.Code
				case envelope.Type == "error":
					code = envelope.Code
				}
			} else {
				code = envelope.Error.Code
			}
		}
	}
	if source == auth.ResponsesEvent {
		// The caller must first validate the complete native terminal event.
		if code != sharingUsageLimit {
			return auth.FailureSignal{}, false
		}
		signal.HTTPStatus = http.StatusTooManyRequests
	} else if status == http.StatusUnauthorized || status == http.StatusForbidden {
		signal.Reason, signal.Scope = auth.AuthorizationReview, auth.Grant
		return signal, true
	}
	if signal.HTTPStatus != http.StatusTooManyRequests {
		return auth.FailureSignal{}, false
	}
	if code == sharingUsageLimit {
		signal.Reason, signal.Scope, signal.ProviderCode = auth.UsageLimited, auth.Grant, sharingUsageLimit
	} else {
		signal.Reason, signal.Scope = auth.RateLimitUnknown, auth.Model
	}
	return signal, true
}
