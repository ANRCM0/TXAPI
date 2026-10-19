package responses

import (
	"context"
	"errors"
	identity "minigateway/internal/foundation/contracts/identity/v1"
	history "minigateway/internal/foundation/contracts/responsehistory/v1"
	state "minigateway/internal/foundation/contracts/state/v1"
)

// History holds HTTP use-case policy. Content protection and persistence are
// owned by the injected shared L1 repository.
type History struct{ repository history.Repository }
type historyContent = history.Content

func NewHistory(repository history.Repository) (*History, error) {
	if missing(repository) {
		return nil, errors.New("Responses history repository is required")
	}
	return &History{repository: repository}, nil
}

func (h *History) read(ctx context.Context, b identity.Binding, id string) (state.ResponseHistory, historyContent, error) {
	return h.repository.Read(ctx, b, id)
}
func (h *History) seal(r state.ResponseHistory, c historyContent) (state.ResponseHistory, error) {
	return h.repository.Seal(r, c)
}
func (h *History) fresh(id, kind, name string, b identity.Binding) state.ResponseHistory {
	return h.repository.Fresh(id, kind, name, b)
}
func (h *History) apply(ctx context.Context, b identity.Binding, changes []state.HistoryChange, guards ...state.HistoryGuard) error {
	return h.repository.Apply(ctx, b, changes, guards...)
}
