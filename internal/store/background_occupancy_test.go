package store

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"minigateway/internal/config"
	"minigateway/internal/model"
)

func occupancyJob(n int, stage string) model.BackgroundJob {
	return model.BackgroundJob{ID: fmt.Sprintf("%064x", n), PublicID: fmt.Sprintf("resp_%064x", n), UserID: 1, TokenID: uint(n), Operation: "responses", Model: "private-model", Stage: stage, Ciphertext: []byte("invalid-private-input"), Snapshot: []byte("invalid-private-snapshot"), EventCount: 2, StoredBytes: 17, ReservedBytes: 100}
}

func TestBackgroundOccupancyReadsPersistedMetadataWithoutContent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "occupancy.db")
	st, err := Open(config.DatabaseConfig{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	ctx := context.Background()
	if got, err := st.BackgroundOccupancy(ctx); err != nil || got != (BackgroundOccupancy{}) {
		t.Fatal(got, err)
	}
	for i, stage := range []string{"queued", "dispatching", "remote_active", "terminal", "uncertain", "future-private-stage"} {
		row := occupancyJob(i+1, stage)
		row.ReplayPending = stage == "terminal"
		row.Deleted = stage == "terminal"
		if err := st.DB().Create(&row).Error; err != nil {
			t.Fatal(err)
		}
	}
	want := BackgroundOccupancy{Queued: 1, Dispatching: 1, RemoteActive: 1, Terminal: 1, Uncertain: 1, Other: 1, ReplayPending: 1, Events: 12, EventBytes: 102, ReservedBytes: 600}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	st, err = Open(config.DatabaseConfig{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	if got, err := st.BackgroundOccupancy(ctx); err != nil || got != want {
		t.Fatal("reopen lost metadata occupancy", got, err)
	}
	// A metadata-only view makes any attempt to read blobs or identities fail.
	for _, sql := range []string{
		"ALTER TABLE background_jobs RENAME TO private_background_jobs",
		"CREATE VIEW background_jobs AS SELECT stage, replay_pending, event_count, stored_bytes, reserved_bytes FROM private_background_jobs",
	} {
		if err := st.DB().Exec(sql).Error; err != nil {
			t.Fatal(err)
		}
	}
	if got, err := st.BackgroundOccupancy(ctx); err != nil || got != want {
		t.Fatal("occupancy depends on private columns", got, err)
	}
}

func TestBackgroundOccupancyRejectsInvalidMetadataAndCancelledRead(t *testing.T) {
	st := openTestStore(t, "occupancy-invalid")
	defer st.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := st.BackgroundOccupancy(ctx); err == nil {
		t.Fatal("cancelled read succeeded")
	}
	row := occupancyJob(1, "queued")
	if err := st.DB().Create(&row).Error; err != nil {
		t.Fatal(err)
	}
	for _, column := range []string{"event_count", "stored_bytes", "reserved_bytes"} {
		if err := st.DB().Model(&row).Update(column, -1).Error; err != nil {
			t.Fatal(err)
		}
		if _, err := st.BackgroundOccupancy(context.Background()); err == nil {
			t.Fatal("negative metadata reported as occupancy", column)
		}
		if err := st.DB().Model(&row).Update(column, 0).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := st.BackgroundOccupancy(context.Background()); err == nil {
		t.Fatal("closed database fabricated zero occupancy")
	}
}

func TestBackgroundOccupancyDetectsRecordBoundWithoutTruncatingCounts(t *testing.T) {
	st := openTestStore(t, "occupancy-bound")
	defer st.Close()
	rows := make([]model.BackgroundJob, 4096)
	for i := range rows {
		rows[i] = occupancyJob(i+1, "queued")
	}
	if err := st.DB().CreateInBatches(rows, 128).Error; err != nil {
		t.Fatal(err)
	}
	if got, err := st.BackgroundOccupancy(context.Background()); err != nil || got.Queued != 4096 || got.Events != 8192 {
		t.Fatal("valid record boundary rejected", got, err)
	}
	extra := occupancyJob(4097, "queued")
	if err := st.DB().Create(&extra).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := st.BackgroundOccupancy(context.Background()); err == nil {
		t.Fatal("over-limit database silently truncated occupancy")
	}
}

func TestBackgroundOccupancyRetentionClearsEventCountWithDeletedEvents(t *testing.T) {
	st := openTestStore(t, "occupancy-retention")
	defer st.Close()
	row := occupancyJob(1, "terminal")
	row.ReplayPending = true
	row.ContentExpiresAt = 1
	row.ExpiresAt = time.Now().Add(time.Hour).UnixMilli()
	if err := st.DB().Create(&row).Error; err != nil {
		t.Fatal(err)
	}
	for i := int64(0); i < row.EventCount; i++ {
		if err := st.DB().Create(&model.BackgroundEvent{TaskID: row.ID, Sequence: i, Ciphertext: []byte("private-event")}).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := st.CleanupBackground(context.Background(), 200); err != nil {
		t.Fatal(err)
	}
	var retained int64
	if err := st.DB().Model(&model.BackgroundEvent{}).Count(&retained).Error; err != nil || retained != 0 {
		t.Fatal("events were not cleaned", retained, err)
	}
	if got, err := st.BackgroundOccupancy(context.Background()); err != nil || got != (BackgroundOccupancy{Terminal: 1}) {
		t.Fatal("deleted event metadata still occupies resources", got, err)
	}
}
