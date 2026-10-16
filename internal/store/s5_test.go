package store

import (
	"crypto/sha256"
	"encoding/hex"
	"math"
	"testing"
	"time"

	"minigateway/internal/model"
)

func TestS5PriceHistoryIsImmutableAndSurvivesLogRetention(t *testing.T) {
	st := openTestStore(t, "price-history")
	defer st.Close()
	payload := `{"format":1,"unit_scale":1000000000,"prices":[],"rate_units":{}}`
	sum := sha256.Sum256([]byte(payload))
	version := hex.EncodeToString(sum[:])
	for range 2 {
		if err := st.RecordPriceSnapshot(version, payload); err != nil {
			t.Fatal(err)
		}
	}
	var count int64
	if err := st.DB().Model(&model.BillingPriceVersion{}).Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("duplicate version: count=%d err=%v", count, err)
	}
	if err := st.RecordPriceSnapshot(version, payload+" "); err == nil {
		t.Fatal("accepted a mismatched digest")
	}
	// Detect corrupt existing history rather than silently overwriting it.
	if err := st.DB().Model(&model.BillingPriceVersion{}).Where("version=?", version).Update("payload", "corrupt").Error; err != nil {
		t.Fatal(err)
	}
	if err := st.RecordPriceSnapshot(version, payload); err == nil {
		t.Fatal("overwrote immutable history")
	}
	if err := st.DB().Create(&model.RequestLog{PriceVersion: version, CreatedAt: time.Now().AddDate(0, 0, -40)}).Error; err != nil {
		t.Fatal(err)
	}
	if n, err := st.PurgeOnce(30); err != nil || n != 1 {
		t.Fatalf("retention failed: n=%d err=%v", n, err)
	}
	if err := st.DB().Model(&model.BillingPriceVersion{}).Count(&count).Error; err != nil || count != 1 {
		t.Fatal("ordinary log retention deleted price history")
	}
}

func TestS5InvalidPricesAreRejectedBeforePersistence(t *testing.T) {
	st := openTestStore(t, "invalid-price")
	defer st.Close()
	for _, value := range []float64{-1, math.NaN(), math.Inf(1), 1_000_000_001} {
		if err := st.UpsertModelPrice(&model.ModelPrice{Model: "m", InputPricePer1K: value}); err == nil {
			t.Fatalf("invalid price saved: %v", value)
		}
		if err := st.SetGroupRates(map[string]float64{"g": value}); err == nil {
			t.Fatalf("invalid rate saved: %v", value)
		}
	}
	if err := st.SetGroupRates(map[string]float64{"": 1}); err == nil {
		t.Fatal("empty group saved")
	}
}
