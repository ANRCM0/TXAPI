// Package configuration defines the candidate managed credential configuration
// ports approved by FC-P1-001. Version registration does not freeze this API.
package configuration

import (
	"context"
	"errors"
	v1 "minigateway/internal/foundation/contracts/configuration/v1"
)

const Version = "2"

var (
	ErrNotFound    = errors.New("managed credential not found")
	ErrConflict    = errors.New("managed credential configuration conflict")
	ErrUnavailable = errors.New("managed inference configuration is disabled")
)

type ManagedBindingInput struct {
	AccountID string `json:"account_id"`
	OwnerID   uint   `json:"owner_id"`
	TokenID   uint   `json:"token_id"`
	Model     string `json:"model"`
}

type ManagedChannelInput struct {
	Channel v1.ChannelInput     `json:"channel"`
	Binding ManagedBindingInput `json:"binding"`
}

type ManagedCredential struct {
	ChannelID    uint                `json:"channel_id"`
	CredentialID uint                `json:"credential_id"`
	Source       string              `json:"source"`
	Binding      ManagedBindingInput `json:"binding"`
	Revision     int64               `json:"revision,string"`
	Enabled      bool                `json:"enabled"`
}

type ManagedChannel struct {
	Channel    v1.Channel        `json:"channel"`
	Credential ManagedCredential `json:"credential"`
}

// Repository commits the real channel, credential and binding together. Errors
// after commit use v1.SavedError, so publication never replays a saved mutation.
type Repository interface {
	CreateManagedChannel(context.Context, *ManagedChannelInput) (*ManagedChannel, error)
	GetManagedCredential(context.Context, uint) (*ManagedCredential, error)
	UpdateManagedCredential(context.Context, uint, int64, *ManagedBindingInput) (*ManagedCredential, error)
	DisableManagedCredential(context.Context, uint, int64) (*ManagedCredential, error)
}

// Service shares the existing save/publication serialization and status.
type Service interface {
	CreateManagedChannel(context.Context, *ManagedChannelInput) (*ManagedChannel, v1.Change, error)
	GetManagedCredential(context.Context, uint) (*ManagedCredential, error)
	UpdateManagedCredential(context.Context, uint, int64, *ManagedBindingInput) (*ManagedCredential, v1.Change, error)
	DisableManagedCredential(context.Context, uint, int64) (*ManagedCredential, v1.Change, error)
}
