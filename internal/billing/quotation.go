package billing

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"sort"
	"strconv"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	execution "minigateway/internal/foundation/contracts/execution/v3"
	q "minigateway/internal/foundation/contracts/quotation/v1"
)

const quotationPriceProfile = "responses_input_partitions_sale_v1"
const quotationUsageProfile = "responses_native_tokens_v2"
const quotationDocumentLimit = 256 << 10

type QuotationHistoryReader func(context.Context, string) (string, error)

type QuotationPrice struct {
	Model                     string `json:"model"`
	UncachedInputUnitsPer1K   string `json:"uncached_input_units_per_1k"`
	CachedInputUnitsPer1K     string `json:"cached_input_units_per_1k"`
	CacheWriteInputUnitsPer1K string `json:"cache_write_input_units_per_1k"`
	OutputUnitsPer1K          string `json:"output_units_per_1k"`
}

type QuotationConfig struct {
	Currency       string
	Prices         []QuotationPrice
	GroupRateUnits map[string]string
}

type quotationDocument struct {
	Format         int               `json:"format"`
	Kind           string            `json:"kind"`
	PriceProfile   string            `json:"price_profile"`
	UsageProfile   string            `json:"usage_profile"`
	Currency       string            `json:"currency"`
	UnitScale      string            `json:"unit_scale"`
	Prices         []QuotationPrice  `json:"prices"`
	GroupRateUnits map[string]string `json:"group_rate_units"`
}

type quotationSnapshot struct {
	version string
	payload string
	doc     quotationDocument
}

type QuotationBook struct {
	mu       sync.RWMutex
	replace  sync.Mutex
	current  *quotationSnapshot
	recorder SnapshotRecorder
	reader   QuotationHistoryReader
}

func NewQuotationBook(recorder SnapshotRecorder, reader QuotationHistoryReader) *QuotationBook {
	return &QuotationBook{recorder: recorder, reader: reader}
}

func quotationIdentifier(value string, model bool) bool {
	if len(value) == 0 || len(value) > 128 || !utf8.ValidString(value) || model && strings.Contains(value, "*") {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func quotationCurrency(value string) bool {
	if len(value) == 0 || len(value) > 32 {
		return false
	}
	for _, c := range []byte(value) {
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-' || c == '.' || c == ':' {
			continue
		}
		return false
	}
	return true
}

func quotationInteger(value string, max uint64, positive bool) bool {
	if len(value) == 0 || len(value) > 19 || len(value) > 1 && value[0] == '0' {
		return false
	}
	for _, c := range []byte(value) {
		if c < '0' || c > '9' {
			return false
		}
	}
	n, err := strconv.ParseUint(value, 10, 64)
	return err == nil && n <= max && (!positive || n != 0)
}

func compileQuotation(config QuotationConfig) (*quotationSnapshot, error) {
	if !quotationCurrency(config.Currency) || len(config.Prices) == 0 || len(config.Prices) > 256 || len(config.GroupRateUnits) == 0 || len(config.GroupRateUnits) > 64 {
		return nil, q.ErrInvalid
	}
	doc := quotationDocument{Format: 2, Kind: "dimensional_quote", PriceProfile: quotationPriceProfile, UsageProfile: quotationUsageProfile,
		Currency: strings.Clone(config.Currency), UnitScale: "1000000000", Prices: append([]QuotationPrice(nil), config.Prices...), GroupRateUnits: make(map[string]string, len(config.GroupRateUnits))}
	seen := map[string]bool{}
	for i, price := range doc.Prices {
		if !quotationIdentifier(price.Model, true) || seen[price.Model] {
			return nil, q.ErrInvalid
		}
		seen[price.Model] = true
		for _, rate := range []string{price.UncachedInputUnitsPer1K, price.CachedInputUnitsPer1K, price.CacheWriteInputUnitsPer1K, price.OutputUnitsPer1K} {
			if !quotationInteger(rate, 1_000_000_000_000_000_000, false) {
				return nil, q.ErrInvalid
			}
		}
		doc.Prices[i] = QuotationPrice{Model: strings.Clone(price.Model), UncachedInputUnitsPer1K: strings.Clone(price.UncachedInputUnitsPer1K),
			CachedInputUnitsPer1K: strings.Clone(price.CachedInputUnitsPer1K), CacheWriteInputUnitsPer1K: strings.Clone(price.CacheWriteInputUnitsPer1K), OutputUnitsPer1K: strings.Clone(price.OutputUnitsPer1K)}
	}
	for group, rate := range config.GroupRateUnits {
		if !quotationIdentifier(group, false) || !quotationInteger(rate, 1_000_000_000_000_000, true) {
			return nil, q.ErrInvalid
		}
		doc.GroupRateUnits[strings.Clone(group)] = strings.Clone(rate)
	}
	sort.Slice(doc.Prices, func(i, j int) bool { return doc.Prices[i].Model < doc.Prices[j].Model })
	payload, err := json.Marshal(doc)
	if err != nil || len(payload) > quotationDocumentLimit {
		return nil, q.ErrInvalid
	}
	digest := sha256.Sum256(payload)
	return &quotationSnapshot{version: hex.EncodeToString(digest[:]), payload: string(payload), doc: doc}, nil
}

func (b *QuotationBook) Replace(config QuotationConfig) error {
	if b == nil || b.recorder == nil {
		return q.ErrUnavailable
	}
	b.replace.Lock()
	defer b.replace.Unlock()
	next, err := compileQuotation(config)
	if err != nil {
		return err
	}
	if err := b.recorder(next.version, next.payload); err != nil {
		return q.ErrUnavailable
	}
	b.mu.Lock()
	b.current = next
	b.mu.Unlock()
	return nil
}

func (b *QuotationBook) Current(ctx context.Context) (q.Current, error) {
	if b == nil || ctx == nil {
		return q.Current{}, q.ErrUnavailable
	}
	if err := ctx.Err(); err != nil {
		return q.Current{}, err
	}
	b.mu.RLock()
	current := b.current
	b.mu.RUnlock()
	if current == nil {
		return q.Current{Enabled: false}, nil
	}
	return q.Current{Enabled: true, Version: current.version, Currency: current.doc.Currency, PriceProfile: quotationPriceProfile,
		UsageProfile: quotationUsageProfile, UnitScale: "1000000000"}, nil
}

func quotationVersion(version string) bool {
	if len(version) != 64 {
		return false
	}
	for _, c := range []byte(version) {
		if c >= '0' && c <= '9' || c >= 'a' && c <= 'f' {
			continue
		}
		return false
	}
	return true
}

func restoreQuotation(version, payload string) (*quotationSnapshot, error) {
	if len(payload) == 0 || len(payload) > quotationDocumentLimit || !utf8.ValidString(payload) {
		return nil, q.ErrInvalid
	}
	var doc quotationDocument
	decoder := json.NewDecoder(strings.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&doc); err != nil {
		return nil, q.ErrInvalid
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) || doc.Format != 2 || doc.Kind != "dimensional_quote" ||
		doc.PriceProfile != quotationPriceProfile || doc.UsageProfile != quotationUsageProfile || doc.UnitScale != "1000000000" {
		return nil, q.ErrInvalid
	}
	compiled, err := compileQuotation(QuotationConfig{Currency: doc.Currency, Prices: doc.Prices, GroupRateUnits: doc.GroupRateUnits})
	// Exact canonical comparison rejects duplicate keys, alternate encodings and
	// noncanonical ordering instead of accepting ambiguous historical content.
	if err != nil || compiled.version != version || !bytes.Equal([]byte(compiled.payload), []byte(payload)) {
		return nil, q.ErrInvalid
	}
	return compiled, nil
}

func (b *QuotationBook) Calculate(ctx context.Context, in q.Input) (q.Quote, error) {
	if b == nil || ctx == nil {
		return q.Quote{}, q.ErrUnavailable
	}
	if err := ctx.Err(); err != nil {
		return q.Quote{}, err
	}
	b.mu.RLock()
	current := b.current
	b.mu.RUnlock()
	if current == nil {
		return q.Quote{}, q.ErrUnavailable
	}
	in.Usage = ownedQuotationUsage(in.Usage)
	if !quotationVersion(in.PriceVersion) || !quotationIdentifier(in.Model, true) || !quotationIdentifier(in.Group, false) || in.Usage.Validate() != nil {
		return q.Quote{}, q.ErrInvalid
	}
	snapshot := current
	if in.PriceVersion != current.version {
		if b.reader == nil {
			return q.Quote{}, q.ErrUnavailable
		}
		payload, err := b.reader(ctx, in.PriceVersion)
		if err != nil {
			if errors.Is(err, q.ErrNotFound) {
				return q.Quote{}, q.ErrNotFound
			}
			if ctx.Err() != nil {
				return q.Quote{}, ctx.Err()
			}
			return q.Quote{}, q.ErrUnavailable
		}
		snapshot, err = restoreQuotation(in.PriceVersion, payload)
		if err != nil {
			return q.Quote{}, err
		}
	}
	if err := ctx.Err(); err != nil {
		return q.Quote{}, err
	}
	out := q.Quote{PriceVersion: snapshot.version, Model: in.Model, Group: in.Group, Currency: snapshot.doc.Currency,
		PriceProfile: quotationPriceProfile, UsageProfile: quotationUsageProfile, UnitScale: "1000000000"}
	var price *QuotationPrice
	for i := range snapshot.doc.Prices {
		if snapshot.doc.Prices[i].Model == in.Model {
			price = &snapshot.doc.Prices[i]
			break
		}
	}
	if price == nil {
		out.Reason = "price_missing"
		return out, nil
	}
	rate, ok := snapshot.doc.GroupRateUnits[in.Group]
	if !ok {
		out.Reason = "group_missing"
		return out, nil
	}
	if in.Usage.Profile != "" && in.Usage.Profile != quotationUsageProfile {
		out.Reason = "usage_profile_unsupported"
		return out, nil
	}
	u := in.Usage
	if u.Counts.InputTokens == nil || u.Counts.OutputTokens == nil || u.Counts.TotalTokens == nil || u.Breakdown.CachedInputTokens == nil || u.Breakdown.CacheWriteInputTokens == nil {
		out.Reason = "usage_unknown"
		return out, nil
	}
	uncached := *u.Counts.InputTokens - *u.Breakdown.CachedInputTokens - *u.Breakdown.CacheWriteInputTokens
	counts := []int64{uncached, *u.Breakdown.CachedInputTokens, *u.Breakdown.CacheWriteInputTokens, *u.Counts.OutputTokens}
	prices := []string{price.UncachedInputUnitsPer1K, price.CachedInputUnitsPer1K, price.CacheWriteInputUnitsPer1K, price.OutputUnitsPer1K}
	numerator := new(big.Int)
	for i, count := range counts {
		p, _ := new(big.Int).SetString(prices[i], 10)
		numerator.Add(numerator, new(big.Int).Mul(big.NewInt(count), p))
	}
	multiplier, _ := new(big.Int).SetString(rate, 10)
	numerator.Mul(numerator, multiplier)
	amount, rem := new(big.Int), new(big.Int)
	amount.QuoRem(numerator, big.NewInt(1000*UnitScale), rem)
	if rem.Sign() != 0 {
		amount.Add(amount, big.NewInt(1))
	}
	if !amount.IsInt64() || amount.Sign() < 0 {
		return q.Quote{}, q.ErrOverflow
	}
	if err := ctx.Err(); err != nil {
		return q.Quote{}, err
	}
	out.Known, out.AmountUnits, out.Reason = true, amount.String(), "quoted"
	return out, nil
}

func ownedQuotationUsage(usage execution.UsageObservation) execution.UsageObservation {
	for _, count := range []**int64{&usage.Counts.InputTokens, &usage.Counts.OutputTokens, &usage.Counts.TotalTokens,
		&usage.Breakdown.CachedInputTokens, &usage.Breakdown.CacheWriteInputTokens, &usage.Breakdown.ReasoningOutputTokens} {
		if *count != nil {
			value := **count
			*count = &value
		}
	}
	return usage
}

var _ q.Reader = (*QuotationBook)(nil)
