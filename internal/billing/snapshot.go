package billing

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/big"
	ledger "minigateway/internal/foundation/contracts/ledger/v1"
	"sort"
	"strconv"
	"strings"

	relaymodel "minigateway/internal/foundation/contracts/execution/v1"
	"minigateway/internal/model"
)

// Units are billionths of the configured currency. Prices retain the existing
// per-1K-token API; calculation and versioned storage use integers only.
const UnitScale int64 = ledger.UnitScale

type fixedPrice struct {
	Model            string `json:"model"`
	InputUnitsPer1K  int64  `json:"input_units_per_1k"`
	OutputUnitsPer1K int64  `json:"output_units_per_1k"`
}

type snapshotDocument struct {
	Format    int              `json:"format"`
	UnitScale int64            `json:"unit_scale"`
	Prices    []fixedPrice     `json:"prices"`
	Rates     map[string]int64 `json:"rate_units"`
}

// PriceSnapshot is immutable. Its content digest is independent of load time,
// database ordering and process restarts.
type PriceSnapshot struct {
	version string
	payload string
	exact   map[string]fixedPrice
	wild    []fixedPrice
	rates   map[string]int64
}

func (s *PriceSnapshot) Version() string {
	if s == nil {
		return ""
	}
	return s.version
}

// decimalUnits converts the shortest round-trippable API decimal, not its
// binary float approximation. Sub-nanounit values round upward, never to free.
func decimalUnits(v float64) (int64, error) {
	return decimalUnitsRounded(v, true)
}

func decimalUnitsRounded(v float64, upward bool) (int64, error) {
	if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 {
		return 0, errors.New("price and rate must be finite and nonnegative")
	}
	r, ok := new(big.Rat).SetString(strconv.FormatFloat(v, 'f', -1, 64))
	if !ok {
		return 0, errors.New("invalid decimal price")
	}
	r.Mul(r, new(big.Rat).SetInt64(UnitScale))
	q, rem := new(big.Int), new(big.Int)
	q.QuoRem(r.Num(), r.Denom(), rem)
	if upward && rem.Sign() != 0 {
		q.Add(q, big.NewInt(1))
	}
	if !q.IsInt64() {
		return 0, errors.New("price or rate exceeds fixed-point range")
	}
	return q.Int64(), nil
}

func buildSnapshot(prices []model.ModelPrice, rates map[string]float64) (*PriceSnapshot, error) {
	doc := snapshotDocument{Format: 1, UnitScale: UnitScale, Prices: []fixedPrice{}, Rates: map[string]int64{}}
	seen := map[string]bool{}
	for _, p := range prices {
		if strings.TrimSpace(p.Model) == "" || seen[p.Model] {
			return nil, errors.New("empty or duplicate price model")
		}
		seen[p.Model] = true
		if p.InputPricePer1K > model.MaxPricePer1K || p.OutputPricePer1K > model.MaxPricePer1K {
			return nil, errors.New("model price exceeds supported configuration range")
		}
		in, err := decimalUnits(p.InputPricePer1K)
		if err != nil {
			return nil, fmt.Errorf("input price for %q: %w", p.Model, err)
		}
		out, err := decimalUnits(p.OutputPricePer1K)
		if err != nil {
			return nil, fmt.Errorf("output price for %q: %w", p.Model, err)
		}
		doc.Prices = append(doc.Prices, fixedPrice{Model: p.Model, InputUnitsPer1K: in, OutputUnitsPer1K: out})
	}
	sort.Slice(doc.Prices, func(i, j int) bool { return doc.Prices[i].Model < doc.Prices[j].Model })
	for group, rate := range rates {
		if strings.TrimSpace(group) == "" || rate <= 0 || rate > model.MaxGroupRate {
			return nil, errors.New("group rates require a name and a positive rate")
		}
		units, err := decimalUnits(rate)
		if err != nil {
			return nil, fmt.Errorf("rate for %q: %w", group, err)
		}
		doc.Rates[group] = units
	}
	payload, err := json.Marshal(doc)
	if err != nil {
		return nil, err
	}
	return compileSnapshot(doc, string(payload)), nil
}

func compileSnapshot(doc snapshotDocument, payload string) *PriceSnapshot {
	digest := sha256.Sum256([]byte(payload))
	s := &PriceSnapshot{version: hex.EncodeToString(digest[:]), payload: payload, exact: map[string]fixedPrice{}, rates: doc.Rates}
	for _, p := range doc.Prices {
		if strings.Contains(p.Model, "*") {
			s.wild = append(s.wild, p)
		} else {
			s.exact[p.Model] = p
		}
	}
	// Most specific wins; equal-specificity matches use lexical model order.
	sort.Slice(s.wild, func(i, j int) bool {
		a, b := wildcardScore(s.wild[i].Model), wildcardScore(s.wild[j].Model)
		if a == b {
			return s.wild[i].Model < s.wild[j].Model
		}
		return a > b
	})
	return s
}

func restoreSnapshot(version, payload string) (*PriceSnapshot, error) {
	var doc snapshotDocument
	if err := json.Unmarshal([]byte(payload), &doc); err != nil {
		return nil, err
	}
	if doc.Format != 1 || doc.UnitScale != UnitScale {
		return nil, errors.New("unsupported historical price format")
	}
	for _, p := range doc.Prices {
		if p.InputUnitsPer1K < 0 || p.OutputUnitsPer1K < 0 {
			return nil, errors.New("corrupt historical price")
		}
	}
	for _, rate := range doc.Rates {
		if rate <= 0 {
			return nil, errors.New("corrupt historical group rate")
		}
	}
	s := compileSnapshot(doc, payload)
	if s.Version() != version {
		return nil, errors.New("historical price digest mismatch")
	}
	return s, nil
}

func (s *PriceSnapshot) price(name string) (fixedPrice, bool) {
	if s == nil {
		return fixedPrice{}, false
	}
	if p, ok := s.exact[name]; ok {
		return p, true
	}
	for _, p := range s.wild {
		if wildcardMatch(p.Model, name) {
			return p, true
		}
	}
	return fixedPrice{}, false
}

func (s *PriceSnapshot) rate(group string) int64 {
	if s != nil {
		if r, ok := s.rates[group]; ok {
			return r
		}
	}
	return UnitScale
}

// CostUnits rounds once, upward, after combining both token dimensions and the
// group multiplier. Wide intermediates avoid overflow before the range check.
func (s *PriceSnapshot) CostUnits(u *relaymodel.Usage, name, group string) (int64, error) {
	if u == nil || u.PromptTokens < 0 || u.CompletionTokens < 0 {
		return 0, errors.New("missing or negative token usage")
	}
	return s.costUnits(int64(u.PromptTokens), int64(u.CompletionTokens), name, group)
}

// Bounds use int64 counts directly, without narrowing a provider's declared
// ceiling to the host's int width or changing the settlement rounding rule.
func (s *PriceSnapshot) costUnits(inputTokens, outputTokens int64, name, group string) (int64, error) {
	if inputTokens < 0 || outputTokens < 0 {
		return 0, errors.New("negative token counts")
	}
	p, ok := s.price(name)
	if !ok {
		return 0, fmt.Errorf("no price for model %q", name)
	}
	input := new(big.Int).Mul(big.NewInt(inputTokens), big.NewInt(p.InputUnitsPer1K))
	output := new(big.Int).Mul(big.NewInt(outputTokens), big.NewInt(p.OutputUnitsPer1K))
	numerator := new(big.Int).Mul(input.Add(input, output), big.NewInt(s.rate(group)))
	denominator := big.NewInt(1000 * UnitScale)
	units, rem := new(big.Int), new(big.Int)
	units.QuoRem(numerator, denominator, rem)
	if rem.Sign() != 0 {
		units.Add(units, big.NewInt(1))
	}
	if !units.IsInt64() {
		return 0, errors.New("request cost exceeds fixed-point range")
	}
	return units.Int64(), nil
}

func (s *PriceSnapshot) estimateOutput(maxTokens int, name string) float64 {
	if maxTokens <= 0 {
		return 0
	}
	p, ok := s.price(name)
	if !ok {
		return 0
	}
	// Legacy pre-deduction deliberately excludes group rates.
	single := &PriceSnapshot{exact: map[string]fixedPrice{name: p}}
	units, err := single.CostUnits(&relaymodel.Usage{CompletionTokens: maxTokens}, name, "")
	if err != nil {
		return 0
	}
	return float64(units) / float64(UnitScale)
}
