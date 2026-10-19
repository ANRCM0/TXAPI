package billing

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"testing"

	v2 "minigateway/internal/foundation/contracts/execution/v2"
	v3 "minigateway/internal/foundation/contracts/execution/v3"
	q "minigateway/internal/foundation/contracts/quotation/v1"
)

type quotationHistoryFixture struct {
	mu   sync.Mutex
	rows map[string]string
	fail bool
}

func (h *quotationHistoryFixture) record(version, payload string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.fail {
		return errors.New("fixture persistence failure")
	}
	if previous, ok := h.rows[version]; ok && previous != payload {
		return errors.New("immutable history conflict")
	}
	h.rows[version] = payload
	return nil
}
func (h *quotationHistoryFixture) read(ctx context.Context, version string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	payload, ok := h.rows[version]
	if !ok {
		return "", q.ErrNotFound
	}
	return payload, nil
}
func quotationFixture(t *testing.T) (*QuotationBook, *quotationHistoryFixture) {
	t.Helper()
	history := &quotationHistoryFixture{rows: map[string]string{}}
	book := NewQuotationBook(history.record, history.read)
	if err := book.Replace(quotationConfigFixture()); err != nil {
		t.Fatal(err)
	}
	return book, history
}
func quotationConfigFixture() QuotationConfig {
	return QuotationConfig{Currency: "USD", Prices: []QuotationPrice{{Model: "m", UncachedInputUnitsPer1K: "1000", CachedInputUnitsPer1K: "500", CacheWriteInputUnitsPer1K: "2000", OutputUnitsPer1K: "3000"}}, GroupRateUnits: map[string]string{"default": "1000000000"}}
}
func quotationCount(n int64) *int64 { return &n }
func quotationUsageFixture() v3.UsageObservation {
	return v3.UsageObservation{Counts: v2.TokenCounts{InputTokens: quotationCount(10), OutputTokens: quotationCount(4), TotalTokens: quotationCount(14)},
		Breakdown:    v3.TokenBreakdown{CachedInputTokens: quotationCount(2), CacheWriteInputTokens: quotationCount(1)},
		Completeness: v2.Complete, Source: v2.ProviderReported, Profile: quotationUsageProfile}
}
func quotationInputFixture(t *testing.T, book *QuotationBook) q.Input {
	t.Helper()
	current, err := book.Current(context.Background())
	if err != nil || !current.Enabled {
		t.Fatal(current, err)
	}
	return q.Input{PriceVersion: current.Version, Model: "m", Group: "default", Usage: quotationUsageFixture()}
}

func TestQuotationDisabledRequiresPublicationAndExplicitExactTargets(t *testing.T) {
	book := NewQuotationBook(nil, nil)
	current, err := book.Current(context.Background())
	if err != nil || current.Enabled || current.Version != "" {
		t.Fatal(current, err)
	}
	if _, err := book.Calculate(context.Background(), q.Input{}); !errors.Is(err, q.ErrUnavailable) {
		t.Fatal(err)
	}
	book, _ = quotationFixture(t)
	in := quotationInputFixture(t, book)
	for _, version := range []string{"", "latest", strings.Repeat("A", 64), strings.Repeat("f", 63)} {
		in.PriceVersion = version
		if _, err := book.Calculate(context.Background(), in); !errors.Is(err, q.ErrInvalid) {
			t.Fatal(version, err)
		}
	}
	in = quotationInputFixture(t, book)
	in.PriceVersion = strings.Repeat("f", 64)
	if _, err := book.Calculate(context.Background(), in); !errors.Is(err, q.ErrNotFound) {
		t.Fatal(err)
	}
	in = quotationInputFixture(t, book)
	in.Model = "missing"
	quote, err := book.Calculate(context.Background(), in)
	if err != nil || quote.Known || quote.AmountUnits != "" || quote.Reason != "price_missing" {
		t.Fatal(quote, err)
	}
	in.Model, in.Group = "m", "missing"
	quote, err = book.Calculate(context.Background(), in)
	if err != nil || quote.Known || quote.Reason != "group_missing" {
		t.Fatal(quote, err)
	}
}

func TestQuotationPartitionsUniformOutputAndOneRounding(t *testing.T) {
	book, _ := quotationFixture(t)
	in := quotationInputFixture(t, book)
	quote, err := book.Calculate(context.Background(), in)
	if err != nil || !quote.Known || quote.AmountUnits != "22" || quote.Reason != "quoted" || quote.Currency != "USD" || quote.UnitScale != "1000000000" {
		t.Fatal(quote, err)
	}
	in.Usage.Breakdown.ReasoningOutputTokens = quotationCount(4)
	withReasoning, err := book.Calculate(context.Background(), in)
	if err != nil || withReasoning.AmountUnits != quote.AmountUnits {
		t.Fatal("reasoning was priced twice", withReasoning, err)
	}
	config := quotationConfigFixture()
	config.Prices[0] = QuotationPrice{Model: "m", UncachedInputUnitsPer1K: "1", CachedInputUnitsPer1K: "1", CacheWriteInputUnitsPer1K: "1", OutputUnitsPer1K: "1"}
	if err := book.Replace(config); err != nil {
		t.Fatal(err)
	}
	in = quotationInputFixture(t, book)
	quote, err = book.Calculate(context.Background(), in)
	if err != nil || quote.AmountUnits != "1" {
		t.Fatal("partitions rounded individually", quote, err)
	}
	config.GroupRateUnits["default"] = "2000000000"
	config.Prices[0].OutputUnitsPer1K = "1000"
	if err := book.Replace(config); err != nil {
		t.Fatal(err)
	}
	quote, err = book.Calculate(context.Background(), quotationInputFixture(t, book))
	if err != nil || quote.AmountUnits != "9" {
		t.Fatal("group multiplier or final rounding wrong", quote, err)
	}
}

func TestQuotationUnknownCountsRemainUnknownEvenWithZeroPrices(t *testing.T) {
	book, _ := quotationFixture(t)
	config := quotationConfigFixture()
	config.Prices[0] = QuotationPrice{Model: "m", UncachedInputUnitsPer1K: "0", CachedInputUnitsPer1K: "0", CacheWriteInputUnitsPer1K: "0", OutputUnitsPer1K: "0"}
	if err := book.Replace(config); err != nil {
		t.Fatal(err)
	}
	for index := range 5 {
		in := quotationInputFixture(t, book)
		fields := []**int64{&in.Usage.Counts.InputTokens, &in.Usage.Counts.OutputTokens, &in.Usage.Counts.TotalTokens, &in.Usage.Breakdown.CachedInputTokens, &in.Usage.Breakdown.CacheWriteInputTokens}
		*fields[index] = nil
		if index < 3 {
			in.Usage.Completeness = v2.Partial
		}
		quote, err := book.Calculate(context.Background(), in)
		if err != nil || quote.Known || quote.AmountUnits != "" || quote.Reason != "usage_unknown" {
			t.Fatal(index, quote, err)
		}
	}
	in := quotationInputFixture(t, book)
	in.Usage = v3.UsageObservation{Counts: v2.TokenCounts{InputTokens: quotationCount(0), OutputTokens: quotationCount(0), TotalTokens: quotationCount(0)},
		Breakdown: v3.TokenBreakdown{CachedInputTokens: quotationCount(0), CacheWriteInputTokens: quotationCount(0)}, Completeness: v2.Complete, Source: v2.ProviderReported, Profile: quotationUsageProfile}
	quote, err := book.Calculate(context.Background(), in)
	if err != nil || !quote.Known || quote.AmountUnits != "0" {
		t.Fatal("explicit zero became unknown", quote, err)
	}
	in.Usage.Profile = "other-profile"
	quote, err = book.Calculate(context.Background(), in)
	if err != nil || quote.Known || quote.Reason != "usage_profile_unsupported" {
		t.Fatal(quote, err)
	}
	in.Usage.Counts.InputTokens = quotationCount(-1)
	if _, err := book.Calculate(context.Background(), in); !errors.Is(err, q.ErrInvalid) {
		t.Fatal("invalid count downgraded to unknown", err)
	}
}

func TestQuotationLargeIntermediatesAndFinalOverflow(t *testing.T) {
	book, _ := quotationFixture(t)
	config := quotationConfigFixture()
	config.Prices[0] = QuotationPrice{Model: "m", UncachedInputUnitsPer1K: "1000", CachedInputUnitsPer1K: "1000", CacheWriteInputUnitsPer1K: "1000", OutputUnitsPer1K: "1000"}
	if err := book.Replace(config); err != nil {
		t.Fatal(err)
	}
	in := quotationInputFixture(t, book)
	in.Usage.Counts = v2.TokenCounts{InputTokens: quotationCount(math.MaxInt64 - 1), OutputTokens: quotationCount(1), TotalTokens: quotationCount(math.MaxInt64)}
	in.Usage.Breakdown = v3.TokenBreakdown{CachedInputTokens: quotationCount(0), CacheWriteInputTokens: quotationCount(0)}
	quote, err := book.Calculate(context.Background(), in)
	if err != nil || quote.AmountUnits != fmt.Sprint(int64(math.MaxInt64)) {
		t.Fatal(quote, err)
	}
	config.Prices[0].UncachedInputUnitsPer1K = "1000000000000000000"
	config.GroupRateUnits["default"] = "1000000000000000"
	if err := book.Replace(config); err != nil {
		t.Fatal(err)
	}
	current, _ := book.Current(context.Background())
	in.PriceVersion = current.Version
	if _, err := book.Calculate(context.Background(), in); !errors.Is(err, q.ErrOverflow) {
		t.Fatal(err)
	}
}

func TestQuotationHistoryPinningRestartAndFailedPublication(t *testing.T) {
	book, history := quotationFixture(t)
	in := quotationInputFixture(t, book)
	config := quotationConfigFixture()
	config.Prices[0].UncachedInputUnitsPer1K = "2000"
	history.fail = true
	if err := book.Replace(config); !errors.Is(err, q.ErrUnavailable) {
		t.Fatal(err)
	}
	current, _ := book.Current(context.Background())
	if current.Version != in.PriceVersion {
		t.Fatal("failed persistence published current")
	}
	history.fail = false
	if err := book.Replace(config); err != nil {
		t.Fatal(err)
	}
	published, _ := book.Current(context.Background())
	config.Prices[0].UncachedInputUnitsPer1K = "9999"
	config.GroupRateUnits["default"] = "9999"
	quote, err := book.Calculate(context.Background(), in)
	if err != nil || quote.AmountUnits != "22" {
		t.Fatal("old version repriced", quote, err)
	}
	restarted := NewQuotationBook(history.record, history.read)
	if err := restarted.Replace(quotationConfigFixture()); err != nil {
		t.Fatal(err)
	}
	quote, err = restarted.Calculate(context.Background(), q.Input{PriceVersion: published.Version, Model: "m", Group: "default", Usage: quotationUsageFixture()})
	if err != nil || quote.AmountUnits != "29" {
		t.Fatal("restart lost history", quote, err)
	}
	newInput := quotationInputFixture(t, book)
	quote, err = book.Calculate(context.Background(), newInput)
	if err != nil || quote.AmountUnits != "29" {
		t.Fatal("config alias mutated current", quote, err)
	}
}

func TestQuotationRejectsLegacyOrAmbiguousHistoryWithoutFallback(t *testing.T) {
	book, history := quotationFixture(t)
	in := quotationInputFixture(t, book)
	payloads := []string{
		`{"format":1,"unit_scale":1000000000,"prices":[],"rate_units":{}}`,
		book.current.payload + " ",
		strings.Replace(book.current.payload, `"format":2`, `"format":2,"format":2`, 1),
		strings.Replace(book.current.payload, `"kind":"dimensional_quote"`, `"kind":"other"`, 1),
		strings.Repeat("x", quotationDocumentLimit+1),
	}
	for _, payload := range payloads {
		digest := sha256.Sum256([]byte(payload))
		version := hex.EncodeToString(digest[:])
		history.rows[version] = payload
		in.PriceVersion = version
		if _, err := book.Calculate(context.Background(), in); !errors.Is(err, q.ErrInvalid) {
			t.Fatal(err)
		}
	}
	// The original two-counter reader still refuses the dimensional document.
	if _, err := restoreSnapshot(book.current.version, book.current.payload); err == nil {
		t.Fatal("legacy settlement accepted dimensional quote history")
	}
}

func TestQuotationConfigValidationAndCanonicalVersions(t *testing.T) {
	for _, edit := range []func(*QuotationConfig){
		func(c *QuotationConfig) { c.Currency = "" },
		func(c *QuotationConfig) { c.Currency = strings.Repeat("x", 33) },
		func(c *QuotationConfig) { c.Currency = "US D" },
		func(c *QuotationConfig) { c.Prices = nil },
		func(c *QuotationConfig) { c.Prices = append(c.Prices, c.Prices[0]) },
		func(c *QuotationConfig) { c.Prices[0].Model = "m*" },
		func(c *QuotationConfig) { c.Prices[0].Model = "\xff" },
		func(c *QuotationConfig) { c.Prices[0].Model = strings.Repeat("x", 129) },
		func(c *QuotationConfig) { c.Prices[0].Model = "m\n" },
		func(c *QuotationConfig) { c.Prices[0].CachedInputUnitsPer1K = "01" },
		func(c *QuotationConfig) { c.Prices[0].CachedInputUnitsPer1K = "-1" },
		func(c *QuotationConfig) { c.Prices[0].CachedInputUnitsPer1K = "1000000000000000001" },
		func(c *QuotationConfig) { c.GroupRateUnits = nil },
		func(c *QuotationConfig) { c.GroupRateUnits["default"] = "0" },
		func(c *QuotationConfig) { c.GroupRateUnits["default"] = "1000000000000001" },
	} {
		config := quotationConfigFixture()
		edit(&config)
		if _, err := compileQuotation(config); !errors.Is(err, q.ErrInvalid) {
			t.Fatal(config, err)
		}
	}
	config := quotationConfigFixture()
	config.Prices = append(config.Prices, QuotationPrice{Model: "a", UncachedInputUnitsPer1K: "0", CachedInputUnitsPer1K: "0", CacheWriteInputUnitsPer1K: "0", OutputUnitsPer1K: "0"})
	a, err := compileQuotation(config)
	if err != nil {
		t.Fatal(err)
	}
	config.Prices[0], config.Prices[1] = config.Prices[1], config.Prices[0]
	b, err := compileQuotation(config)
	if err != nil || a.version != b.version {
		t.Fatal("price ordering changed digest", err)
	}
	for _, size := range []int{256, 257} {
		config = quotationConfigFixture()
		config.Prices = nil
		for i := 0; i < size; i++ {
			p := quotationConfigFixture().Prices[0]
			p.Model = fmt.Sprintf("m%d", i)
			config.Prices = append(config.Prices, p)
		}
		_, err := compileQuotation(config)
		if (err == nil) != (size == 256) {
			t.Fatal(size, err)
		}
	}
	config = quotationConfigFixture()
	config.GroupRateUnits = map[string]string{}
	for i := 0; i < 65; i++ {
		config.GroupRateUnits[fmt.Sprint(i)] = "1"
	}
	if _, err := compileQuotation(config); !errors.Is(err, q.ErrInvalid) {
		t.Fatal(err)
	}
	config = quotationConfigFixture()
	config.Prices = nil
	for i := 0; i < 256; i++ {
		p := quotationConfigFixture().Prices[0]
		p.Model = fmt.Sprintf("%03d", i) + strings.Repeat("<", 125)
		config.Prices = append(config.Prices, p)
	}
	config.GroupRateUnits = map[string]string{}
	for i := 0; i < 64; i++ {
		config.GroupRateUnits[fmt.Sprintf("%03d", i)+strings.Repeat("<", 125)] = "1"
	}
	if _, err := compileQuotation(config); !errors.Is(err, q.ErrInvalid) {
		t.Fatal("encoded document bound ignored", err)
	}
}

func TestQuotationOwnsInputAcrossHistoricalRead(t *testing.T) {
	book, history := quotationFixture(t)
	in := quotationInputFixture(t, book)
	config := quotationConfigFixture()
	config.Prices[0].OutputUnitsPer1K = "4000"
	if err := book.Replace(config); err != nil {
		t.Fatal(err)
	}
	book.reader = func(ctx context.Context, version string) (string, error) {
		*in.Usage.Counts.InputTokens = -1
		*in.Usage.Breakdown.CachedInputTokens = -1
		return history.read(ctx, version)
	}
	quote, err := book.Calculate(context.Background(), in)
	if err != nil || quote.AmountUnits != "22" {
		t.Fatal("history callback mutated the validated observation", quote, err)
	}
}

func TestQuotationConcurrentPublicationAndHistoricalRead(t *testing.T) {
	book, _ := quotationFixture(t)
	in := quotationInputFixture(t, book)
	var group sync.WaitGroup
	errorsSeen := make(chan error, 20)
	for i := 0; i < 20; i++ {
		group.Add(1)
		go func(n int) {
			defer group.Done()
			config := quotationConfigFixture()
			config.Prices[0].OutputUnitsPer1K = fmt.Sprint(3000 + n)
			if err := book.Replace(config); err != nil {
				errorsSeen <- err
				return
			}
			quote, err := book.Calculate(context.Background(), in)
			if err != nil || quote.AmountUnits != "22" {
				errorsSeen <- fmt.Errorf("pinned quote %+v: %v", quote, err)
			}
		}(i)
	}
	group.Wait()
	close(errorsSeen)
	for err := range errorsSeen {
		t.Error(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := book.Current(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := book.Calculate(ctx, in); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
