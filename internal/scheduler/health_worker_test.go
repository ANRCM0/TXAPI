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
func TestHealthWorkerSerializesConcurrentFailures(t *testing.T) {
	current := dbmodel.Credential{ID: 77, Status: dbmodel.CredentialActive, APIKey: "sk"}
	stale := current
	calls := 0
	h := NewHealthWorker(func(u HealthUpdate) error {
		calls++
		current.FailCount, current.Status, current.CooldownUntil = u.FailCount, u.Status, u.CooldownUntil
		return nil
	}, nil, func(uint) (*dbmodel.Credential, error) { copy := current; return &copy, nil })
	var group sync.WaitGroup
	group.Add(8)
	for range 8 {
		go func() { defer group.Done(); h.RecordFailure(&stale, 502, "upstream", time.Now()) }()
	}
	group.Wait()
	h.Close()
	if calls != BanThreshold || current.FailCount != BanThreshold || current.Status != dbmodel.CredentialBanned {
		t.Fatalf("lost failure transitions or continued after ban: calls=%d current=%+v", calls, current)
	}
}
func TestHealthWorkerIgnoresStaleManualGenerationAndLateSuccess(t *testing.T) {
	for _, scenario := range []string{"manual reset", "rotation", "disable", "ban", "late success"} {
		t.Run(scenario, func(t *testing.T) {
			original := dbmodel.Credential{ID: 88, Status: dbmodel.CredentialActive, APIKey: "old"}
			current := original
			switch scenario {
			case "manual reset":
				current.HealthGeneration++
			case "rotation":
				current.HealthGeneration++
				current.APIKey = "new"
			case "disable":
				current.Status = dbmodel.CredentialDisabled
			case "ban":
				current.Status = dbmodel.CredentialBanned
			case "late success":
				current.FailCount = 2
				current.Status = dbmodel.CredentialCooldown
			}
			calls := 0
			h := NewHealthWorker(func(HealthUpdate) error { calls++; return nil }, nil, func(uint) (*dbmodel.Credential, error) { return &current, nil })
			if scenario == "late success" {
				h.RecordSuccess(&original, time.Now())
			} else {
				h.RecordFailure(&original, 502, "upstream", time.Now())
			}
			h.Close()
			if calls != 0 {
				t.Fatal("stale observation overwrote current state")
			}
		})
	}
}
func TestHealthRefreshPanicDoesNotStopWorker(t *testing.T) {
	calls := 0
	h := NewHealthWorker(func(HealthUpdate) error { calls++; return nil }, nil, func(id uint) (*dbmodel.Credential, error) {
		if id == 1 {
			panic("refresh")
		}
		return &dbmodel.Credential{ID: id, Status: dbmodel.CredentialActive}, nil
	})
	for id := uint(1); id <= 2; id++ {
		h.RecordFailure(&dbmodel.Credential{ID: id, Status: dbmodel.CredentialActive}, 502, "upstream", time.Now())
	}
	h.Close()
	if calls != 1 {
		t.Fatalf("refresh panic stopped worker: calls=%d", calls)
	}
}

func TestReviewObservedHealthPreservesStateAndRejectsReorderedEvents(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		current := dbmodel.Credential{ID: 99, Status: dbmodel.CredentialActive, APIKey: "key"}
		failure := current
		failure.HealthObserved = true
		failure.HealthObservedAt = 10
		failure.FailCount = 2
		failure.Status = dbmodel.CredentialCooldown
		failure.CooldownUntil = 1000
		success := current
		success.HealthObserved = true
		success.HealthObservedAt = 20
		var updates []HealthUpdate
		h := NewHealthWorker(func(u HealthUpdate) error {
			updates = append(updates, u)
			current.FailCount, current.Status, current.CooldownUntil, current.HealthObservedAt = u.FailCount, u.Status, u.CooldownUntil, u.ObservedAt
			return nil
		}, nil, func(uint) (*dbmodel.Credential, error) { copy := current; return &copy, nil })
		if reverse {
			h.RecordSuccess(&success, time.Now())
			h.RecordFailure(&failure, 502, "old", time.Now())
		} else {
			h.RecordFailure(&failure, 502, "old", time.Now())
			h.RecordSuccess(&success, time.Now())
		}
		h.Close()
		want := 2
		if reverse {
			want = 1
		}
		if len(updates) != want || current.HealthObservedAt != 20 || current.Status != dbmodel.CredentialActive || current.FailCount != 0 {
			t.Fatalf("reverse=%v reordered health: current=%+v updates=%+v", reverse, current, updates)
		}
		if !reverse && updates[0].FailCount != 2 {
			t.Fatal("worker recomputed already observed failure count")
		}
	}
}
