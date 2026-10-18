package persistence

import (
	control "minigateway/internal/foundation/contracts/control/v1"
	identity "minigateway/internal/foundation/contracts/identity/v1"
	"minigateway/internal/model"
	"minigateway/internal/store"
	"time"
)

type ControlBackend interface {
	Login(string, string) (string, *model.User, error)
	Logout(string) error
	SessionUser(string) (*model.User, error)
	ListUsers() ([]model.User, error)
	GetUser(uint) (*model.User, error)
	CreateUser(*store.UserInput) (*model.User, error)
	UpdateUser(uint, *store.UserInput) (*model.User, error)
	SystemPolicy() (identity.Policy, error)
	SetSystemPolicy(identity.Policy) error
	ListTokens() []model.Token
	IssueToken(uint, *store.TokenInput) (*model.Token, error)
	UpdateToken(uint, *store.TokenInput) (*model.Token, error)
	DeleteToken(uint) error
	SetTokenQuota(uint, float64) (*model.Token, error)
	ClaimToken(uint, uint) (*model.Token, error)
	OwnerTokens(uint) ([]model.Token, error)
	OwnedToken(uint, uint) (*model.Token, error)
	UpdateOwnedToken(uint, uint, *store.TokenInput) (*model.Token, error)
	DeleteOwnedToken(uint, uint) error
	ListLogs(store.LogQuery) ([]model.RequestLog, error)
	UsageForUser(uint) (*store.LogSummary, error)
	LogSummary(time.Time) (*store.LogSummary, error)
	StatsByModel(time.Time) ([]store.ModelStat, error)
	StatsByChannel(time.Time) ([]store.ChannelStat, error)
	Abilities() ([]model.Ability, error)
	Channels() []model.Channel
}
type controlPort struct{ backend ControlBackend }

func Control(backend ControlBackend) control.Repository {
	if absent(backend) {
		return nil
	}
	return &controlPort{backend}
}
func UserProjection(in model.User) control.User {
	return control.User{ID: in.ID, Username: in.Username, Role: in.Role, ModelMode: in.ModelMode, Models: cloneStrings(in.Models), RPMLimit: in.RPMLimit, TPMLimit: in.TPMLimit, ConcurrencyLimit: in.ConcurrencyLimit, Quota: in.Quota, UsedQuota: in.UsedQuota, Status: in.Status, CreatedAt: in.CreatedAt, UpdatedAt: in.UpdatedAt}
}
func TokenProjection(in model.Token) control.Token {
	return control.Token{ID: in.ID, UserID: in.UserID, Name: in.Name, Key: in.Key, KeyPrefix: in.KeyPrefix, ModelMode: in.ModelMode, ConcurrencyLimit: in.ConcurrencyLimit, Models: cloneStrings(in.Models), Quota: in.Quota, UsedQuota: in.UsedQuota, RPMLimit: in.RPMLimit, TPMLimit: in.TPMLimit, ExpiresAt: in.ExpiresAt, Status: in.Status, CreatedAt: in.CreatedAt, UpdatedAt: in.UpdatedAt}
}
func RequestLogProjection(in model.RequestLog) control.RequestLog {
	return control.RequestLog{ID: in.ID, RequestID: in.RequestID, UserID: in.UserID, TokenID: in.TokenID, ChannelID: in.ChannelID, CredentialID: in.CredentialID, OriginModel: in.OriginModel, ActualModel: in.ActualModel, IsStream: in.IsStream, PromptTokens: in.PromptTokens, CompletionTokens: in.CompletionTokens, TotalTokens: in.TotalTokens, Cost: in.Cost, PriceVersion: in.PriceVersion, BillingID: in.BillingID, BillingState: in.BillingState, LatencyMs: in.LatencyMs, Status: in.Status, ErrorMessage: in.ErrorMessage, IsEstimated: in.IsEstimated, PriceMissing: in.PriceMissing, CreatedAt: in.CreatedAt}
}
func AbilityProjection(in model.Ability) control.Ability {
	return control.Ability{GroupName: in.GroupName, Model: in.Model, ChannelID: in.ChannelID, Enabled: in.Enabled, Priority: in.Priority, Weight: in.Weight}
}
func userInput(in *control.UserInput) *store.UserInput {
	if in == nil {
		return nil
	}
	out := store.UserInput(*in)
	out.Models = cloneStrings(in.Models)
	return &out
}
func tokenInput(in *control.TokenInput) *store.TokenInput {
	if in == nil {
		return nil
	}
	out := store.TokenInput(*in)
	out.Models = cloneStrings(in.Models)
	return &out
}
func userResult(in *model.User, err error) (*control.User, error) {
	if in == nil {
		return nil, err
	}
	out := UserProjection(*in)
	return &out, err
}
func tokenResult(in *model.Token, err error) (*control.Token, error) {
	if in == nil {
		return nil, err
	}
	out := TokenProjection(*in)
	return &out, err
}
func (p *controlPort) Login(name, password string) (string, *control.User, error) {
	secret, in, err := p.backend.Login(name, password)
	user, err := userResult(in, err)
	return secret, user, err
}
func (p *controlPort) Logout(secret string) error { return p.backend.Logout(secret) }
func (p *controlPort) SessionUser(secret string) (*control.User, error) {
	return userResult(p.backend.SessionUser(secret))
}
func (p *controlPort) GetUser(id uint) (*control.User, error) {
	return userResult(p.backend.GetUser(id))
}
func (p *controlPort) CreateUser(in *control.UserInput) (*control.User, error) {
	return userResult(p.backend.CreateUser(userInput(in)))
}
func (p *controlPort) UpdateUser(id uint, in *control.UserInput) (*control.User, error) {
	return userResult(p.backend.UpdateUser(id, userInput(in)))
}
func (p *controlPort) SystemPolicy() (identity.Policy, error) {
	out, err := p.backend.SystemPolicy()
	out.Models = append([]string(nil), out.Models...)
	return out, err
}
func (p *controlPort) SetSystemPolicy(in identity.Policy) error {
	in.Models = cloneStrings(in.Models)
	return p.backend.SetSystemPolicy(in)
}
func (p *controlPort) IssueToken(owner uint, in *control.TokenInput) (*control.Token, error) {
	return tokenResult(p.backend.IssueToken(owner, tokenInput(in)))
}
func (p *controlPort) UpdateToken(id uint, in *control.TokenInput) (*control.Token, error) {
	return tokenResult(p.backend.UpdateToken(id, tokenInput(in)))
}
func (p *controlPort) DeleteToken(id uint) error { return p.backend.DeleteToken(id) }
func (p *controlPort) SetTokenQuota(id uint, quota float64) (*control.Token, error) {
	return tokenResult(p.backend.SetTokenQuota(id, quota))
}
func (p *controlPort) ClaimToken(id, owner uint) (*control.Token, error) {
	return tokenResult(p.backend.ClaimToken(id, owner))
}
func (p *controlPort) OwnedToken(owner, id uint) (*control.Token, error) {
	return tokenResult(p.backend.OwnedToken(owner, id))
}
func (p *controlPort) UpdateOwnedToken(owner, id uint, in *control.TokenInput) (*control.Token, error) {
	return tokenResult(p.backend.UpdateOwnedToken(owner, id, tokenInput(in)))
}
func (p *controlPort) DeleteOwnedToken(owner, id uint) error {
	return p.backend.DeleteOwnedToken(owner, id)
}
func (p *controlPort) ListUsers() ([]control.User, error) {
	rows, err := p.backend.ListUsers()
	if rows == nil {
		return nil, err
	}
	out := make([]control.User, len(rows))
	for i, r := range rows {
		out[i] = UserProjection(r)
	}
	return out, err
}
func (p *controlPort) OwnerTokens(owner uint) ([]control.Token, error) {
	rows, err := p.backend.OwnerTokens(owner)
	if rows == nil {
		return nil, err
	}
	out := make([]control.Token, len(rows))
	for i, r := range rows {
		out[i] = TokenProjection(r)
	}
	return out, err
}
func (p *controlPort) ListLogs(q control.LogQuery) ([]control.RequestLog, error) {
	rows, err := p.backend.ListLogs(store.LogQuery(q))
	if rows == nil {
		return nil, err
	}
	out := make([]control.RequestLog, len(rows))
	for i, r := range rows {
		out[i] = RequestLogProjection(r)
	}
	return out, err
}
func (p *controlPort) Abilities() ([]control.Ability, error) {
	rows, err := p.backend.Abilities()
	if rows == nil {
		return nil, err
	}
	out := make([]control.Ability, len(rows))
	for i, r := range rows {
		out[i] = AbilityProjection(r)
	}
	return out, err
}
func (p *controlPort) ListTokens() []control.Token {
	rows := p.backend.ListTokens()
	if rows == nil {
		return nil
	}
	out := make([]control.Token, len(rows))
	for i, r := range rows {
		out[i] = TokenProjection(r)
	}
	return out
}
func (p *controlPort) ModelChannels() []control.ModelChannel {
	rows := p.backend.Channels()
	if rows == nil {
		return nil
	}
	out := make([]control.ModelChannel, len(rows))
	for i, r := range rows {
		out[i] = control.ModelChannel{Enabled: r.IsEnabled(), Models: append([]string(nil), r.Models...)}
	}
	return out
}
func (p *controlPort) UsageForUser(owner uint) (*control.LogSummary, error) {
	in, err := p.backend.UsageForUser(owner)
	if in == nil {
		return nil, err
	}
	out := control.LogSummary(*in)
	return &out, err
}
func (p *controlPort) LogSummary(since time.Time) (*control.LogSummary, error) {
	in, err := p.backend.LogSummary(since)
	if in == nil {
		return nil, err
	}
	out := control.LogSummary(*in)
	return &out, err
}
func (p *controlPort) StatsByModel(since time.Time) ([]control.ModelStat, error) {
	rows, err := p.backend.StatsByModel(since)
	if rows == nil {
		return nil, err
	}
	out := make([]control.ModelStat, len(rows))
	for i, r := range rows {
		out[i] = control.ModelStat(r)
	}
	return out, err
}
func (p *controlPort) StatsByChannel(since time.Time) ([]control.ChannelStat, error) {
	rows, err := p.backend.StatsByChannel(since)
	if rows == nil {
		return nil, err
	}
	out := make([]control.ChannelStat, len(rows))
	for i, r := range rows {
		out[i] = control.ChannelStat(r)
	}
	return out, err
}

func cloneStrings(in []string) []string {
	if in == nil {
		return nil
	}
	return append([]string{}, in...)
}
