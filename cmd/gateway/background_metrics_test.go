package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"minigateway/internal/config"
	"minigateway/internal/metrics"
	"minigateway/internal/model"
	"minigateway/internal/store"
)

func backgroundMetricsText(t *testing.T, registry *metrics.Registry) string {
	t.Helper()
	var out strings.Builder
	if err := registry.Render(&out); err != nil {
		t.Fatal(err)
	}
	return out.String()
}

func metricsBackgroundJob() model.BackgroundJob {
	return model.BackgroundJob{ID: strings.Repeat("a", 64), PublicID: "resp_" + strings.Repeat("b", 64), Stage: "terminal", Revision: 1, Ciphertext: []byte("invalid-private-input"), Snapshot: []byte("invalid-private-snapshot"), EventCount: 2, StoredBytes: 20, ReservedBytes: 100, ContentExpiresAt: time.Now().Add(time.Hour).UnixMilli(), ExpiresAt: time.Now().Add(2 * time.Hour).UnixMilli()}
}

func TestBackgroundMetricsCachePreservesUnknownAndStaleCountsWithoutScrapeIO(t *testing.T) {
	st, err := store.Open(config.DatabaseConfig{Path: filepath.Join(t.TempDir(), "cached.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	r := &responsesRuntime{}
	registry := metrics.New()
	registerBackgroundMetrics(registry, r)
	if out := backgroundMetricsText(t, registry); strings.Contains(out, "gateway_background_jobs") {
		t.Fatal("initial unknown durable state became zero", out)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	r.sampleBackgroundOccupancy(cancelled, st)
	if out := backgroundMetricsText(t, registry); strings.Contains(out, "gateway_background_jobs") || !strings.Contains(out, "gateway_background_snapshot_success 0\n") {
		t.Fatal("initial failure fabricated counts", out)
	}
	row := metricsBackgroundJob()
	if err := st.DB().Create(&row).Error; err != nil {
		t.Fatal(err)
	}
	r.sampleBackgroundOccupancy(context.Background(), st)
	if err := st.DB().Model(&row).Updates(map[string]any{"event_count": 5, "stored_bytes": 50}).Error; err != nil {
		t.Fatal(err)
	}
	if out := backgroundMetricsText(t, registry); !strings.Contains(out, "gateway_background_enabled 0\n") || !strings.Contains(out, "gateway_background_events_retained 2\n") {
		t.Fatal("scrape queried DB or hidden feature-off metadata", out)
	}
	r.sampleBackgroundOccupancy(context.Background(), st)
	before := backgroundMetricsText(t, registry)
	if !strings.Contains(before, "gateway_background_events_retained 5\n") || !strings.Contains(before, "gateway_background_snapshot_success 1\n") || r.backgroundOccupancy.Load().UnixTime <= 0 {
		t.Fatal("sample did not publish current metadata", before)
	}
	lastSuccess := r.backgroundOccupancy.Load().UnixTime
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	if after := backgroundMetricsText(t, registry); after != before {
		t.Fatal("scraping touched a closed database", after)
	}
	r.sampleBackgroundOccupancy(context.Background(), st)
	if out := backgroundMetricsText(t, registry); !strings.Contains(out, "gateway_background_snapshot_success 0\n") || !strings.Contains(out, "gateway_background_events_retained 5\n") || r.backgroundOccupancy.Load().UnixTime != lastSuccess {
		t.Fatal("failed sample lost the last successful observation", out)
	}
}

func TestBackgroundOccupancyMaintenanceStillSamplesWhenResponsesDisabled(t *testing.T) {
	st, err := store.Open(config.DatabaseConfig{Path: filepath.Join(t.TempDir(), "disabled.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	row := metricsBackgroundJob()
	if err := st.DB().Create(&row).Error; err != nil {
		t.Fatal(err)
	}
	r, err := startResponsesRuntime(config.ResponsesConfig{}, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	registry := metrics.New()
	registerBackgroundMetrics(registry, r)
	for _, count := range []int64{2, 7} {
		if count == 7 {
			if err := st.DB().Model(&row).Update("event_count", count).Error; err != nil {
				t.Fatal(err)
			}
		}
		deadline := time.Now().Add(5 * time.Second)
		for {
			if sample := r.backgroundOccupancy.Load(); sample != nil && sample.Success && sample.Occupancy.Events == count {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("existing maintenance loop did not sample retained metadata", count)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	if out := backgroundMetricsText(t, registry); !strings.Contains(out, "gateway_background_enabled 0\n") || !strings.Contains(out, `gateway_background_jobs{stage="terminal"} 1`) {
		t.Fatal("disabled background runtime concealed retained state", out)
	}
}
