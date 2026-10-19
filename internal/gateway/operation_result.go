package gateway

import (
	"minigateway/internal/core"
	execution "minigateway/internal/foundation/contracts/execution/v1"
)

// Non-200 bodies bypass the native decoder. Only trusted local facts select an
// error description; no provider message, type, code or transport detail escapes.
func sanitizeOperationFailure(result core.Result, messages execution.FailureMessages, nativeFailed bool) core.Result {
	result.Replayable = false
	if result.Error == nil {
		return result
	}
	kind, message := "upstream_response_error", messages.Upstream
	switch result.Termination {
	case core.Canceled:
		kind, message = "canceled", messages.Canceled
	case core.Deadline:
		kind, message = "deadline", messages.Deadline
	case core.ClientWriteError:
		kind, message = "client_write_error", messages.Output
	default:
		if result.ProtocolComplete && nativeFailed {
			kind, message = "upstream_response_failed", messages.Generation
		}
	}
	result.Error = &core.Error{StatusCode: result.StatusCode, Error: core.ErrorDetail{Message: message, Type: kind}}
	attachHTTPTimeoutCode(&result)
	return result
}

func requestPromptText(req execution.Request) string { return req.PromptText() }
func requestMaxTokens(req execution.Request) int     { return req.LegacyMaxTokens() }
func requestCompletionText(req execution.Request, body []byte) string {
	return req.CompletionText(body)
}
