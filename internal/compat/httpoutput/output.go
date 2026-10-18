package httpoutput

import (
	execution "minigateway/internal/foundation/contracts/execution/v1"
	bridge "minigateway/internal/foundation/httpoutput/v1"
	"net/http"
)

// New adapts HTTP delivery to the public output contract; the bridge owns no writer.
func New(w http.ResponseWriter) execution.DeadlineOutput { return bridge.New(w) }
