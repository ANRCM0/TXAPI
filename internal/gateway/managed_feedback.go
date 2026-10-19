package gateway

import (
	"context"
	"time"

	"minigateway/internal/core"
	authv2 "minigateway/internal/foundation/contracts/upstreamauth/v2"
	authv3 "minigateway/internal/foundation/contracts/upstreamauth/v3"
	"minigateway/internal/model"
)

const managedPendingLimit = 16

func issuedManaged(cred *model.Credential, access authv2.AccessCredential) authv3.FailureWrite {
	return authv3.FailureWrite{GateEpoch: access.GateEpoch, Issued: authv2.AccessRequest{
		CredentialID: cred.ID, ExpectedBindingRevision: cred.BindingRevision, AccountID: cred.AccountID,
		ExpectedRevision: access.Revision, ExpectedCredentialRevision: access.CredentialRevision,
		OwnerID: cred.BindingOwnerID, TokenID: cred.BindingTokenID, Model: cred.BindingModel,
	}}
}

func feedbackContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 5*time.Second)
}

func (m *managedInference) record(ctx context.Context, write authv3.FailureWrite) (result authv3.RecordResult, err error) {
	defer func() {
		if recover() != nil {
			result, err = authv3.RecordResult{}, authv3.ErrBlocked
		}
	}()
	return m.recorder.RecordFailure(ctx, write)
}

// Repair serializes with new issuance. Only fixed metadata is retained, bounded
// across concurrent late responses; an overflow permanently fails this process
// closed rather than losing evidence and silently reopening inference.
func (m *managedInference) issue(ctx context.Context, request authv2.AccessRequest) (authv2.AccessCredential, error) {
	work, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if !m.lockFeedback(work) {
		return authv2.AccessCredential{}, authv3.ErrBlocked
	}
	defer func() { <-m.feedbackGate }()
	if !m.flushFeedback(work) {
		return authv2.AccessCredential{}, authv3.ErrBlocked
	}
	m.mu.Lock()
	blocked := m.feedbackOverflow || time.Now().Before(m.backoffUntil)
	m.mu.Unlock()
	if blocked {
		return authv2.AccessCredential{}, authv3.ErrBlocked
	}
	credential, err := m.credentials.AcquireAccess(work, request)
	// Feedback may enqueue while issuance is in flight but cannot persist
	// through the serial gate. Do not use a newly issued token until repaired.
	m.mu.Lock()
	blocked = m.feedbackOverflow || len(m.pending) != 0 || time.Now().Before(m.backoffUntil)
	m.mu.Unlock()
	if blocked || work.Err() != nil {
		return authv2.AccessCredential{}, authv3.ErrBlocked
	}
	return credential, err
}

func (m *managedInference) lockFeedback(ctx context.Context) bool {
	if ctx.Err() != nil {
		return false
	}
	select {
	case m.feedbackGate <- struct{}{}:
		if ctx.Err() != nil {
			<-m.feedbackGate
			return false
		}
		return true
	case <-ctx.Done():
		return false
	}
}

func (m *managedInference) flushFeedback(ctx context.Context) bool {
	for {
		if ctx.Err() != nil {
			return false
		}
		m.mu.Lock()
		if len(m.pending) == 0 {
			m.mu.Unlock()
			return true
		}
		write := m.pending[0]
		m.mu.Unlock()
		if _, err := m.record(ctx, write); err != nil {
			return false
		}
		m.mu.Lock()
		// A concurrently queued stronger fixed classification must also be
		// offered to the owner before this evidence slot can be discarded.
		if m.pending[0] == write {
			m.pending = m.pending[1:]
		}
		m.mu.Unlock()
	}
}

func feedbackPriority(s authv3.FailureSignal) int {
	switch s.Reason {
	case authv3.AuthorizationReview:
		return 3
	case authv3.UsageLimited:
		return 2
	default:
		return 1
	}
}

func (m *managedInference) feedback(write authv3.FailureWrite) {
	work, cancel := feedbackContext()
	defer cancel()
	m.enqueueFeedback(write)
	if !m.lockFeedback(work) {
		return
	}
	defer func() { <-m.feedbackGate }()
	m.flushFeedback(work)
}

func (m *managedInference) enqueueFeedback(write authv3.FailureWrite) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cache = managedCatalog{}
	for i, pending := range m.pending {
		if pending.Issued == write.Issued && pending.GateEpoch == write.GateEpoch {
			if feedbackPriority(write.Signal) > feedbackPriority(pending.Signal) {
				m.pending[i] = write
			}
			return
		}
	}
	if len(m.pending) == managedPendingLimit {
		m.feedbackOverflow = true
		return
	}
	m.pending = append(m.pending, write)
}

func (m *managedInference) observeHTTP(issued authv3.FailureWrite, status int, body []byte, complete bool, source authv3.Source) {
	if status == 503 {
		m.mu.Lock()
		m.backoffUntil = time.Now().Add(5 * time.Second)
		m.mu.Unlock()
	}
	if signal, ok := m.classify(status, body, complete, source); ok {
		issued.Signal = signal
		m.feedback(issued)
	}
}

func (g *Gateway) observeManagedResult(c *executionScope, observation *managedWireObservation, decoder any, result core.Result) {
	if c.managedIssued == nil || result.TimeoutStage != "" || result.Termination == core.Canceled || result.Termination == core.Deadline || result.Termination == core.ClientWriteError {
		return
	}
	if observation.status != 200 {
		g.managed.observeHTTP(*c.managedIssued, observation.status, observation.body, observation.complete, authv3.ResponsesHTTP)
		return
	}
	if observer, ok := decoder.(authv3.FailureObserver); ok {
		if signal, present := observer.InferenceFailure(); present {
			write := *c.managedIssued
			write.Signal = signal
			g.managed.feedback(write)
		}
	}
}
