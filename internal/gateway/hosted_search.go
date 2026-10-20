package gateway

import (
	"encoding/json"

	execution "minigateway/internal/foundation/contracts/execution/v1"
)

// Hosted search has costs beyond token usage. Check both declarations and
// self-contained replay items before admission or any monetary reservation.
func hasHostedSearch(request execution.Request) bool {
	if request.Operation() != execution.Responses {
		return false
	}
	var body struct {
		Tools []struct{ Type string } `json:"tools"`
		Input json.RawMessage         `json:"input"`
	}
	if json.Unmarshal(request.Body(), &body) != nil {
		return false
	}
	for _, tool := range body.Tools {
		if tool.Type == "web_search" {
			return true
		}
	}
	var items []struct{ Type string }
	if json.Unmarshal(body.Input, &items) == nil {
		for _, item := range items {
			if item.Type == "web_search_call" {
				return true
			}
		}
	}
	return false
}
