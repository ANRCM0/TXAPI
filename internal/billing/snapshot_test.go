package billing

import (
	"errors"
	"math"
	"sync"
	"testing"
	"time"

	"minigateway/internal/model"
	relaymodel "minigateway/internal/protocol/model"
)

func TestFixedPointPriceArithmeticAndBounds(t *testing.T) {
	table := newTestTable([]model.ModelPrice{
		{Model: "m", InputPricePer1K: 0.15, OutputPricePer1K: 0.6},
		{Model: "tiny", InputPricePer1K: 0.00000000001},
		{Model: "huge", InputPricePer1K: 1_000_000_000},
		{Model: "free"},
	}, map[string]float64{"relay": 1.2})
	s := table.Snapshot()
	for _, tc := range []struct {
		name  string
		group string
		usage relaymodel.Usage
		units int64
	}{
		{"m", "default", relaymodel.Usage{PromptTokens: 11, CompletionTokens: 7}, 5_850_000},
		{"m", "relay", relaymodel.Usage{PromptTokens: 11, CompletionTokens: 7}, 7_020_000},
		{"tiny", "", relaymodel.Usage{PromptTokens: 1}, 1},
		{"free", "", relaymodel.Usage{PromptTokens: 100}, 0},
	} {
		got, err := s.CostUnits(&tc.usage, tc.name, tc.group)
		if err != nil || got != tc.units {
			t.Fatalf("%s/%s: units=%d err=%v, want %d", tc.name, tc.group, got, err, tc.units)
		}
	}
	for _, tc := range []struct {
		name  string
		usage *relaymodel.Usage
	}{
		{"m", nil},
		{"m", &relaymodel.Usage{PromptTokens: -1}},
		{"m", &relaymodel.Usage{CompletionTokens: -1}},
		{"missing", &relaymodel.Usage{}},
		{"huge", &relaymodel.Usage{PromptTokens: math.MaxInt}},
	} {
		if _, err := s.CostUnits(tc.usage, tc.name, ""); err == nil {
			t.Fatalf("invalid or overflowing charge accepted: %+v", tc)
		}
	}
	for _, value := range []float64{-1, math.NaN(), math.Inf(1), math.MaxFloat64} {
		if _, err := buildSnapshot([]model.ModelPrice{{Model: "m", InputPricePer1K: value}}, nil); err == nil {
			t.Fatalf("invalid price %v published", value)
		}
	}
	for _, value := range []float64{0, -1, math.NaN(), math.Inf(1)} {
		if _, err := buildSnapshot(nil, map[string]float64{"group": value}); err == nil {
			t.Fatalf("invalid group rate %v published", value)
		}
	}
}

func TestSnapshotVersionAndWildcardSelectionAreDeterministic(t *testing.T) {
	prices := []model.ModelPrice{
		{Model: "*mini", InputPricePer1K: 2},
		{Model: "gpt*", InputPricePer1K: 1},
		{Model: "gpt-4", InputPricePer1K: 3},
	}
	a, err := buildSnapshot(prices, map[string]float64{"a": 1, "b": 2})
	if err != nil {
		t.Fatal(err)
	}
	prices[0], prices[2] = prices[2], prices[0]
	b, err := buildSnapshot(prices, map[string]float64{"b": 2, "a": 1})
	if err != nil || a.Version() != b.Version() || a.payload != b.payload {
		t.Fatalf("same content changed version: %v", err)
	}
	for _, tc := range []struct {
		pattern, name string
		want          bool
	}{
		{"*mini", "mini-mini", true},
		{"*mini", "mini-pro", false},
		{"gpt*mini", "gpt-mini-mini", true},
		{"gpt*mini", "gpt-mini-extra", false},
		{"gpt*mid*mini", "gpt-mid-mini", true},
		{"gpt*mid*mini", "gpt-mini-mid", false},
	} {
		if got := wildcardMatch(tc.pattern, tc.name); got != tc.want {
			t.Fatalf("%s/%s = %v", tc.pattern, tc.name, got)
		}
	}
	prices[0].UpdatedAt = time.Now()
	c, _ := buildSnapshot(prices, map[string]float64{"a": 1, "b": 2})
	if a.Version() != c.Version() {
		t.Fatal("metadata-only change repriced requests")
	}
	// Equal-specificity candidates must not depend on database iteration order.
	d, _ := buildSnapshot([]model.ModelPrice{{Model: "a*", InputPricePer1K: 1}, {Model: "*a", InputPricePer1K: 2}}, nil)
	p, _ := d.price("aa")
	if p.Model != "*a" {
		t.Fatalf("unstable wildcard tie: %+v", p)
	}
}

func TestRequestKeepsPriceAndRateAcrossPublication(t *testing.T) {
	price, rate := 1.0, 1.0
	table := NewPriceTable(func() ([]model.ModelPrice, map[string]float64, error) {
		return []model.ModelPrice{{Model: "m", InputPricePer1K: price}}, map[string]float64{"g": rate}, nil
	}, time.Hour)
	engine := New(nil, table)
	pre, err := engine.PreRequest(0, 0, 0, "m")
	if err != nil {
		t.Fatal(err)
	}
	price, rate = 5, 2
	if err := table.ReloadPrices(); err != nil {
		t.Fatal(err)
	}
	u := &relaymodel.Usage{PromptTokens: 1000}
	old, ok := engine.PriceOfSnapshot(pre, u, "m", "m", "g")
	current, _ := engine.PriceOf(u, "m", "m", "g")
	if !ok || old != 1 || current != 10 || pre.PriceVersion() == table.Snapshot().Version() {
		t.Fatalf("request repriced: old=%v current=%v", old, current)
	}
	copy := pre.PriceOnly()
	if copy.PriceVersion() != pre.PriceVersion() || copy.TokenLimited || copy.UserLimited {
		t.Fatal("legacy quota failure lost price version or retained deductions")
	}
	missing, err := engine.PreRequest(0, 0, 0, "missing")
	if err != nil {
		t.Fatal(err)
	}
	table.loader = func() ([]model.ModelPrice, map[string]float64, error) {
		return []model.ModelPrice{{Model: "missing", InputPricePer1K: 100}}, nil, nil
	}
	if err := table.ReloadPrices(); err != nil {
		t.Fatal(err)
	}
	if _, found := engine.PriceOfSnapshot(missing, u, "missing", "missing", ""); found {
		t.Fatal("missing price added mid-flight charged retroactively")
	}
}

func TestSnapshotPersistenceFailureRetainsPublishedVersion(t *testing.T) {
	price := 1.0
	fail := false
	table := NewPriceTableWithHistory(func() ([]model.ModelPrice, map[string]float64, error) {
		return []model.ModelPrice{{Model: "m", InputPricePer1K: price}}, nil, nil
	}, time.Hour, func(version, payload string) error {
		if version == "" || payload == "" {
			t.Fatal("empty history")
		}
		if fail {
			return errors.New("controlled persistence failure")
		}
		return nil
	})
	if err := table.ReloadPrices(); err != nil {
		t.Fatal(err)
	}
	old := table.Snapshot()
	price, fail = 10, true
	if err := table.ReloadPrices(); err == nil || table.Snapshot() != old {
		t.Fatal("published unpersisted prices")
	}
	price, fail = -1, false
	if err := table.ReloadPrices(); err == nil || table.Snapshot() != old {
		t.Fatal("published invalid prices")
	}
	price = 10
	if err := table.ReloadPrices(); err != nil || table.Snapshot().Version() == old.Version() {
		t.Fatal("valid retry did not publish")
	}
}

func TestConcurrentSnapshotReloadAndPricing(t *testing.T) {
	var mu sync.Mutex
	price := 1.0
	table := NewPriceTable(func() ([]model.ModelPrice, map[string]float64, error) {
		mu.Lock()
		defer mu.Unlock()
		price++
		return []model.ModelPrice{{Model: "m", InputPricePer1K: price}}, nil, nil
	}, time.Hour)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := table.ReloadPrices(); err != nil {
				t.Error(err)
			}
			s := table.Snapshot()
			p, ok := s.price("m")
			units, err := s.CostUnits(&relaymodel.Usage{PromptTokens: 1000}, "m", "")
			if err != nil || !ok || units != p.InputUnitsPer1K || s.Version() == "" {
				t.Error("torn snapshot during reload")
			}
		}()
	}
	wg.Wait()
}

var benchmarkCost float64

func BenchmarkPriceCost(b *testing.B) {
	s, err := buildSnapshot([]model.ModelPrice{{Model: "m", InputPricePer1K: 0.15, OutputPricePer1K: 0.6}}, map[string]float64{"g": 1.2})
	if err != nil {
		b.Fatal(err)
	}
	u := &relaymodel.Usage{PromptTokens: 11, CompletionTokens: 7}
	b.Run("FixedSnapshot", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			units, err := s.CostUnits(u, "m", "g")
			if err != nil {
				b.Fatal(err)
			}
			benchmarkCost = float64(units) / float64(UnitScale)
		}
	})
	// Arithmetic-only comparison, not an end-to-end or historical gateway baseline.
	b.Run("FloatFormulaReference", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			p, _ := s.price("m")
			benchmarkCost = (float64(u.PromptTokens)/1000*(float64(p.InputUnitsPer1K)/float64(UnitScale)) +
				float64(u.CompletionTokens)/1000*(float64(p.OutputUnitsPer1K)/float64(UnitScale))) *
				(float64(s.rate("g")) / float64(UnitScale))
		}
	})
}
