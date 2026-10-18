package persistence

import (
	"errors"
	"maps"
	cfg "minigateway/internal/foundation/contracts/configuration/v1"
	"minigateway/internal/model"
	"minigateway/internal/store"
)

type ConfigurationBackend interface {
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

type configurationPort struct{ backend ConfigurationBackend }

func Configuration(backend ConfigurationBackend) cfg.Repository {
	if absent(backend) {
		return nil
	}
	return &configurationPort{backend}
}
func configurationError(err error) error {
	var saved *store.SavedConfigurationError
	if errors.As(err, &saved) {
		return &cfg.SavedError{Cause: err}
	}
	return err
}
func CredentialSnapshot(in model.Credential) cfg.Credential {
	return cfg.Credential{ID: in.ID, ChannelID: in.ChannelID, Name: in.Name, APIKey: in.APIKey, Weight: in.Weight,
		Status: in.Status, FailCount: in.FailCount, CooldownUntil: in.CooldownUntil, LastUsedAt: in.LastUsedAt,
		LastError: in.LastError, CreatedAt: in.CreatedAt, UpdatedAt: in.UpdatedAt}
}
func ChannelSnapshot(in model.Channel) cfg.Channel {
	out := cfg.Channel{ID: in.ID, Name: in.Name, Type: in.Type, BaseURL: in.BaseURL, Models: append([]string(nil), in.Models...),
		ModelMapping: maps.Clone(in.ModelMapping), GroupName: in.GroupName, Priority: in.Priority, Weight: in.Weight,
		Status: in.Status, CreatedAt: in.CreatedAt, UpdatedAt: in.UpdatedAt}
	if in.Models != nil && out.Models == nil {
		out.Models = make([]string, 0)
	}
	if in.Credentials != nil {
		out.Credentials = make([]cfg.Credential, len(in.Credentials))
		for i, c := range in.Credentials {
			out.Credentials[i] = CredentialSnapshot(c)
		}
	}
	return out
}
func channelResult(in *model.Channel, err error) (*cfg.Channel, error) {
	if in == nil {
		return nil, configurationError(err)
	}
	out := ChannelSnapshot(*in)
	return &out, configurationError(err)
}
func credentialResult(in *model.Credential, err error) (*cfg.Credential, error) {
	if in == nil {
		return nil, configurationError(err)
	}
	out := CredentialSnapshot(*in)
	return &out, configurationError(err)
}
func channelInput(in *cfg.ChannelInput) *store.ChannelInput {
	if in == nil {
		return nil
	}
	out := store.ChannelInput(*in)
	out.Models = append([]string(nil), in.Models...)
	out.ModelMapping = maps.Clone(in.ModelMapping)
	return &out
}
func credentialInput(in *cfg.CredentialInput) *store.CredentialInput {
	if in == nil {
		return nil
	}
	out := store.CredentialInput(*in)
	return &out
}
func (p *configurationPort) ListChannels() []cfg.Channel {
	rows := p.backend.ListChannels()
	if rows == nil {
		return nil
	}
	out := make([]cfg.Channel, len(rows))
	for i, r := range rows {
		out[i] = ChannelSnapshot(r)
	}
	return out
}
func (p *configurationPort) GetChannel(id uint) (*cfg.Channel, error) {
	return channelResult(p.backend.GetChannel(id))
}
func (p *configurationPort) CreateChannel(in *cfg.ChannelInput) (*cfg.Channel, error) {
	return channelResult(p.backend.CreateChannel(channelInput(in)))
}
func (p *configurationPort) UpdateChannel(id uint, in *cfg.ChannelInput) (*cfg.Channel, error) {
	return channelResult(p.backend.UpdateChannel(id, channelInput(in)))
}
func (p *configurationPort) DeleteChannel(id uint) error {
	return configurationError(p.backend.DeleteChannel(id))
}
func (p *configurationPort) ListCredentials(id uint) []cfg.Credential {
	rows := p.backend.ListCredentials(id)
	if rows == nil {
		return nil
	}
	out := make([]cfg.Credential, len(rows))
	for i, r := range rows {
		out[i] = CredentialSnapshot(r)
	}
	return out
}
func (p *configurationPort) CreateCredential(id uint, in *cfg.CredentialInput) (*cfg.Credential, error) {
	return credentialResult(p.backend.CreateCredential(id, credentialInput(in)))
}
func (p *configurationPort) UpdateCredential(id uint, in *cfg.CredentialInput) (*cfg.Credential, error) {
	return credentialResult(p.backend.UpdateCredential(id, credentialInput(in)))
}
func (p *configurationPort) DeleteCredential(id uint) error {
	return configurationError(p.backend.DeleteCredential(id))
}
func (p *configurationPort) SetCredentialStatus(id uint, status string) (*cfg.Credential, error) {
	return credentialResult(p.backend.SetCredentialStatus(id, status))
}
func priceSnapshot(in model.ModelPrice) cfg.ModelPrice {
	return cfg.ModelPrice{Model: in.Model, InputPricePer1K: in.InputPricePer1K, OutputPricePer1K: in.OutputPricePer1K, UpdatedAt: in.UpdatedAt}
}
func (p *configurationPort) ModelPrices() ([]cfg.ModelPrice, error) {
	rows, err := p.backend.ModelPrices()
	if rows == nil {
		return nil, err
	}
	out := make([]cfg.ModelPrice, len(rows))
	for i, r := range rows {
		out[i] = priceSnapshot(r)
	}
	return out, err
}
func (p *configurationPort) GroupRates() (map[string]float64, error) {
	out, err := p.backend.GroupRates()
	return maps.Clone(out), err
}
func (p *configurationPort) UpsertModelPrice(in *cfg.ModelPrice) error {
	if in == nil {
		return p.backend.UpsertModelPrice(nil)
	}
	out := model.ModelPrice{Model: in.Model, InputPricePer1K: in.InputPricePer1K, OutputPricePer1K: in.OutputPricePer1K, UpdatedAt: in.UpdatedAt}
	err := p.backend.UpsertModelPrice(&out)
	*in = priceSnapshot(out)
	return configurationError(err)
}
func (p *configurationPort) DeleteModelPrice(name string) error {
	return configurationError(p.backend.DeleteModelPrice(name))
}
func (p *configurationPort) SetGroupRates(in map[string]float64) error {
	return configurationError(p.backend.SetGroupRates(maps.Clone(in)))
}
