package gateway

import (
	"encoding/json"

	execution "minigateway/internal/foundation/contracts/execution/v1"
)

// Both the compression request and opaque continuation lack a cost proof.
func hasCompaction(request execution.Request) bool {
	if request.Operation() != execution.Responses {
		return false
	}
	var body struct {
		Input []struct{ Type string } `json:"input"`
	}
	if json.Unmarshal(request.Body(), &body) != nil {
		return false
	}
	for _, item := range body.Input {
		if item.Type == "compaction_trigger" || item.Type == "compaction" {
			return true
		}
	}
	return false
}
