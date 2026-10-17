// Package management owns control-plane use cases independently of HTTP.
package management

import (
	"errors"
	"log/slog"
	"sync"
	"time"

	"minigateway/internal/model"
	"minigateway/internal/store"
)

// ConfigurationStore is the persistence consumed by configuration use cases.
// Store owns validation, transactions and cache refresh; this service owns
// save/publication ordering and distinguishes a committed save from a rollback.
type ConfigurationStore interface {
	ListChannels() []model.Channel
	GetChannel(uint) (*model.Channel, error)
	CreateChannel(*store.ChannelInput) (*model.Channel, error)
	UpdateChannel(uint, *store.ChannelInput) (*model.Channel, error)
	DeleteChannel(uint) error
	ListCredentials(uint) []model.Credential
	CreateCredential(uint, *store.CredentialInput) (*model.Credential, error)
	UpdateCredential(uint, *store.CredentialInput) (*model.Credential, error)
	DeleteCredential(uint) error
	SetCredentialStatus(uint, string) (*model.Credential, error)
	ModelPrices() ([]model.ModelPrice, error)
	GroupRates() (map[string]float64, error)
	UpsertModelPrice(*model.ModelPrice) error
	DeleteModelPrice(string) error
	SetGroupRates(map[string]float64) error
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

// Change describes persistence separately from runtime publication, including
// an error returned after the database commit. HTTP status does not encode this.
type Change struct {
	Saved       bool
	Publication PublicationStatus
}

type publication struct {
	mu     sync.Mutex
	status PublicationStatus
	apply  func() error
}

func (p *publication) publish(changed bool) PublicationStatus {
	p.mu.Lock()
	defer p.mu.Unlock()
	if changed {
		p.status.DesiredRevision++
	}
	p.status.UpdatedAt = time.Now().UTC()
	if p.apply != nil {
		if err := p.apply(); err != nil {
			p.status.LastError = "Publication failed. Previous snapshot retained; check server logs and retry."
			var public interface{ PublicReason() string }
			if errors.As(err, &public) {
				p.status.LastError = public.PublicReason() + ". Previous snapshot retained."
			}
			slog.Error("configuration publication failed", "revision", p.status.DesiredRevision, "err", err)
			return p.status
		}
	}
	p.status.AppliedRevision = p.status.DesiredRevision
	p.status.LastError = ""
	return p.status
}

func (p *publication) snapshot() PublicationStatus {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.status
}

// Configuration serializes persistence and runtime publication across all
// channel, credential and price writes. It is shared by legacy and session APIs.
type Configuration struct {
	mu       sync.Mutex
	store    ConfigurationStore
	epoch    string
	channels publication
	prices   publication
}

func NewConfiguration(st ConfigurationStore, channels, prices func() error) *Configuration {
	s := &Configuration{store: st, epoch: time.Now().UTC().Format(time.RFC3339Nano)}
	s.channels.apply, s.prices.apply = channels, prices
	s.channels.publish(true)
	s.prices.publish(true)
	return s
}

func (s *Configuration) status() ConfigStatus {
	return ConfigStatus{Epoch: s.epoch, Channels: s.channels.snapshot(), Prices: s.prices.snapshot()}
}

func (s *Configuration) Status() ConfigStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.status()
}

// Apply retries the saved desired revisions; it never replays a mutation.
func (s *Configuration) Apply() ConfigStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.channels.publish(false)
	s.prices.publish(false)
	return s.status()
}

func finishChange(p *publication, err error) Change {
	if err != nil {
		var committed *store.SavedConfigurationError
		if !errors.As(err, &committed) {
			return Change{}
		}
	}
	return Change{Saved: true, Publication: p.publish(true)}
}

func (s *Configuration) ListChannels() []model.Channel { return s.store.ListChannels() }
func (s *Configuration) GetChannel(id uint) (*model.Channel, error) {
	return s.store.GetChannel(id)
}
func (s *Configuration) ListCredentials(id uint) []model.Credential {
	return s.store.ListCredentials(id)
}

func (s *Configuration) CreateChannel(in *store.ChannelInput) (*model.Channel, Change, error) {
	if in == nil {
		return nil, Change{}, errors.New("channel input is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	channel, err := s.store.CreateChannel(in)
	return channel, finishChange(&s.channels, err), err
}

func (s *Configuration) UpdateChannel(id uint, in *store.ChannelInput) (*model.Channel, Change, error) {
	if in == nil {
		return nil, Change{}, errors.New("channel input is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	channel, err := s.store.UpdateChannel(id, in)
	return channel, finishChange(&s.channels, err), err
}

func (s *Configuration) DeleteChannel(id uint) (Change, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	err := s.store.DeleteChannel(id)
	return finishChange(&s.channels, err), err
}

func (s *Configuration) CreateCredential(id uint, in *store.CredentialInput) (*model.Credential, Change, error) {
	if in == nil {
		return nil, Change{}, errors.New("credential input is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	credential, err := s.store.CreateCredential(id, in)
	return credential, finishChange(&s.channels, err), err
}

func (s *Configuration) UpdateCredential(id uint, in *store.CredentialInput) (*model.Credential, Change, error) {
	if in == nil {
		return nil, Change{}, errors.New("credential input is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	credential, err := s.store.UpdateCredential(id, in)
	return credential, finishChange(&s.channels, err), err
}

func (s *Configuration) DeleteCredential(id uint) (Change, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	err := s.store.DeleteCredential(id)
	return finishChange(&s.channels, err), err
}

func (s *Configuration) SetCredentialStatus(id uint, status string) (*model.Credential, Change, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	credential, err := s.store.SetCredentialStatus(id, status)
	return credential, finishChange(&s.channels, err), err
}

func (s *Configuration) Prices() ([]model.ModelPrice, map[string]float64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	prices, err := s.store.ModelPrices()
	if err != nil {
		return nil, nil, err
	}
	rates, err := s.store.GroupRates()
	return prices, rates, err
}

func (s *Configuration) UpsertPrice(name string, in *store.PriceInput) (*model.ModelPrice, Change, error) {
	if name == "" {
		return nil, Change{}, errors.New("model name is required")
	}
	if in == nil {
		return nil, Change{}, errors.New("price input is required")
	}
	if err := in.Validate(); err != nil {
		return nil, Change{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	price := &model.ModelPrice{Model: name, InputPricePer1K: in.InputPricePer1K, OutputPricePer1K: in.OutputPricePer1K}
	err := s.store.UpsertModelPrice(price)
	return price, finishChange(&s.prices, err), err
}

func (s *Configuration) DeletePrice(name string) (Change, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	err := s.store.DeleteModelPrice(name)
	return finishChange(&s.prices, err), err
}

func (s *Configuration) SetGroupRates(rates map[string]float64) (Change, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	err := s.store.SetGroupRates(rates)
	return finishChange(&s.prices, err), err
}
