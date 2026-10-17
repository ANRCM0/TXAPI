package store

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"minigateway/internal/access"
	"minigateway/internal/model"
)

func archiveRecord(id int, owner uint) model.GenerationRecord {
	now := time.Now()
	return model.GenerationRecord{ID: fmt.Sprintf("%064x", id), UserID: owner, TokenID: 1, State: "open", MaxBytes: 16384,
		CreatedAt: now.UnixMilli(), ExpiresAt: now.Add(time.Hour).UnixMilli()}
}

func archiveLimits() model.GenerationArchiveLimits {
	return model.GenerationArchiveLimits{InstanceBytes: 65536, OwnerBytes: 32768, InstanceRecords: 4, OwnerRecords: 2}
}

func TestGenerationArchiveQuotaReservationSerializesConcurrentCreates(t *testing.T) {
	s := openTestStore(t, "archive-quota")
	defer s.Close()
	var wg sync.WaitGroup
	var accepted atomic.Int64
	for i := range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := s.CreateGenerationRecord(context.Background(), archiveRecord(i+1, 7), archiveLimits())
			if err == nil {
				accepted.Add(1)
			} else if !errors.Is(err, ErrArchiveCapacity) {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if accepted.Load() != 2 {
		t.Fatalf("owner capacity bypassed: %d", accepted.Load())
	}
	for i := 21; i < 23; i++ {
		if err := s.CreateGenerationRecord(context.Background(), archiveRecord(i, uint(i)), archiveLimits()); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.CreateGenerationRecord(context.Background(), archiveRecord(24, 24), archiveLimits()); !errors.Is(err, ErrArchiveCapacity) {
		t.Fatal(err)
	}
}

func TestGenerationArchiveAppendRollbackAndTerminalFencing(t *testing.T) {
	s := openTestStore(t, "archive-append")
	defer s.Close()
	ctx := context.Background()
	r := archiveRecord(1, 3)
	if err := s.CreateGenerationRecord(ctx, r, archiveLimits()); err != nil {
		t.Fatal(err)
	}
	seg := model.GenerationSegment{RecordID: r.ID, Kind: "metadata", Ciphertext: make([]byte, 40)}
	if err := s.DB().Exec("CREATE TRIGGER archive_fail BEFORE INSERT ON generation_segments BEGIN SELECT RAISE(ABORT, 'controlled archive write failure'); END").Error; err != nil {
		t.Fatal(err)
	}
	if err := s.AppendGenerationSegment(ctx, r, seg); err == nil {
		t.Fatal("write failure ignored")
	}
	var check model.GenerationRecord
	if err := s.DB().First(&check, "id = ?", r.ID).Error; err != nil {
		t.Fatal(err)
	}
	if check.NextSequence != 0 || check.StoredBytes != 0 || check.Committed {
		t.Fatalf("partial append survived: %+v", check)
	}
	if err := s.DB().Exec("DROP TRIGGER archive_fail").Error; err != nil {
		t.Fatal(err)
	}
	if err := s.AppendGenerationSegment(ctx, r, seg); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendGenerationSegment(ctx, r, seg); !errors.Is(err, ErrArchiveConflict) {
		t.Fatalf("stale append: %v", err)
	}
	r.NextSequence, seg.Sequence, seg.Kind = 1, 1, "result"
	wrong := r
	wrong.TokenID++
	if err := s.AppendGenerationSegment(ctx, wrong, seg); !errors.Is(err, ErrArchiveConflict) {
		t.Fatalf("wrong binding: %v", err)
	}
	if err := s.AppendGenerationSegment(ctx, r, seg); err != nil {
		t.Fatal(err)
	}
	r.NextSequence, seg.Sequence = 2, 2
	if err := s.AppendGenerationSegment(ctx, r, seg); !errors.Is(err, ErrArchiveConflict) {
		t.Fatalf("closed writer: %v", err)
	}
	got, err := s.ReadGenerationRecord(ctx, access.Binding{UserID: 3, TokenID: 1}, r.ID)
	if err != nil || len(got.Segments) != 2 || got.State != "sealed" || got.StoredBytes != 80 {
		t.Fatalf("snapshot: %+v %v", got, err)
	}
	if _, err := s.ReadGenerationRecord(ctx, access.Binding{UserID: 3, TokenID: 2}, r.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	if err := s.DeleteGenerationRecord(ctx, access.Binding{UserID: 3, TokenID: 2}, r.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	if err := s.DeleteGenerationRecord(ctx, access.Binding{UserID: 3, TokenID: 1}, r.ID); err != nil {
		t.Fatal(err)
	}
	var n int64
	if err := s.DB().Model(&model.GenerationSegment{}).Count(&n).Error; err != nil || n != 0 {
		t.Fatalf("orphan ciphertext: %d %v", n, err)
	}
}

func TestGenerationArchiveRecoveryExpiryAndBoundedCleanup(t *testing.T) {
	s := openTestStore(t, "archive-recovery")
	defer s.Close()
	ctx := context.Background()
	for i := 1; i <= 3; i++ {
		r := archiveRecord(i, uint(i))
		if err := s.CreateGenerationRecord(ctx, r, archiveLimits()); err != nil {
			t.Fatal(err)
		}
		if err := s.AppendGenerationSegment(ctx, r, model.GenerationSegment{RecordID: r.ID, Kind: "metadata", Ciphertext: make([]byte, 40)}); err != nil {
			t.Fatal(err)
		}
	}
	for _, want := range []int64{2, 1, 0} {
		if n, err := s.InterruptGenerationRecords(ctx, 2); err != nil || n != want {
			t.Fatalf("recovery: %d %v", n, err)
		}
	}
	r := archiveRecord(1, 1)
	r.NextSequence = 1
	if err := s.AppendGenerationSegment(ctx, r, model.GenerationSegment{RecordID: r.ID, Sequence: 1, Kind: "body", Ciphertext: make([]byte, 40)}); !errors.Is(err, ErrArchiveConflict) {
		t.Fatalf("interrupted writer revived: %v", err)
	}
	if err := s.DB().Model(&model.GenerationRecord{}).Where("1=1").Update("expires_at", time.Now().Add(-time.Hour).UnixMilli()).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReadGenerationRecord(ctx, access.Binding{UserID: 1, TokenID: 1}, r.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	for _, want := range []int64{2, 1, 0} {
		if n, err := s.CleanupGenerationRecords(ctx, 2); err != nil || n != want {
			t.Fatalf("cleanup: %d %v", n, err)
		}
	}
	var n int64
	s.DB().Model(&model.GenerationSegment{}).Count(&n)
	if n != 0 {
		t.Fatalf("cleanup left ciphertext: %d", n)
	}
	if err := s.CreateGenerationRecord(ctx, archiveRecord(4, 4), archiveLimits()); err != nil {
		t.Fatalf("quota not reclaimed: %v", err)
	}
}

func TestGenerationArchiveRefusesOversizedCorruptBlobBeforeLoading(t *testing.T) {
	s := openTestStore(t, "archive-corrupt")
	defer s.Close()
	r := archiveRecord(1, 1)
	if err := s.CreateGenerationRecord(context.Background(), r, archiveLimits()); err != nil {
		t.Fatal(err)
	}
	if err := s.DB().Exec("INSERT INTO generation_segments(record_id,sequence,kind,ciphertext) VALUES (?,0,'result',zeroblob(70000))", r.ID).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReadGenerationRecord(context.Background(), access.Binding{UserID: 1, TokenID: 1}, r.ID); !errors.Is(err, ErrArchiveConflict) {
		t.Fatalf("unbounded blob accepted: %v", err)
	}
}

func TestGenerationArchiveSegmentCapReservesFinalSlotAndBoundsCorruptReads(t *testing.T) {
	s := openTestStore(t, "archive-segment-cap")
	defer s.Close()
	ctx := context.Background()
	r := archiveRecord(1, 1)
	r.MaxBytes = 1 << 20
	limits := model.GenerationArchiveLimits{InstanceBytes: 2 << 20, OwnerBytes: 1 << 20, InstanceRecords: 2, OwnerRecords: 1}
	if err := s.CreateGenerationRecord(ctx, r, limits); err != nil {
		t.Fatal(err)
	}
	segments := make([]model.GenerationSegment, model.GenerationArchiveMaxSegments-1)
	for i := range segments {
		kind := "body"
		if i == 0 {
			kind = "metadata"
		}
		segments[i] = model.GenerationSegment{RecordID: r.ID, Sequence: uint64(i), Kind: kind, Ciphertext: make([]byte, 30)}
	}
	// Populate a consistent near-cap store snapshot without thousands of
	// transactions; existing tests cover per-append CAS and rollback behavior.
	if err := s.DB().CreateInBatches(&segments, 200).Error; err != nil {
		t.Fatal(err)
	}
	r.NextSequence = uint64(len(segments))
	r.StoredBytes = int64(len(segments) * 30)
	r.Committed = true
	if err := s.DB().Model(&model.GenerationRecord{}).Where("id = ?", r.ID).Updates(map[string]any{"next_sequence": r.NextSequence, "stored_bytes": r.StoredBytes, "committed": true}).Error; err != nil {
		t.Fatal(err)
	}
	last := model.GenerationSegment{RecordID: r.ID, Sequence: r.NextSequence, Kind: "body", Ciphertext: make([]byte, 30)}
	if err := s.AppendGenerationSegment(ctx, r, last); !errors.Is(err, ErrArchiveConflict) {
		t.Fatalf("body consumed reserved final slot: %v", err)
	}
	last.Kind = "result"
	if err := s.AppendGenerationSegment(ctx, r, last); err != nil {
		t.Fatal(err)
	}
	binding := access.Binding{UserID: 1, TokenID: 1}
	got, err := s.ReadGenerationRecord(ctx, binding, r.ID)
	if err != nil || got.State != "sealed" || len(got.Segments) != model.GenerationArchiveMaxSegments {
		t.Fatalf("exact cap could not be read: segments=%d err=%v", len(got.Segments), err)
	}
	last.Sequence = model.GenerationArchiveMaxSegments
	r.NextSequence = last.Sequence
	if err := s.AppendGenerationSegment(ctx, r, last); !errors.Is(err, ErrArchiveConflict) {
		t.Fatalf("append exceeded finite cap: %v", err)
	}
	if err := s.DB().Model(&model.GenerationRecord{}).Where("id = ?", r.ID).Update("next_sequence", model.GenerationArchiveMaxSegments+1).Error; err != nil {
		t.Fatal(err)
	}
	got, err = s.ReadGenerationRecord(ctx, binding, r.ID)
	if !errors.Is(err, ErrArchiveConflict) || len(got.Segments) != 0 {
		t.Fatal("malformed row-count metadata loaded ciphertext beyond cap")
	}
	if err := s.DB().Model(&model.GenerationRecord{}).Where("id = ?", r.ID).Update("next_sequence", model.GenerationArchiveMaxSegments).Error; err != nil {
		t.Fatal(err)
	}
	extra := model.GenerationSegment{RecordID: r.ID, Sequence: model.GenerationArchiveMaxSegments, Kind: "body", Ciphertext: make([]byte, 30)}
	if err := s.DB().Create(&extra).Error; err != nil {
		t.Fatal(err)
	}
	got, err = s.ReadGenerationRecord(ctx, binding, r.ID)
	if !errors.Is(err, ErrArchiveConflict) || len(got.Segments) != 0 {
		t.Fatal("extra corrupted ciphertext row bypassed pre-load accounting")
	}
}
