package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"slices"
	"strconv"
	"time"

	"gorm.io/gorm"
	auth "minigateway/internal/foundation/contracts/upstreamauth/v1"
	"minigateway/internal/model"
)

const (
	upstreamAccountCapacity = 1000
	upstreamClaimCapacity   = 4
	upstreamClaimLease      = 90 * time.Second
	upstreamSuccessCooldown = 5 * time.Second
	upstreamHostSetting     = "managed_upstream_accounts.host_id"
	upstreamIssuer          = "https://auth.openai.com"
)

const upstreamMetadataColumns = "id,provider,name,issuer,subject,client_id,row_revision,credential_revision,fence,state,has_refresh,scopes_known,scopes_json,expires_at,next_refresh_at,last_failure,retry_count,created_at,updated_at"

type upstreamAccounts struct {
	store *Store
	now   func() time.Time
}

func (s *Store) UpstreamAccounts() auth.Repository {
	return &upstreamAccounts{store: s, now: time.Now}
}

func upstreamError(err error) error {
	if err == nil {
		return nil
	}
	for _, fixed := range []error{auth.ErrInvalid, auth.ErrUnavailable, auth.ErrNotFound, auth.ErrConflict, auth.ErrEncryptionRequired,
		auth.ErrIdentityMismatch, auth.ErrBusy, auth.ErrBackoff, auth.ErrReauthorize, auth.ErrUncertain, context.Canceled, context.DeadlineExceeded} {
		if errors.Is(err, fixed) {
			return fixed
		}
	}
	return auth.ErrUnavailable
}

func upstreamContext(ctx context.Context) (context.Context, context.CancelFunc, error) {
	if ctx == nil {
		return nil, nil, auth.ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, upstreamError(err)
	}
	c, cancel := context.WithTimeout(ctx, 5*time.Second)
	return c, cancel, nil
}

func (r *upstreamAccounts) read(ctx context.Context, fn func(*gorm.DB) error) error {
	c, cancel, err := upstreamContext(ctx)
	if err != nil {
		return err
	}
	defer cancel()
	if r == nil || r.store == nil || r.store.db == nil {
		return auth.ErrUnavailable
	}
	return upstreamError(fn(r.store.db.WithContext(c)))
}

func (r *upstreamAccounts) write(ctx context.Context, secret bool, fn func(*gorm.DB) error) error {
	return r.read(ctx, func(db *gorm.DB) error {
		return db.Transaction(func(tx *gorm.DB) error {
			// A write before any claim/capacity read also serializes other CLI
			// processes; Store's single connection alone cannot provide that.
			locked := tx.Exec("UPDATE schema_migrations SET version = version WHERE version = ?", latestMigrationVersion)
			if locked.Error != nil {
				return locked.Error
			}
			if locked.RowsAffected != 1 {
				return auth.ErrUnavailable
			}
			if secret && (r.store.vault == nil || r.store.checkCredentialMode(tx) != nil) {
				return auth.ErrEncryptionRequired
			}
			return fn(tx)
		})
	})
}

func (r *upstreamAccounts) timestamp() time.Time { return r.now().UTC().Truncate(time.Millisecond) }

func upstreamTimeValid(t time.Time) bool {
	return !t.IsZero() && t.Year() >= 1970 && t.Year() <= 9999 && t.UnixMilli() > 0
}

func upstreamMetadataValid(row model.ManagedUpstreamAccount) bool {
	if _, err := upstreamScopes(row.ScopesKnown, row.ScopesJSON); err != nil {
		return false
	}
	if !auth.ValidAccountID(row.ID) || row.Provider != "openai" || row.Issuer != upstreamIssuer ||
		!auth.ValidWireText(row.Subject, 512) || !auth.ValidWireText(row.ClientID, 512) || !auth.ValidName(row.Name) ||
		row.RowRevision <= 0 || row.CredentialRevision <= 0 || row.Fence <= 0 ||
		row.RetryCount < 0 || row.RetryCount > 6 || !upstreamTimeValid(row.ExpiresAt) ||
		!upstreamTimeValid(row.CreatedAt) || !upstreamTimeValid(row.UpdatedAt) ||
		(!row.NextRefreshAt.IsZero() && !upstreamTimeValid(row.NextRefreshAt)) {
		return false
	}
	switch auth.State(row.State) {
	case auth.Active, auth.Disabled, auth.Reauthorize, auth.Uncertain:
	default:
		return false
	}
	switch auth.Failure(row.LastFailure) {
	case auth.NoFailure, auth.NotSentFailure, auth.RejectedFailure, auth.UnknownFailure:
	default:
		return false
	}
	return true
}

func upstreamRowValid(row model.ManagedUpstreamAccount) bool {
	if !upstreamMetadataValid(row) {
		return false
	}
	if row.ClaimHash == "" {
		return row.ClaimUntil.IsZero() && !row.RefreshStarted
	}
	return row.State == string(auth.Active) && upstreamDigest(row.ClaimHash) && upstreamTimeValid(row.ClaimUntil)
}

func upstreamView(row model.ManagedUpstreamAccount) auth.Account {
	scopes, _ := upstreamScopes(row.ScopesKnown, row.ScopesJSON)
	return auth.Account{ID: row.ID, Provider: row.Provider, Name: row.Name, Issuer: row.Issuer, Subject: row.Subject,
		ClientID: row.ClientID, Revision: row.RowRevision, CredentialRevision: row.CredentialRevision,
		State: auth.State(row.State), HasRefresh: row.HasRefresh, ScopesKnown: row.ScopesKnown, Scopes: scopes, ExpiresAt: row.ExpiresAt.UTC(),
		NextRefreshAt: row.NextRefreshAt.UTC(), LastFailure: auth.Failure(row.LastFailure), UpdatedAt: row.UpdatedAt.UTC()}
}

func upstreamFind(tx *gorm.DB, id string) (model.ManagedUpstreamAccount, error) {
	var row model.ManagedUpstreamAccount
	result := tx.Where("id = ?", id).Limit(1).Find(&row)
	if result.Error != nil {
		return row, result.Error
	}
	if result.RowsAffected != 1 {
		return row, auth.ErrNotFound
	}
	if !upstreamRowValid(row) {
		return row, auth.ErrUnavailable
	}
	return row, nil
}

func upstreamIncrement(values ...*int64) error {
	for _, value := range values {
		if *value <= 0 || *value == math.MaxInt64 {
			return auth.ErrConflict
		}
	}
	for _, value := range values {
		*value = *value + 1
	}
	return nil
}

func upstreamSave(tx *gorm.DB, old, row model.ManagedUpstreamAccount) error {
	if !upstreamRowValid(row) {
		return auth.ErrUnavailable
	}
	result := tx.Model(&model.ManagedUpstreamAccount{}).
		Where("id = ? AND row_revision = ? AND credential_revision = ? AND fence = ?", old.ID, old.RowRevision, old.CredentialRevision, old.Fence).
		UpdateColumns(map[string]any{"name": row.Name, "row_revision": row.RowRevision, "credential_revision": row.CredentialRevision,
			"auth_source": row.AuthSource, "resource": row.Resource,
			"fence": row.Fence, "token_ciphertext": row.TokenCiphertext, "state": row.State, "has_refresh": row.HasRefresh,
			"scopes_known": row.ScopesKnown, "scopes_json": row.ScopesJSON,
			"expires_at": row.ExpiresAt, "next_refresh_at": row.NextRefreshAt, "last_failure": row.LastFailure,
			"retry_count": row.RetryCount, "claim_hash": row.ClaimHash, "claim_until": row.ClaimUntil,
			"refresh_started": row.RefreshStarted, "updated_at": row.UpdatedAt})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return auth.ErrConflict
	}
	return nil
}

func upstreamClearClaim(row *model.ManagedUpstreamAccount) {
	row.ClaimHash, row.ClaimUntil, row.RefreshStarted = "", time.Time{}, false
}

func upstreamStateError(row model.ManagedUpstreamAccount) error {
	switch auth.State(row.State) {
	case auth.Uncertain:
		return auth.ErrUncertain
	case auth.Disabled, auth.Reauthorize:
		return auth.ErrReauthorize
	}
	return nil
}

func (r *upstreamAccounts) HostID(ctx context.Context) (out string, err error) {
	err = r.write(ctx, true, func(tx *gorm.DB) error {
		var setting model.Setting
		found := tx.Where("key = ?", upstreamHostSetting).Limit(1).Find(&setting)
		if found.Error != nil {
			return found.Error
		}
		if found.RowsAffected == 1 {
			if !auth.ValidHostID(setting.Value) {
				return auth.ErrUnavailable
			}
			out = setting.Value
			return nil
		}
		var data [16]byte
		if _, err := rand.Read(data[:]); err != nil {
			return auth.ErrUnavailable
		}
		data[6], data[8] = (data[6]&15)|64, (data[8]&63)|128
		encoded := hex.EncodeToString(data[:])
		out = encoded[:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:]
		return tx.Create(&model.Setting{Key: upstreamHostSetting, Value: out}).Error
	})
	if err != nil {
		out = ""
	}
	return
}

func (r *upstreamAccounts) List(ctx context.Context, limit, offset int) (out auth.Page, err error) {
	if limit == 0 {
		limit = 50
	}
	if limit < 1 || limit > 100 || offset < 0 || offset > 10000 {
		return out, auth.ErrInvalid
	}
	err = r.read(ctx, func(db *gorm.DB) error {
		var rows []model.ManagedUpstreamAccount
		if e := db.Select(upstreamMetadataColumns).Order("created_at DESC,id DESC").Limit(limit + 1).Offset(offset).Find(&rows).Error; e != nil {
			return e
		}
		out = auth.Page{Data: make([]auth.Account, 0, min(len(rows), limit)), HasMore: len(rows) > limit}
		for i, row := range rows {
			if !upstreamMetadataValid(row) {
				return auth.ErrUnavailable
			}
			if i < limit {
				out.Data = append(out.Data, upstreamView(row))
			}
		}
		return nil
	})
	if err != nil {
		out = auth.Page{}
	}
	return
}

func (r *upstreamAccounts) Read(ctx context.Context, id string) (out auth.Account, err error) {
	if !auth.ValidAccountID(id) {
		return out, auth.ErrInvalid
	}
	err = r.read(ctx, func(db *gorm.DB) error {
		var row model.ManagedUpstreamAccount
		result := db.Select(upstreamMetadataColumns).Where("id = ?", id).Limit(1).Find(&row)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return auth.ErrNotFound
		}
		if !upstreamMetadataValid(row) {
			return auth.ErrUnavailable
		}
		out = upstreamView(row)
		return nil
	})
	if err != nil {
		out = auth.Account{}
	}
	return
}

func (r *upstreamAccounts) BeginAuthorization(ctx context.Context, id string, revision int64) (out auth.Account, err error) {
	if !auth.ValidAccountID(id) || revision <= 0 {
		return out, auth.ErrInvalid
	}
	err = r.write(ctx, true, func(tx *gorm.DB) error {
		row, e := upstreamFind(tx, id)
		if e != nil {
			return e
		}
		if row.RowRevision != revision {
			return auth.ErrConflict
		}
		old := row
		if e := upstreamIncrement(&row.RowRevision, &row.Fence); e != nil {
			return e
		}
		row.State, row.LastFailure, row.RetryCount = string(auth.Reauthorize), "", 0
		row.NextRefreshAt, row.UpdatedAt = time.Time{}, r.timestamp()
		upstreamClearClaim(&row)
		if e := upstreamSave(tx, old, row); e != nil {
			return e
		}
		out = upstreamView(row)
		return nil
	})
	if err != nil {
		out = auth.Account{}
	}
	return
}

func upstreamAuthorizationValid(in auth.AuthorizationWrite) bool {
	return auth.ValidAccountID(in.ID) && in.ExpectedRevision >= 0 && in.Provider == "openai" && auth.ValidName(in.Name) &&
		in.Identity.Issuer == upstreamIssuer && auth.ValidWireText(in.Identity.Subject, 512) && auth.ValidWireText(in.ClientID, 512) && upstreamTokensValid(in.Tokens)
}

func (r *upstreamAccounts) SaveAuthorization(ctx context.Context, in auth.AuthorizationWrite) (out auth.Account, err error) {
	in.Tokens.Scopes = slices.Clone(in.Tokens.Scopes)
	if !upstreamAuthorizationValid(in) {
		return out, auth.ErrInvalid
	}
	in.Tokens.ExpiresAt = in.Tokens.ExpiresAt.UTC().Truncate(time.Millisecond)
	err = r.write(ctx, true, func(tx *gorm.DB) error {
		now := r.timestamp()
		if !in.Tokens.ExpiresAt.After(now) {
			return auth.ErrInvalid
		}
		var row, old model.ManagedUpstreamAccount
		if in.ExpectedRevision == 0 {
			var found int64
			if e := tx.Model(&model.ManagedUpstreamAccount{}).Where("id = ?", in.ID).Count(&found).Error; e != nil {
				return e
			}
			if found != 0 {
				return auth.ErrConflict
			}
			if e := tx.Model(&model.ManagedUpstreamAccount{}).
				Where("provider = ? AND issuer = ? AND subject = ? AND client_id = ?", in.Provider, in.Identity.Issuer, in.Identity.Subject, in.ClientID).
				Count(&found).Error; e != nil {
				return e
			}
			if found != 0 {
				return auth.ErrConflict
			}
			if e := tx.Model(&model.ManagedUpstreamAccount{}).Count(&found).Error; e != nil {
				return e
			}
			if found >= upstreamAccountCapacity {
				return auth.ErrBusy
			}
			row = model.ManagedUpstreamAccount{ID: in.ID, Provider: in.Provider, Issuer: in.Identity.Issuer, Subject: in.Identity.Subject,
				ClientID: in.ClientID, RowRevision: 1, CredentialRevision: 1, Fence: 1, CreatedAt: now}
		} else {
			var e error
			row, e = upstreamFind(tx, in.ID)
			if e != nil {
				return e
			}
			if row.RowRevision != in.ExpectedRevision || row.State != string(auth.Reauthorize) {
				return auth.ErrConflict
			}
			if row.Provider != in.Provider || row.Issuer != in.Identity.Issuer || row.Subject != in.Identity.Subject || row.ClientID != in.ClientID {
				return auth.ErrIdentityMismatch
			}
			old = row
			if e := upstreamIncrement(&row.RowRevision, &row.CredentialRevision); e != nil {
				return e
			}
		}
		row.AuthSource, row.Resource = managedSIWCSource, managedSIWCResource
		row.Name, row.State, row.HasRefresh = in.Name, string(auth.Active), in.Tokens.RefreshToken.Value() != ""
		row.ScopesKnown, row.ScopesJSON = in.Tokens.ScopesKnown, "[]"
		row.ScopesJSON, _ = upstreamScopeJSON(in.Tokens.ScopesKnown, in.Tokens.Scopes)
		row.ExpiresAt, row.NextRefreshAt, row.UpdatedAt = in.Tokens.ExpiresAt, now.Add(upstreamSuccessCooldown), now
		row.LastFailure, row.RetryCount = "", 0
		upstreamClearClaim(&row)
		sealed, e := r.store.sealUpstreamAccount(tx, row, in.Tokens)
		if e != nil {
			return e
		}
		row.TokenCiphertext = sealed
		if in.ExpectedRevision == 0 {
			if e := tx.Create(&row).Error; e != nil {
				return e
			}
		} else if e := upstreamSave(tx, old, row); e != nil {
			return e
		}
		out = upstreamView(row)
		return nil
	})
	if err != nil {
		out = auth.Account{}
	}
	return
}

func upstreamDigest(value string) bool {
	if len(value) != 64 {
		return false
	}
	data, err := hex.DecodeString(value)
	return err == nil && hex.EncodeToString(data) == value
}

func upstreamProofHash(c auth.Claim) string {
	value := "TXAPI/managed-upstream-claim/v1/" + c.AccountID + "/" + strconv.FormatInt(c.Revision, 10) + "/" +
		strconv.FormatInt(c.CredentialRevision, 10) + "/" + strconv.FormatInt(c.Fence, 10) + "/" + c.Proof.Value()
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

// Adapted from codex-proxy-rs 591f95c gateway-core/provider_ports/refresh.rs
// stable_factor and refresh_backoff_at: persisted retries only after no send.
func upstreamBackoff(id string, attempts int) time.Duration {
	base := int64(5)
	for i := 1; i < attempts && base < 300; i++ {
		base = min(base*3, 300)
	}
	hash := uint64(0xcbf29ce484222325)
	for _, b := range []byte(id + "\x00refresh-not-sent") {
		hash ^= uint64(b)
		hash *= 0x100000001b3
	}
	factor := int64(800 + hash%401)
	millis := min((base*1000*factor+500)/1000, 300000)
	return time.Duration(millis) * time.Millisecond
}

func upstreamNotSent(row *model.ManagedUpstreamAccount, now time.Time) {
	row.State, row.LastFailure = string(auth.Active), string(auth.NotSentFailure)
	row.RetryCount = min(row.RetryCount+1, 6)
	row.NextRefreshAt = now.Add(upstreamBackoff(row.ID, row.RetryCount))
}

func upstreamRecoverRow(tx *gorm.DB, row *model.ManagedUpstreamAccount, now time.Time) (bool, error) {
	if row.ClaimHash == "" || row.ClaimUntil.After(now) {
		return false, nil
	}
	old := *row
	if e := upstreamIncrement(&row.RowRevision, &row.Fence); e != nil {
		return false, e
	}
	if row.RefreshStarted {
		row.State, row.LastFailure, row.NextRefreshAt = string(auth.Uncertain), string(auth.UnknownFailure), time.Time{}
	} else {
		upstreamNotSent(row, now)
	}
	upstreamClearClaim(row)
	row.UpdatedAt = now
	return true, upstreamSave(tx, old, *row)
}

func (r *upstreamAccounts) ClaimRefresh(ctx context.Context, id string, revision int64) (out auth.Claim, err error) {
	if !auth.ValidAccountID(id) || revision <= 0 {
		return out, auth.ErrInvalid
	}
	var semantic error
	err = r.write(ctx, true, func(tx *gorm.DB) error {
		row, e := upstreamFind(tx, id)
		if e != nil {
			return e
		}
		now := r.timestamp()
		if _, e := upstreamRecoverRow(tx, &row, now); e != nil {
			return e
		}
		// Return semantic failures after commit: expiry recovery must survive a
		// refusal instead of being rolled back by gorm.Transaction.
		if semantic = upstreamStateError(row); semantic != nil {
			return nil
		}
		if row.RowRevision != revision {
			semantic = auth.ErrConflict
			return nil
		}
		if !row.HasRefresh {
			semantic = auth.ErrReauthorize
			return nil
		}
		if row.ClaimHash != "" {
			semantic = auth.ErrBusy
			return nil
		}
		if row.NextRefreshAt.After(now) {
			semantic = auth.ErrBackoff
			return nil
		}
		// Leave room to finish or invalidate the claim before any rotating RT
		// can escape. Checking overflow only when persisting success is too late.
		if row.RowRevision >= math.MaxInt64-1 || row.Fence >= math.MaxInt64-1 || row.CredentialRevision == math.MaxInt64 {
			return auth.ErrConflict
		}
		var claims int64
		if e := tx.Model(&model.ManagedUpstreamAccount{}).Where("claim_hash <> '' AND claim_until > ?", now).Count(&claims).Error; e != nil {
			return e
		}
		if claims >= upstreamClaimCapacity {
			semantic = auth.ErrBusy
			return nil
		}
		old := row
		if e := upstreamIncrement(&row.RowRevision, &row.Fence); e != nil {
			return e
		}
		var proof [32]byte
		if _, e := rand.Read(proof[:]); e != nil {
			return auth.ErrUnavailable
		}
		out = auth.Claim{AccountID: row.ID, Revision: row.RowRevision, CredentialRevision: row.CredentialRevision, Fence: row.Fence,
			Proof: auth.NewSecret(hex.EncodeToString(proof[:])), LeaseUntil: now.Add(upstreamClaimLease)}
		row.ClaimHash, row.ClaimUntil, row.RefreshStarted, row.UpdatedAt = upstreamProofHash(out), out.LeaseUntil, false, now
		return upstreamSave(tx, old, row)
	})
	if err == nil {
		err = semantic
	}
	if err != nil {
		out = auth.Claim{}
	}
	return
}

func upstreamClaimValid(c auth.Claim) bool {
	return auth.ValidAccountID(c.AccountID) && c.Revision > 0 && c.CredentialRevision > 0 && c.Fence > 0 && upstreamDigest(c.Proof.Value()) && upstreamTimeValid(c.LeaseUntil)
}

func upstreamOwned(tx *gorm.DB, c auth.Claim, now time.Time) (row model.ManagedUpstreamAccount, semantic, hard error) {
	row, hard = upstreamFind(tx, c.AccountID)
	if hard != nil {
		return
	}
	if row.RowRevision != c.Revision || row.CredentialRevision != c.CredentialRevision || row.Fence != c.Fence ||
		row.ClaimHash != upstreamProofHash(c) || row.State != string(auth.Active) || !row.ClaimUntil.Equal(c.LeaseUntil) {
		semantic = auth.ErrConflict
		return
	}
	if row.ClaimUntil.After(now) {
		return
	}
	_, hard = upstreamRecoverRow(tx, &row, now)
	if hard == nil {
		semantic = auth.ErrConflict
		if row.State == string(auth.Uncertain) {
			semantic = auth.ErrUncertain
		}
	}
	return
}

func (r *upstreamAccounts) StartRefresh(ctx context.Context, claim auth.Claim) (out auth.Credential, err error) {
	if !upstreamClaimValid(claim) {
		return out, auth.ErrInvalid
	}
	var semantic error
	err = r.write(ctx, true, func(tx *gorm.DB) error {
		row, decision, e := upstreamOwned(tx, claim, r.timestamp())
		if e != nil {
			return e
		}
		if semantic = decision; semantic != nil {
			return nil
		}
		if row.RefreshStarted {
			semantic = auth.ErrUncertain
			return nil
		}
		if row.RowRevision == math.MaxInt64 || row.CredentialRevision == math.MaxInt64 || row.Fence == math.MaxInt64 {
			return auth.ErrConflict
		}
		tokens, e := r.store.openUpstreamAccount(tx, row)
		if e != nil {
			return e
		}
		old := row
		row.RefreshStarted = true
		if e := upstreamSave(tx, old, row); e != nil {
			return e
		}
		out = auth.Credential{Account: upstreamView(row), Tokens: tokens}
		return nil
	})
	if err == nil {
		err = semantic
	}
	if err != nil {
		out = auth.Credential{}
	}
	return
}

func (r *upstreamAccounts) CompleteRefresh(ctx context.Context, claim auth.Claim, tokens auth.Tokens) (out auth.Account, err error) {
	tokens.Scopes = slices.Clone(tokens.Scopes)
	if !upstreamClaimValid(claim) || !upstreamTokensValid(tokens) {
		return out, auth.ErrInvalid
	}
	tokens.ExpiresAt = tokens.ExpiresAt.UTC().Truncate(time.Millisecond)
	var semantic error
	err = r.write(ctx, true, func(tx *gorm.DB) error {
		now := r.timestamp()
		row, decision, e := upstreamOwned(tx, claim, now)
		if e != nil {
			return e
		}
		if semantic = decision; semantic != nil {
			return nil
		}
		if !row.RefreshStarted || !tokens.ExpiresAt.After(now) {
			semantic = auth.ErrConflict
			return nil
		}
		old := row
		if e := upstreamIncrement(&row.RowRevision, &row.CredentialRevision); e != nil {
			return e
		}
		row.HasRefresh, row.ExpiresAt, row.NextRefreshAt = tokens.RefreshToken.Value() != "", tokens.ExpiresAt, now.Add(upstreamSuccessCooldown)
		row.ScopesKnown, row.ScopesJSON = tokens.ScopesKnown, "[]"
		row.ScopesJSON, _ = upstreamScopeJSON(tokens.ScopesKnown, tokens.Scopes)
		row.LastFailure, row.RetryCount, row.UpdatedAt = "", 0, now
		upstreamClearClaim(&row)
		sealed, e := r.store.sealUpstreamAccount(tx, row, tokens)
		if e != nil {
			return e
		}
		row.TokenCiphertext = sealed
		if e := upstreamSave(tx, old, row); e != nil {
			return e
		}
		out = upstreamView(row)
		return nil
	})
	if err == nil {
		err = semantic
	}
	if err != nil {
		out = auth.Account{}
	}
	return
}

func (r *upstreamAccounts) FinishRefresh(ctx context.Context, claim auth.Claim, failure auth.Failure) (out auth.Account, err error) {
	if !upstreamClaimValid(claim) || (failure != auth.NotSentFailure && failure != auth.RejectedFailure && failure != auth.UnknownFailure) {
		return out, auth.ErrInvalid
	}
	var semantic error
	err = r.write(ctx, true, func(tx *gorm.DB) error {
		now := r.timestamp()
		row, decision, e := upstreamOwned(tx, claim, now)
		if e != nil {
			return e
		}
		if semantic = decision; semantic != nil {
			return nil
		}
		if !row.RefreshStarted && failure != auth.NotSentFailure {
			semantic = auth.ErrConflict
			return nil
		}
		old := row
		if e := upstreamIncrement(&row.RowRevision, &row.Fence); e != nil {
			return e
		}
		if failure == auth.NotSentFailure {
			upstreamNotSent(&row, now)
		} else {
			row.LastFailure, row.NextRefreshAt = string(failure), time.Time{}
			row.State = string(auth.Reauthorize)
			if failure == auth.UnknownFailure {
				row.State = string(auth.Uncertain)
			}
		}
		upstreamClearClaim(&row)
		row.UpdatedAt = now
		if e := upstreamSave(tx, old, row); e != nil {
			return e
		}
		out = upstreamView(row)
		return nil
	})
	if err == nil {
		err = semantic
	}
	if err != nil {
		out = auth.Account{}
	}
	return
}

func (r *upstreamAccounts) Disable(ctx context.Context, id string, revision int64) (out auth.Account, err error) {
	if !auth.ValidAccountID(id) || revision <= 0 {
		return out, auth.ErrInvalid
	}
	err = r.write(ctx, false, func(tx *gorm.DB) error {
		row, e := upstreamFind(tx, id)
		if e != nil {
			return e
		}
		if row.RowRevision != revision {
			return auth.ErrConflict
		}
		old := row
		if e := upstreamIncrement(&row.RowRevision, &row.Fence); e != nil {
			return e
		}
		row.State, row.NextRefreshAt, row.UpdatedAt = string(auth.Disabled), time.Time{}, r.timestamp()
		upstreamClearClaim(&row)
		if e := upstreamSave(tx, old, row); e != nil {
			return e
		}
		out = upstreamView(row)
		return nil
	})
	if err != nil {
		out = auth.Account{}
	}
	return
}

func (r *upstreamAccounts) Recover(ctx context.Context, limit int) (out int, err error) {
	if limit < 1 || limit > upstreamAccountCapacity {
		return 0, auth.ErrInvalid
	}
	err = r.write(ctx, false, func(tx *gorm.DB) error {
		now := r.timestamp()
		var rows []model.ManagedUpstreamAccount
		if e := tx.Where("claim_hash <> '' AND claim_until <= ?", now).Order("claim_until,id").Limit(limit).Find(&rows).Error; e != nil {
			return e
		}
		for i := range rows {
			if !upstreamRowValid(rows[i]) {
				return auth.ErrUnavailable
			}
			changed, e := upstreamRecoverRow(tx, &rows[i], now)
			if e != nil {
				return e
			}
			if changed {
				out++
			}
		}
		return nil
	})
	if err != nil {
		out = 0
	}
	return
}

var _ auth.Repository = (*upstreamAccounts)(nil)

func upstreamScopeJSON(known bool, scopes []string) (string, error) {
	if len(scopes) > 32 || !known && len(scopes) != 0 {
		return "", auth.ErrInvalid
	}
	for i, scope := range scopes {
		if !auth.ValidWireText(scope, 128) || i > 0 && scopes[i-1] >= scope {
			return "", auth.ErrInvalid
		}
	}
	canonical := make([]string, len(scopes))
	copy(canonical, scopes)
	data, err := json.Marshal(canonical)
	if err != nil || len(data) > 8192 {
		return "", auth.ErrInvalid
	}
	return string(data), nil
}

func upstreamScopes(known bool, encoded string) ([]string, error) {
	if len(encoded) > 8192 || encoded == "" {
		return nil, auth.ErrUnavailable
	}
	var scopes []string
	if json.Unmarshal([]byte(encoded), &scopes) != nil {
		return nil, auth.ErrUnavailable
	}
	canonical, err := upstreamScopeJSON(known, scopes)
	if err != nil || canonical != encoded {
		return nil, auth.ErrUnavailable
	}
	if !known {
		return nil, nil
	}
	return scopes, nil
}
