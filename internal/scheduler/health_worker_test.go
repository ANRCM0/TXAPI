package scheduler

import (
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	dbmodel "minigateway/internal/model"
)

func TestHealthWorkersKeepIndependentPersistenceAndDrain(t *testing.T) {
	var a, b atomic.Int64
	first := NewHealthWorker(func(u HealthUpdate) error {
		if u.CredentialID != 1 {
			t.Errorf("cross-instance health update: %d", u.CredentialID)
		}
		a.Add(1)
		return nil
	}, nil)
	second := NewHealthWorker(func(u HealthUpdate) error {
		if u.CredentialID != 2 {
			t.Errorf("cross-instance health update: %d", u.CredentialID)
		}
		b.Add(1)
		return nil
	}, nil)
	defer first.Close()
	defer second.Close()
	first.RecordFailure(&dbmodel.Credential{ID: 1, Status: dbmodel.CredentialActive}, http.StatusTooManyRequests, "limit", time.Now())
	second.RecordFailure(&dbmodel.Credential{ID: 2, Status: dbmodel.CredentialActive}, http.StatusTooManyRequests, "limit", time.Now())
	var group sync.WaitGroup
	for i := 0; i < 8; i++ {
		group.Add(1)
		go func() { defer group.Done(); first.Close() }()
	}
	group.Wait()
	first.RecordFailure(&dbmodel.Credential{ID: 1, Status: dbmodel.CredentialActive}, http.StatusTooManyRequests, "after close", time.Now())
	second.RecordFailure(&dbmodel.Credential{ID: 2, Status: dbmodel.CredentialActive}, http.StatusTooManyRequests, "still active", time.Now())
	second.Close()
	if a.Load() != 1 || b.Load() != 2 {
		t.Fatalf("health worker counts: first=%d second=%d", a.Load(), b.Load())
	}
}
func TestHealthWorkerContainsCallbackPanics(t *testing.T) {
	var calls atomic.Int64
	h := NewHealthWorker(func(u HealthUpdate) error {
		calls.Add(1)
		if u.CredentialID == 1 {
			panic("persistence extension")
		}
		return nil
	}, func() { panic("reload extension") })
	for i := uint(1); i <= 3; i++ {
		h.RecordFailure(&dbmodel.Credential{ID: i, Status: dbmodel.CredentialActive}, 503, "upstream error", time.Now())
	}
	h.Close()
	if calls.Load() != 3 {
		t.Fatalf("callback panic killed health worker: calls=%d", calls.Load())
	}
}
