package store

import (
	"bytes"
	"encoding/json"
	"io"
	"slices"
	"time"

	"gorm.io/gorm"
	auth "minigateway/internal/foundation/contracts/upstreamauth/v1"
	"minigateway/internal/model"
	"minigateway/internal/secrets"
)

type upstreamTokenEnvelope struct {
	Format       int      `json:"format"`
	AccountID    string   `json:"account_id"`
	Provider     string   `json:"provider"`
	Issuer       string   `json:"issuer"`
	Subject      string   `json:"subject"`
	ClientID     string   `json:"client_id"`
	ExpiresAt    int64    `json:"expires_at"`
	AccessToken  string   `json:"access_token"`
	RefreshToken string   `json:"refresh_token"`
	IDToken      string   `json:"id_token"`
	Nonce        string   `json:"nonce"`
	ScopesKnown  bool     `json:"scopes_known"`
	Scopes       []string `json:"scopes"`
}

func upstreamAccountAAD(row model.ManagedUpstreamAccount) []byte {
	identity, _ := json.Marshal(struct {
		Domain             string
		AccountID          string
		Provider           string
		ClientID           string
		CredentialRevision int64
	}{"TXAPI/managed-upstream-account/v1", row.ID, row.Provider, row.ClientID, row.CredentialRevision})
	return identity
}

func upstreamTokensValid(tokens auth.Tokens) bool {
	if _, err := upstreamScopeJSON(tokens.ScopesKnown, tokens.Scopes); err != nil {
		return false
	}
	return auth.ValidWireText(tokens.AccessToken.Value(), auth.MaxTokenBytes) &&
		auth.ValidWireText(tokens.IDToken.Value(), auth.MaxTokenBytes) &&
		(tokens.RefreshToken.Value() == "" || auth.ValidWireText(tokens.RefreshToken.Value(), auth.MaxTokenBytes)) &&
		auth.ValidWireText(tokens.Nonce.Value(), 512) && upstreamTimeValid(tokens.ExpiresAt)
}

func (s *Store) sealUpstreamAccount(tx *gorm.DB, row model.ManagedUpstreamAccount, tokens auth.Tokens) ([]byte, error) {
	if s.vault == nil || s.checkCredentialMode(tx) != nil {
		return nil, auth.ErrEncryptionRequired
	}
	if !upstreamTokensValid(tokens) {
		return nil, auth.ErrInvalid
	}
	envelope := upstreamTokenEnvelope{Format: 1, AccountID: row.ID, Provider: row.Provider, Issuer: row.Issuer, Subject: row.Subject,
		ClientID: row.ClientID, ExpiresAt: tokens.ExpiresAt.UnixMilli(), AccessToken: tokens.AccessToken.Value(),
		RefreshToken: tokens.RefreshToken.Value(), IDToken: tokens.IDToken.Value(), Nonce: tokens.Nonce.Value()}
	envelope.ScopesKnown, envelope.Scopes = tokens.ScopesKnown, slices.Clone(tokens.Scopes)
	plain, err := json.Marshal(envelope)
	if err != nil || len(plain) > auth.MaxEnvelopeBytes {
		return nil, auth.ErrInvalid
	}
	defer clear(plain)
	sealed, err := s.vault.ring.Seal(plain, upstreamAccountAAD(row))
	if err != nil {
		return nil, auth.ErrEncryptionRequired
	}
	return sealed, nil
}

func (s *Store) openUpstreamAccount(tx *gorm.DB, row model.ManagedUpstreamAccount) (auth.Tokens, error) {
	if s.vault == nil || s.checkCredentialMode(tx) != nil || !upstreamRowValid(row) ||
		len(row.TokenCiphertext) == 0 || len(row.TokenCiphertext) > auth.MaxEnvelopeBytes+128 {
		return auth.Tokens{}, auth.ErrEncryptionRequired
	}
	plain, err := s.vault.ring.Open(row.TokenCiphertext, upstreamAccountAAD(row))
	if err != nil {
		return auth.Tokens{}, auth.ErrEncryptionRequired
	}
	defer clear(plain)
	if len(plain) > auth.MaxEnvelopeBytes {
		return auth.Tokens{}, auth.ErrEncryptionRequired
	}
	var envelope upstreamTokenEnvelope
	decoder := json.NewDecoder(bytes.NewReader(plain))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&envelope) != nil || decoder.Decode(&struct{}{}) != io.EOF || envelope.Format != 1 ||
		envelope.AccountID != row.ID || envelope.Provider != row.Provider || envelope.ClientID != row.ClientID ||
		envelope.Issuer != row.Issuer || envelope.Subject != row.Subject || envelope.ExpiresAt != row.ExpiresAt.UnixMilli() ||
		(envelope.RefreshToken != "") != row.HasRefresh || envelope.ScopesKnown != row.ScopesKnown {
		return auth.Tokens{}, auth.ErrEncryptionRequired
	}
	tokens := auth.Tokens{AccessToken: auth.NewSecret(envelope.AccessToken), RefreshToken: auth.NewSecret(envelope.RefreshToken),
		IDToken: auth.NewSecret(envelope.IDToken), Nonce: auth.NewSecret(envelope.Nonce), ExpiresAt: time.UnixMilli(envelope.ExpiresAt).UTC()}
	tokens.ScopesKnown, tokens.Scopes = envelope.ScopesKnown, slices.Clone(envelope.Scopes)
	scopesJSON, scopeErr := upstreamScopeJSON(tokens.ScopesKnown, tokens.Scopes)
	if scopeErr != nil || scopesJSON != row.ScopesJSON {
		return auth.Tokens{}, auth.ErrEncryptionRequired
	}
	if !upstreamTokensValid(tokens) {
		return auth.Tokens{}, auth.ErrEncryptionRequired
	}
	return tokens, nil
}

// The existing credential-vault activation transaction calls this once after
// pinning its mode. Old keys authenticate every retained row before rewrapping.
func (s *Store) initializeUpstreamAccountVault(tx *gorm.DB) error {
	var rows []model.ManagedUpstreamAccount
	if err := tx.Order("id").Limit(upstreamAccountCapacity + 1).Find(&rows).Error; err != nil {
		return auth.ErrUnavailable
	}
	if len(rows) == 0 {
		return nil
	}
	if len(rows) > upstreamAccountCapacity || s.vault == nil || s.checkCredentialMode(tx) != nil {
		return auth.ErrEncryptionRequired
	}
	for _, row := range rows {
		tokens, err := s.openUpstreamAccount(tx, row)
		if err != nil {
			return auth.ErrEncryptionRequired
		}
		header, err := secrets.Header(row.TokenCiphertext)
		if err != nil {
			return auth.ErrEncryptionRequired
		}
		if string(header[2:]) == s.vault.ring.Active() {
			continue
		}
		sealed, err := s.sealUpstreamAccount(tx, row, tokens)
		if err != nil {
			return err
		}
		result := tx.Model(&model.ManagedUpstreamAccount{}).
			Where("id = ? AND row_revision = ? AND credential_revision = ? AND token_ciphertext = ?", row.ID, row.RowRevision, row.CredentialRevision, row.TokenCiphertext).
			UpdateColumn("token_ciphertext", sealed)
		if result.Error != nil || result.RowsAffected != 1 {
			return auth.ErrEncryptionRequired
		}
	}
	return nil
}
