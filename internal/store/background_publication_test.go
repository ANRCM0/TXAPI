package store

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"
	bg "minigateway/internal/foundation/contracts/background/v1"
	identity "minigateway/internal/foundation/contracts/identity/v1"
	state "minigateway/internal/foundation/contracts/state/v1"
	task "minigateway/internal/foundation/contracts/taskcontrol/v1"
	bs "minigateway/internal/generation/backgroundstate"
	"minigateway/internal/model"
)

func backgroundPublicationFixture(t *testing.T) (*Store, identity.Binding, bs.Record, bs.Limits) {
	t.Helper()
	s, binding, in, parent, limits := backgroundHistoryFixture(t)
	if err := s.DB().Delete(&parent).Error; err != nil {
		t.Fatal(err)
	}
	in.Store = true
	in.HistoryReservation = &bs.HistoryReservation{
		Row:    state.ResponseHistory{ID: in.PublicID, UserID: in.UserID, TokenID: in.TokenID, Kind: "response", Model: in.Model, Version: 1, Busy: bs.HistoryBusy(in.ID), CreatedAt: in.CreatedAt, ExpiresAt: in.ContentExpiresAt, ReservedBytes: model.ResponseHistoryMaxBytes, Ciphertext: []byte(strings.Repeat("p", 40))},
		Limits: state.HistoryLimits{InstanceRecords: 4, OwnerRecords: 4, InstanceBytes: 4 * model.ResponseHistoryMaxBytes, OwnerBytes: 4 * model.ResponseHistoryMaxBytes},
	}
	return s, binding, in, limits
}

func backgroundPublicationClaim(t *testing.T, s *Store, in bs.Record, limits bs.Limits) (bs.Record, bs.Claim) {
	t.Helper()
	ctx := context.Background()
	if _, err := s.AdmitBackground(ctx, in, limits); err != nil {
		t.Fatal(err)
	}
	row, claim, err := s.ClaimBackground(ctx, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	row, err = s.UpdateBackground(ctx, claim, row.Revision, bs.Update{Stage: bg.StageDispatching, Snapshot: bytes.Repeat([]byte{'d'}, 40)})
	if err != nil {
		t.Fatal(err)
	}
	return row, claim
}

func backgroundPublicationChange(row bs.Record) state.HistoryChange {
	now := time.Now().UnixMilli()
	return state.HistoryChange{Expected: 1, Record: state.ResponseHistory{ID: row.PublicID, UserID: row.UserID, TokenID: row.TokenID, Kind: "response", Model: row.Model, Version: 2, CreatedAt: now, ExpiresAt: now + int64(2*time.Hour/time.Millisecond), ReservedBytes: 40, Ciphertext: bytes.Repeat([]byte{'r'}, 40)}}
}

func backgroundPublicationUpdate(row bs.Record) bs.Update {
	change := backgroundPublicationChange(row)
	return bs.Update{Stage: bg.StageTerminal, Snapshot: bytes.Repeat([]byte{'t'}, 40), History: &change, UsageMetadata: &bs.UsageMetadata{NativeState: bg.RemoteSucceeded}}
}

func TestBackgroundPublicationAdmissionCapacityAndRollback(t *testing.T) {
	for _, failure := range []string{"capacity", "history-insert"} {
		t.Run(failure, func(t *testing.T) {
			s, _, in, limits := backgroundPublicationFixture(t)
			want := bg.ErrCapacity
			if failure == "capacity" {
				in.HistoryReservation.Limits.OwnerRecords, in.HistoryReservation.Limits.InstanceRecords = 1, 1
				other := backgroundHistoryRow(in.HistoryReservation.Row)
				other.ID = "resp_existing"
				if err := s.DB().Create(&other).Error; err != nil {
					t.Fatal(err)
				}
			} else {
				want = errors.New("injected reservation insert failure")
				if err := s.DB().Callback().Create().Before("gorm:create").Register("background_history_insert_rollback", func(tx *gorm.DB) {
					if tx.Statement.Table == "response_histories" {
						tx.AddError(want)
					}
				}); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { s.DB().Callback().Create().Remove("background_history_insert_rollback") })
			}
			if _, err := s.AdmitBackground(context.Background(), in, limits); !errors.Is(err, want) {
				t.Fatal("admission did not atomically reject", err, want)
			}
			var tasks, ownHistory int64
			if err := s.DB().Model(&model.BackgroundJob{}).Count(&tasks).Error; err != nil || tasks != 0 {
				t.Fatal("task survived reservation failure", tasks, err)
			}
			if err := s.DB().Model(&model.ResponseHistory{}).Where("id = ?", in.PublicID).Count(&ownHistory).Error; err != nil || ownHistory != 0 {
				t.Fatal("reservation survived failed admission", ownHistory, err)
			}
		})
	}
}

func TestBackgroundPublicationTerminalCASAndRollback(t *testing.T) {
	s, binding, in, limits := backgroundPublicationFixture(t)
	row, claim := backgroundPublicationClaim(t, s, in, limits)
	ctx := context.Background()
	update := backgroundPublicationUpdate(row)
	update.History.Expected = 2
	if _, err := s.UpdateBackground(ctx, claim, row.Revision, update); !errors.Is(err, bg.ErrConflict) {
		t.Fatal("incorrect publication CAS accepted", err)
	}
	update = backgroundPublicationUpdate(row)
	injected := errors.New("injected terminal write failure")
	name := "background_publication_atomicity"
	if err := s.DB().Callback().Update().Before("gorm:update").Register(name, func(tx *gorm.DB) {
		if tx.Statement.Table == "background_jobs" {
			tx.AddError(injected)
		}
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateBackground(ctx, claim, row.Revision, update); !errors.Is(err, injected) {
		t.Fatal(err)
	}
	if err := s.DB().Callback().Update().Remove(name); err != nil {
		t.Fatal(err)
	}
	pending, err := s.ReadResponseHistory(ctx, binding, row.PublicID)
	if err != nil || pending.Version != 1 || pending.Busy != bs.HistoryBusy(row.ID) {
		t.Fatal("history published without terminal commit", pending, err)
	}
	retained, err := s.ReadClaimedBackground(ctx, claim)
	if err != nil || retained.Stage != string(bg.StageDispatching) || retained.Revision != row.Revision {
		t.Fatal("failed terminal commit changed task", retained, err)
	}
	terminal, err := s.UpdateBackground(ctx, claim, row.Revision, update)
	if err != nil || terminal.Stage != string(bg.StageTerminal) {
		t.Fatal(terminal, err)
	}
	published, err := s.ReadResponseHistory(ctx, binding, row.PublicID)
	if err != nil || published.Version != 2 || published.Busy != "" || published.CreatedAt != update.History.Record.CreatedAt || published.ExpiresAt != update.History.Record.ExpiresAt || published.ReservedBytes != 40 {
		t.Fatal("published record differs", published, err)
	}
	if _, err := s.UpdateBackground(ctx, claim, row.Revision, update); !errors.Is(err, bs.ErrClaimLost) {
		t.Fatal("stale terminal worker republished history", err)
	}
	if kind, err := s.BackgroundKind(ctx, binding, row.PublicID); err != nil || kind != bg.BackgroundResource {
		t.Fatal("same-id projections confused resource authority", kind, err)
	}
	if err := s.DB().Model(&model.BackgroundJob{}).Where("id = ?", row.ID).Update("content_expires_at", time.Now().Add(-time.Minute).UnixMilli()).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.CleanupBackground(ctx, 10); err != nil {
		t.Fatal(err)
	}
	if kind, err := s.BackgroundKind(ctx, binding, row.PublicID); err != nil || kind != bg.ForegroundResource {
		t.Fatal("background TTL removed independent history", kind, err)
	}
	if _, err := s.ReadResponseHistory(ctx, binding, row.PublicID); err != nil {
		t.Fatal("published history did not outlive task content", err)
	}
}

func TestBackgroundPublicationRevokedOrMissingReservationStillCommitsTerminal(t *testing.T) {
	for _, mode := range []string{"revoked", "missing", "deleted", "expired", "no-result"} {
		t.Run(mode, func(t *testing.T) {
			s, _, in, limits := backgroundPublicationFixture(t)
			row, claim := backgroundPublicationClaim(t, s, in, limits)
			update := backgroundPublicationUpdate(row)
			switch mode {
			case "revoked":
				if err := s.DB().Model(&model.Token{}).Where("id = ?", in.TokenID).Update("status", model.TokenStatusDisabled).Error; err != nil {
					t.Fatal(err)
				}
			case "missing":
				if err := s.DB().Where("id = ?", in.PublicID).Delete(&model.ResponseHistory{}).Error; err != nil {
					t.Fatal(err)
				}
			case "deleted":
				if err := s.DB().Model(&model.ResponseHistory{}).Where("id = ?", in.PublicID).Update("deleted", true).Error; err != nil {
					t.Fatal(err)
				}
			case "expired":
				if err := s.DB().Model(&model.ResponseHistory{}).Where("id = ?", in.PublicID).Update("expires_at", time.Now().Add(-time.Minute).UnixMilli()).Error; err != nil {
					t.Fatal(err)
				}
			case "no-result":
				update.History = nil
			}
			terminal, err := s.UpdateBackground(context.Background(), claim, row.Revision, update)
			if err != nil || terminal.Stage != string(bg.StageTerminal) {
				t.Fatal("unpublishable history blocked known terminal", terminal, err)
			}
			var histories []model.ResponseHistory
			if err := s.DB().Where("id = ?", in.PublicID).Find(&histories).Error; err != nil {
				t.Fatal(err)
			}
			if mode == "missing" {
				if len(histories) != 0 {
					t.Fatal("missing reservation was recreated", histories)
				}
			} else if len(histories) != 1 || !histories[0].Deleted || histories[0].Busy != "" || histories[0].ReservedBytes != 0 || len(histories[0].Ciphertext) != 0 {
				t.Fatal("unpublishable reservation retained ready content or capacity", histories)
			}
		})
	}
}

func TestBackgroundPublicationRecoveryOnlyPreservesOwnedPendingReservation(t *testing.T) {
	s, _, in, limits := backgroundPublicationFixture(t)
	if _, err := s.AdmitBackground(context.Background(), in, limits); err != nil {
		t.Fatal(err)
	}
	orphan := backgroundHistoryRow(in.HistoryReservation.Row)
	orphan.ID = "resp_orphan"
	if err := s.DB().Create(&orphan).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.RecoverResponseHistory(context.Background()); err != nil {
		t.Fatal(err)
	}
	var rows []model.ResponseHistory
	if err := s.DB().Order("id").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if r.ID == in.PublicID && r.Busy != bs.HistoryBusy(in.ID) || r.ID == orphan.ID && r.Busy != "uncertain" {
			t.Fatal("recovery trusted an unowned marker or lost a valid task", r)
		}
	}
	if _, err := s.CancelBackground(context.Background(), identity.Binding{UserID: in.UserID, TokenID: in.TokenID}, in.PublicID); err != nil {
		t.Fatal(err)
	}
	var released model.ResponseHistory
	if err := s.DB().First(&released, "id = ?", in.PublicID).Error; err != nil || !released.Deleted || released.ReservedBytes != 0 || released.Busy != "" {
		t.Fatal("queued cancellation leaked reservation", released, err)
	}
}

func TestBackgroundPublicationDeleteAtomicallyTombstonesBothAndNeverRevives(t *testing.T) {
	s, binding, in, limits := backgroundPublicationFixture(t)
	row, claim := backgroundPublicationClaim(t, s, in, limits)
	if _, err := s.UpdateBackground(context.Background(), claim, row.Revision, backgroundPublicationUpdate(row)); err != nil {
		t.Fatal(err)
	}
	injected := errors.New("injected explicit delete failure")
	if err := s.DB().Callback().Update().Before("gorm:update").Register("background_delete_both_rollback", func(tx *gorm.DB) {
		if tx.Statement.Table == "background_jobs" {
			tx.AddError(injected)
		}
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteBackground(context.Background(), binding, row.PublicID, nil); !errors.Is(err, injected) {
		t.Fatal(err)
	}
	if err := s.DB().Callback().Update().Remove("background_delete_both_rollback"); err != nil {
		t.Fatal(err)
	}
	if retained, err := s.ReadResponseHistory(context.Background(), binding, row.PublicID); err != nil || retained.Deleted || retained.Version != 2 {
		t.Fatal("history deletion escaped failed task delete", retained, err)
	}
	if err := s.DeleteBackground(context.Background(), binding, row.PublicID, nil); err != nil {
		t.Fatal(err)
	}
	var history model.ResponseHistory
	if err := s.DB().First(&history, "id = ?", row.PublicID).Error; err != nil || !history.Deleted || history.ReservedBytes != 0 || len(history.Ciphertext) != 0 {
		t.Fatal("explicit task deletion retained published history", history, err)
	}
	if _, err := s.UpdateBackground(context.Background(), claim, row.Revision, backgroundPublicationUpdate(row)); !errors.Is(err, bs.ErrClaimLost) {
		t.Fatal("deleted task could publish", err)
	}
	if _, err := s.InitializeBackground(context.Background(), in.DigestVersion, "disabled"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.BackgroundKind(context.Background(), binding, row.PublicID); !errors.Is(err, bg.ErrNotFound) {
		t.Fatal("restart revived deleted history", err)
	}
}

func TestBackgroundPublicationSessionCancellationReleasesMetadataOnlyReservation(t *testing.T) {
	s, _, in, limits := backgroundPublicationFixture(t)
	if _, err := s.AdmitBackground(context.Background(), in, limits); err != nil {
		t.Fatal(err)
	}
	session, _, err := s.Login("background-parent", "background-password-123!")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.TaskControl().Cancel(context.Background(), task.Access{Session: session}, in.PublicID, 1); err != nil {
		t.Fatal(err)
	}
	var h model.ResponseHistory
	if err := s.DB().First(&h, "id = ?", in.PublicID).Error; err != nil || !h.Deleted || h.ReservedBytes != 0 || h.Busy != "" {
		t.Fatal("session cancellation leaked a pending reservation", h, err)
	}
}

func TestBackgroundPublicationNonCompletedRemoteStateCannotPublish(t *testing.T) {
	for _, nativeState := range []bg.RemoteState{bg.RemoteIncomplete, bg.RemoteFailed, bg.RemoteCancelled} {
		t.Run(string(nativeState), func(t *testing.T) {
			s, _, in, limits := backgroundPublicationFixture(t)
			row, claim := backgroundPublicationClaim(t, s, in, limits)
			update := backgroundPublicationUpdate(row)
			update.UsageMetadata.NativeState = nativeState
			if _, err := s.UpdateBackground(context.Background(), claim, row.Revision, update); !errors.Is(err, bg.ErrConflict) {
				t.Fatal("non-completed state published ready history", nativeState, err)
			}
			update.History = nil
			if _, err := s.UpdateBackground(context.Background(), claim, row.Revision, update); err != nil {
				t.Fatal("known non-completed task could not terminate", err)
			}
		})
	}
}

func TestBackgroundPublicationLostDispatchAndExpiryReleaseOnlyPending(t *testing.T) {
	for _, mode := range []string{"expired-dispatch", "release-dispatch", "content-expiry", "initialize-uncertain"} {
		t.Run(mode, func(t *testing.T) {
			s, _, in, limits := backgroundPublicationFixture(t)
			row, claim := backgroundPublicationClaim(t, s, in, limits)
			ctx := context.Background()
			switch mode {
			case "expired-dispatch":
				if err := s.DB().Model(&model.BackgroundJob{}).Where("id = ?", row.ID).Update("claim_until", time.Now().Add(-time.Second).UnixMilli()).Error; err != nil {
					t.Fatal(err)
				}
				if _, _, err := s.ClaimBackground(ctx, time.Second); !errors.Is(err, bg.ErrNotFound) {
					t.Fatal(err)
				}
			case "release-dispatch":
				if err := s.ReleaseBackground(ctx, claim, time.Now().UnixMilli()); err != nil {
					t.Fatal(err)
				}
			case "content-expiry":
				expires := time.Now().Add(-time.Minute).UnixMilli()
				if err := s.DB().Model(&model.BackgroundJob{}).Where("id = ?", row.ID).Update("content_expires_at", expires).Error; err != nil {
					t.Fatal(err)
				}
				if err := s.CleanupBackground(ctx, 10); err != nil {
					t.Fatal(err)
				}
			case "initialize-uncertain":
				if err := s.DB().Model(&model.BackgroundJob{}).Where("id = ?", row.ID).Update("stage", bg.StageUncertain).Error; err != nil {
					t.Fatal(err)
				}
				if _, err := s.InitializeBackground(ctx, in.DigestVersion, "disabled"); err != nil {
					t.Fatal(err)
				}
			}
			var h model.ResponseHistory
			if err := s.DB().First(&h, "id = ?", in.PublicID).Error; err != nil || !h.Deleted || h.Busy != "" || h.ReservedBytes != 0 {
				t.Fatal("stopped task retained a pending history reservation", h, err)
			}
		})
	}
}

func TestBackgroundPublicationResourceKindRejectsInvalidCoexistence(t *testing.T) {
	for _, mode := range []string{"foreign-owner", "foreign-key", "foreign-model", "wrong-marker", "receipt-collision"} {
		t.Run(mode, func(t *testing.T) {
			s, binding, in, limits := backgroundPublicationFixture(t)
			if _, err := s.AdmitBackground(context.Background(), in, limits); err != nil {
				t.Fatal(err)
			}
			var err error
			switch mode {
			case "foreign-owner":
				err = s.DB().Model(&model.ResponseHistory{}).Where("id = ?", in.PublicID).Update("user_id", binding.UserID+1).Error
			case "foreign-key":
				err = s.DB().Model(&model.ResponseHistory{}).Where("id = ?", in.PublicID).Update("token_id", binding.TokenID+1).Error
			case "foreign-model":
				err = s.DB().Model(&model.ResponseHistory{}).Where("id = ?", in.PublicID).Update("model", "other").Error
			case "wrong-marker":
				err = s.DB().Model(&model.ResponseHistory{}).Where("id = ?", in.PublicID).Update("busy", "unrelated").Error
			case "receipt-collision":
				err = s.DB().Create(&model.ResponseRequest{ID: strings.Repeat("0", 64), ResponseID: in.PublicID, UserID: binding.UserID, TokenID: binding.TokenID}).Error
			}
			if err != nil {
				t.Fatal(err)
			}
			if kind, err := s.BackgroundKind(context.Background(), binding, in.PublicID); !errors.Is(err, bg.ErrNotFound) {
				t.Fatal("invalid coexistence was treated as authority", kind, err)
			}
		})
	}
}
