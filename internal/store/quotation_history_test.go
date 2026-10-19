package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"
	"minigateway/internal/billing"
	execution "minigateway/internal/foundation/contracts/execution/v1"
	q "minigateway/internal/foundation/contracts/quotation/v1"
	"minigateway/internal/model"
)

func quotationHistoryVersion(payload string) string {
	digest := sha256.Sum256([]byte(payload))
	return hex.EncodeToString(digest[:])
}

func TestQuotationHistoryValidatesVersionAndCancellation(t *testing.T) {
	st := openTestStore(t, "quotation-read")
	defer st.Close()
	ctx := context.Background()
	for _, version := range []string{"", "latest", strings.Repeat("a", 63), strings.Repeat("a", 65), strings.Repeat("A", 64), strings.Repeat("g", 64), " " + strings.Repeat("a", 64)} {
		if payload, err := st.QuotationHistory(ctx, version); !errors.Is(err, q.ErrInvalid) || payload != "" {
			t.Fatalf("noncanonical version %q: payload=%q error=%v", version, payload, err)
		}
	}
	if payload, err := st.QuotationHistory(ctx, strings.Repeat("f", 64)); !errors.Is(err, q.ErrNotFound) || payload != "" {
		t.Fatal("missing version was not distinguished", payload, err)
	}
	// Storage deliberately does not interpret JSON, profile or document kind.
	body := "raw metadata: <&>\x00\u6570\u91cf"
	version := quotationHistoryVersion(body)
	if err := st.RecordPriceSnapshot(version, body); err != nil {
		t.Fatal(err)
	}
	if payload, err := st.QuotationHistory(ctx, version); err != nil || payload != body {
		t.Fatal("history altered exact body bytes", payload, err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if payload, err := st.QuotationHistory(canceled, version); !errors.Is(err, context.Canceled) || payload != "" {
		t.Fatal("canceled read returned history", payload, err)
	}
	expired, stop := context.WithDeadline(ctx, time.Now().Add(-time.Second))
	defer stop()
	if _, err := st.QuotationHistory(expired, version); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	if _, err := st.QuotationHistory(nil, version); !errors.Is(err, q.ErrInvalid) {
		t.Fatal(err)
	}
}

func TestQuotationHistoryBoundsPayloadBytesIncludingUnicode(t *testing.T) {
	st := openTestStore(t, "quotation-byte-bounds")
	defer st.Close()
	const limit = 256 << 10
	type projection struct {
		payload string
		size    int64
	}
	observed := make(chan projection, 1)
	// Inspect the real query result before the reader's secondary len check:
	// oversized Unicode must be suppressed by SQL rather than loaded in full.
	if err := st.DB().Callback().Query().After("gorm:query").Register("test:quotation-byte-projection", func(tx *gorm.DB) {
		if row, ok := tx.Statement.Dest.(*struct {
			Payload string
			Size    int64
		}); ok {
			observed <- projection{payload: row.Payload, size: row.Size}
		}
	}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, body string
		valid      bool
	}{
		{"ascii exact", strings.Repeat("a", limit), true},
		{"ascii oversized", strings.Repeat("a", limit+1), false},
		{"unicode exact", strings.Repeat("\u754c", limit/3) + "x", true},
		{"unicode oversized with fewer characters", strings.Repeat("\u754c", limit/3+1), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			version := quotationHistoryVersion(tc.body)
			if err := st.RecordPriceSnapshot(version, tc.body); err != nil {
				t.Fatal(err)
			}
			payload, err := st.QuotationHistory(context.Background(), version)
			var projected projection
			select {
			case projected = <-observed:
			default:
				t.Fatal("history query projection was not observed")
			}
			if projected.size != int64(len(tc.body)) || !tc.valid && projected.payload != "" {
				t.Fatal("SQL measured characters or loaded an oversized body", projected.size, len(projected.payload))
			}
			if tc.valid {
				if err != nil || payload != tc.body || len(payload) != limit {
					t.Fatal("exact byte bound rejected", len(payload), err)
				}
			} else if !errors.Is(err, q.ErrInvalid) || payload != "" {
				t.Fatal("oversized body escaped byte-bound reader", len(payload), err)
			}
		})
	}
}

func TestQuotationHistoryRejectsEmptyAndCorruptDigestWithoutMutatingRows(t *testing.T) {
	st := openTestStore(t, "quotation-corruption")
	defer st.Close()
	for _, row := range []model.BillingPriceVersion{
		{Version: quotationHistoryVersion(""), Payload: ""},
		{Version: quotationHistoryVersion("expected immutable body"), Payload: "corrupt stored body"},
	} {
		if err := st.DB().Create(&row).Error; err != nil {
			t.Fatal(err)
		}
		if payload, err := st.QuotationHistory(context.Background(), row.Version); !errors.Is(err, q.ErrInvalid) || payload != "" {
			t.Fatal("corrupt history was exposed", payload, err)
		}
		var after model.BillingPriceVersion
		if err := st.DB().First(&after, "version = ?", row.Version).Error; err != nil || after.Payload != row.Payload {
			t.Fatal("read repaired or rewrote immutable history", after, err)
		}
	}
	if err := st.RecordPriceSnapshot(quotationHistoryVersion("expected immutable body"), "expected immutable body"); err == nil {
		t.Fatal("append overwrote a conflicting historical row")
	}
}

func TestQuotationHistoryCoexistsWithPinnedLegacyPriceAndStrictRecovery(t *testing.T) {
	st := openTestStore(t, "quotation-legacy-history")
	defer st.Close()
	ctx := context.Background()
	if err := st.UpsertModelPrice(&model.ModelPrice{Model: "m", InputPricePer1K: 0.1, OutputPricePer1K: 0.2}); err != nil {
		t.Fatal(err)
	}
	table := billing.NewPriceTableWithHistory(st.PriceData, time.Hour, st.RecordPriceSnapshot)
	if err := table.ReloadPrices(); err != nil {
		t.Fatal(err)
	}
	old := table.Snapshot()
	oldBody, err := st.QuotationHistory(ctx, old.Version())
	if err != nil || !strings.Contains(oldBody, `"format":1`) {
		t.Fatal(oldBody, err)
	}
	if err := st.RecordPriceSnapshot(old.Version(), oldBody); err != nil {
		t.Fatal("identical append was not idempotent", err)
	}
	if err := st.RecordPriceSnapshot(old.Version(), oldBody+" "); err == nil {
		t.Fatal("digest/content conflict was accepted")
	}
	var count int64
	if err := st.DB().Model(&model.BillingPriceVersion{}).Where("version = ?", old.Version()).Count(&count).Error; err != nil || count != 1 {
		t.Fatal(count, err)
	}

	user := model.User{Username: "quotation-history-owner", Status: model.UserStatusActive, Quota: 10}
	admin := model.User{Username: "quotation-history-admin", Status: model.UserStatusActive, Role: model.RoleAdmin}
	if err := st.DB().Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	if err := st.DB().Create(&admin).Error; err != nil {
		t.Fatal(err)
	}
	key := model.Token{UserID: user.ID, Quota: 10}
	if err := st.DB().Create(&key).Error; err != nil {
		t.Fatal(err)
	}
	engine := billing.New(st.DB(), table)
	if err := engine.EnableStrict("1", time.Minute); err != nil {
		t.Fatal(err)
	}
	pre, err := engine.Reserve(ctx, key.ID, user.ID, "m")
	if err != nil {
		t.Fatal(err)
	}
	book := billing.NewQuotationBook(st.RecordPriceSnapshot, st.QuotationHistory)
	if err := book.Replace(billing.QuotationConfig{Currency: "account-unit", Prices: []billing.QuotationPrice{{Model: "m", UncachedInputUnitsPer1K: "1000000000000000000", CachedInputUnitsPer1K: "0", CacheWriteInputUnitsPer1K: "0", OutputUnitsPer1K: "1000000000000000000"}}, GroupRateUnits: map[string]string{"default": "1000000000"}}); err != nil {
		t.Fatal(err)
	}
	quoteVersion, err := book.Current(ctx)
	if err != nil || !quoteVersion.Enabled || quoteVersion.Version == old.Version() {
		t.Fatal(quoteVersion, err)
	}
	quoteBody, err := st.QuotationHistory(ctx, quoteVersion.Version)
	if err != nil || !strings.Contains(quoteBody, `"kind":"dimensional_quote"`) {
		t.Fatal(quoteBody, err)
	}
	if table.Snapshot().Version() != old.Version() {
		t.Fatal("quotation publication replaced the legacy price table")
	}
	if err := st.UpsertModelPrice(&model.ModelPrice{Model: "m", InputPricePer1K: 0.3, OutputPricePer1K: 0.4}); err != nil {
		t.Fatal(err)
	}
	if err := table.ReloadPrices(); err != nil {
		t.Fatal(err)
	}
	latest := table.Snapshot()
	usage := &execution.Usage{PromptTokens: 1000, CompletionTokens: 1000, TotalTokens: 2000}
	oldCost, oldErr := old.CostUnits(usage, "m", "default")
	newCost, newErr := latest.CostUnits(usage, "m", "default")
	if oldErr != nil || newErr != nil || oldCost != 300000000 || newCost != 700000000 || latest.Version() == old.Version() || latest.Version() == quoteVersion.Version {
		t.Fatal("legacy reload or pinned snapshot was changed", oldCost, newCost, oldErr, newErr)
	}
	markPending := func(id, version string) {
		t.Helper()
		if err := st.DB().Create(&model.BillingAttempt{ReservationID: id, Number: 1, ActualModel: "m", Group: "default", State: billing.Pending}).Error; err != nil {
			t.Fatal(err)
		}
		if err := st.DB().Model(&model.BillingReservation{}).Where("id = ?", id).Updates(map[string]any{"state": billing.Pending, "lease_until": time.Now().Add(-time.Second).UnixMilli(), "price_version": version}).Error; err != nil {
			t.Fatal(err)
		}
	}
	markPending(pre.BillingID(), old.Version())
	inputTokens, outputTokens := 1000, 1000
	decision := billing.ResolutionInput{Action: billing.ResolveUsage, Reason: "verified fixture counts", Evidence: "historical version regression", Usage: &billing.ResolutionUsage{PromptTokens: &inputTokens, OutputTokens: &outputTokens}}
	resolved, err := engine.ResolveReservation(ctx, pre.BillingID(), admin.ID, decision)
	if err != nil || resolved.ChargeUnits != "300000000" {
		t.Fatal("strict recovery repriced the old reservation", resolved, err)
	}
	detail, err := engine.ReservationDetail(ctx, pre.BillingID())
	if err != nil || detail.Reservation.PriceVersion != old.Version() || detail.PriceSnapshot != oldBody {
		t.Fatal("old price version or exact body changed", detail, err)
	}

	// Even a corrupted reference cannot make the strict reader interpret a
	// quotation document as a legacy settlement schedule.
	invalidPre, err := engine.Reserve(ctx, key.ID, user.ID, "m")
	if err != nil {
		t.Fatal(err)
	}
	markPending(invalidPre.BillingID(), quoteVersion.Version)
	if _, err := engine.ResolveReservation(ctx, invalidPre.BillingID(), admin.ID, decision); err == nil {
		t.Fatal("strict recovery accepted the new quote kind")
	}
	if err := st.DB().Model(&model.BillingResolution{}).Where("reservation_id = ?", invalidPre.BillingID()).Count(&count).Error; err != nil || count != 0 {
		t.Fatal("invalid recovery committed a decision", count, err)
	}
	var retained model.BillingReservation
	if err := st.DB().First(&retained, "id = ?", invalidPre.BillingID()).Error; err != nil || retained.State != billing.Pending {
		t.Fatal("invalid history consumed the pending reservation", retained, err)
	}
	if after, err := st.QuotationHistory(ctx, old.Version()); err != nil || after != oldBody {
		t.Fatal("legacy history was rewritten", after, err)
	}
}
