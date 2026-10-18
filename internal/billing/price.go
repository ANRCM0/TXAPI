// Package billing contains optional pricing, legacy quotas and strict settlement.
package billing

import (
	"log/slog"
	"strings"
	"sync"
	"time"

	relaymodel "minigateway/internal/foundation/contracts/execution/v1"
	"minigateway/internal/model"
)

type PriceLoader func() (prices []model.ModelPrice, rates map[string]float64, err error)

// SnapshotRecorder persists immutable content before it becomes chargeable.
// It must return an error if the same version already has different content.
type SnapshotRecorder func(version, payload string) error

// PriceTable publishes complete immutable snapshots. A request holds one
// snapshot across pre-deduction, retries and final pricing.
type PriceTable struct {
	mu       sync.RWMutex
	reloadMu sync.Mutex
	snapshot *PriceSnapshot
	loader   PriceLoader
	recorder SnapshotRecorder
	ttl      time.Duration
	loadedAt time.Time
}

const DefaultPriceTTL = 5 * time.Minute

func NewPriceTable(loader PriceLoader, ttl time.Duration) *PriceTable {
	return NewPriceTableWithHistory(loader, ttl, nil)
}

func NewPriceTableWithHistory(loader PriceLoader, ttl time.Duration, recorder SnapshotRecorder) *PriceTable {
	if ttl <= 0 {
		ttl = DefaultPriceTTL
	}
	return &PriceTable{loader: loader, recorder: recorder, ttl: ttl}
}

func (t *PriceTable) Snapshot() *PriceSnapshot {
	t.refreshIfStale()
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.snapshot
}

func (t *PriceTable) Price(name string) (model.ModelPrice, bool) {
	p, ok := t.Snapshot().price(name)
	return model.ModelPrice{Model: p.Model, InputPricePer1K: float64(p.InputUnitsPer1K) / float64(UnitScale), OutputPricePer1K: float64(p.OutputUnitsPer1K) / float64(UnitScale)}, ok
}

func (t *PriceTable) Rate(group string) float64 {
	return float64(t.Snapshot().rate(group)) / float64(UnitScale)
}

// Cost retains the legacy API. Missing/invalid usage and arithmetic overflow
// remain non-billable in legacy mode; strict mode never uses this fallback.
func (t *PriceTable) Cost(u *relaymodel.Usage, name, group string) (float64, bool) {
	units, err := t.Snapshot().CostUnits(u, name, group)
	if err != nil {
		return 0, false
	}
	return float64(units) / float64(UnitScale), true
}

// EstimateUpperBound is a legacy output-only estimate, NOT a strict bound.
func (t *PriceTable) EstimateUpperBound(maxTokens int, name string) float64 {
	return t.Snapshot().estimateOutput(maxTokens, name)
}

func (t *PriceTable) ReloadPrices() error {
	t.reloadMu.Lock()
	defer t.reloadMu.Unlock()
	return t.reload()
}

func (t *PriceTable) reload() error {
	if t.loader == nil {
		return nil
	}
	prices, rates, err := t.loader()
	if err != nil {
		return err
	}
	next, err := buildSnapshot(prices, rates)
	if err != nil {
		return err
	}
	if t.recorder != nil {
		if err := t.recorder(next.version, next.payload); err != nil {
			return err
		}
	}
	t.mu.Lock()
	t.snapshot = next
	t.loadedAt = time.Now()
	t.mu.Unlock()
	return nil
}

func (t *PriceTable) refreshIfStale() {
	if t.loader == nil {
		return
	}
	t.mu.RLock()
	stale := t.loadedAt.IsZero() || time.Since(t.loadedAt) > t.ttl
	t.mu.RUnlock()
	if !stale {
		return
	}
	t.reloadMu.Lock()
	defer t.reloadMu.Unlock()
	t.mu.RLock()
	stale = t.loadedAt.IsZero() || time.Since(t.loadedAt) > t.ttl
	t.mu.RUnlock()
	if stale {
		if err := t.reload(); err != nil {
			slog.Error("price snapshot refresh failed; previous version retained", "err", err)
		}
	}
}

func wildcardMatch(pattern, name string) bool {
	if !strings.Contains(pattern, "*") {
		return pattern == name
	}
	parts := strings.Split(pattern, "*")
	if !strings.HasPrefix(name, parts[0]) {
		return false
	}
	rest := name[len(parts[0]):]
	for _, part := range parts[1 : len(parts)-1] {
		idx := strings.Index(rest, part)
		if idx < 0 {
			return false
		}
		rest = rest[idx+len(part):]
	}
	return strings.HasSuffix(rest, parts[len(parts)-1])
}

// wildcardScore 是通配条目的「具体度」分数：字面量字符越多越具体。
// 同分时按模型字典序选择，保证排序确定。
func wildcardScore(pattern string) int {
	return len(pattern) - strings.Count(pattern, "*")
}
