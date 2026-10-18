// Package configuration defines persistence and publication contracts.
package configuration

import (
	"fmt"
	"math"
	"time"
)

const Version = "1"
const MaxPricePer1K = 1000000000

type ChannelInput struct {
	Name         string            `json:"name"`
	Type         string            `json:"type"`
	BaseURL      string            `json:"base_url"`
	Models       []string          `json:"models"`
	ModelMapping map[string]string `json:"model_mapping"`
	GroupName    string            `json:"group_name"`
	Priority     int               `json:"priority"`
	Weight       int               `json:"weight"`
	Status       string            `json:"status"`
	// APIKey supplies the initial credential when creating a channel.
	APIKey string `json:"api_key"`
}

type CredentialInput struct {
	Name   string `json:"name"`
	APIKey string `json:"api_key"`
	Weight int    `json:"weight"`
}

type PriceInput struct {
	InputPricePer1K  float64 `json:"input_price_per_1k"`
	OutputPricePer1K float64 `json:"output_price_per_1k"`
}

type PublicationStatus struct {
	DesiredRevision uint64    `json:"desired_revision"`
	AppliedRevision uint64    `json:"applied_revision"`
	LastError       string    `json:"last_error"`
	UpdatedAt       time.Time `json:"updated_at"`
}

type ConfigStatus struct {
	Epoch    string            `json:"epoch"`
	Channels PublicationStatus `json:"channels"`
	Prices   PublicationStatus `json:"prices"`
}

type Change struct {
	Saved       bool
	Publication PublicationStatus
}

type Channel struct {
	ID           uint              `json:"id"`
	Name         string            `json:"name"`
	Type         string            `json:"type"`
	BaseURL      string            `json:"base_url"`
	Models       []string          `json:"models"`
	ModelMapping map[string]string `json:"model_mapping"`
	GroupName    string            `json:"group_name"`
	Priority     int               `json:"priority"`
	Weight       int               `json:"weight"`
	Status       string            `json:"status"`
	CreatedAt    time.Time         `json:"created_at"`
	UpdatedAt    time.Time         `json:"updated_at"`
	Credentials  []Credential      `json:"credentials,omitempty"`
}

// Credential contains only the existing privileged management representation.
// Ciphertext, key digests and internal health concurrency state are not exposed.
type Credential struct {
	ID            uint      `json:"id"`
	ChannelID     uint      `json:"channel_id"`
	Name          string    `json:"name"`
	APIKey        string    `json:"api_key"`
	Weight        int       `json:"weight"`
	Status        string    `json:"status"`
	FailCount     int       `json:"fail_count"`
	CooldownUntil int64     `json:"cooldown_until"`
	LastUsedAt    int64     `json:"last_used_at"`
	LastError     string    `json:"last_error"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

type ModelPrice struct {
	Model            string    `json:"model"`
	InputPricePer1K  float64   `json:"input_price_per_1k"`
	OutputPricePer1K float64   `json:"output_price_per_1k"`
	UpdatedAt        time.Time `json:"updated_at"`
}

// SavedError marks a committed mutation whose management refresh failed.
// Unwrap preserves the original cause for diagnostics; consumers must use Change
// and publication state to decide whether a save may be retried.
type SavedError struct{ Cause error }

func (e *SavedError) Error() string {
	return "configuration saved, but management cache refresh failed"
}
func (e *SavedError) Unwrap() error { return e.Cause }

func (in *PriceInput) Validate() error {
	for _, price := range []float64{in.InputPricePer1K, in.OutputPricePer1K} {
		if math.IsNaN(price) || math.IsInf(price, 0) || price < 0 || price > MaxPricePer1K {
			return fmt.Errorf("price must be finite and between 0 and 1000000000 per 1K tokens")
		}
	}
	return nil
}

type Repository interface {
	ListChannels() []Channel
	GetChannel(uint) (*Channel, error)
	CreateChannel(*ChannelInput) (*Channel, error)
	UpdateChannel(uint, *ChannelInput) (*Channel, error)
	DeleteChannel(uint) error
	ListCredentials(uint) []Credential
	CreateCredential(uint, *CredentialInput) (*Credential, error)
	UpdateCredential(uint, *CredentialInput) (*Credential, error)
	DeleteCredential(uint) error
	SetCredentialStatus(uint, string) (*Credential, error)
	ModelPrices() ([]ModelPrice, error)
	GroupRates() (map[string]float64, error)
	UpsertModelPrice(*ModelPrice) error
	DeleteModelPrice(string) error
	SetGroupRates(map[string]float64) error
}

// Service serializes each save/publication pair across resource types. Apply
// retries a desired revision and must never replay the underlying mutation.
type Service interface {
	ListChannels() []Channel
	GetChannel(uint) (*Channel, error)
	CreateChannel(*ChannelInput) (*Channel, Change, error)
	UpdateChannel(uint, *ChannelInput) (*Channel, Change, error)
	DeleteChannel(uint) (Change, error)
	ListCredentials(uint) []Credential
	CreateCredential(uint, *CredentialInput) (*Credential, Change, error)
	UpdateCredential(uint, *CredentialInput) (*Credential, Change, error)
	DeleteCredential(uint) (Change, error)
	SetCredentialStatus(uint, string) (*Credential, Change, error)
	Prices() ([]ModelPrice, map[string]float64, error)
	UpsertPrice(string, *PriceInput) (*ModelPrice, Change, error)
	DeletePrice(string) (Change, error)
	SetGroupRates(map[string]float64) (Change, error)
	Status() ConfigStatus
	Apply() ConfigStatus
}
