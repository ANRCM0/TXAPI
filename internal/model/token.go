package model

import (
	"minigateway/internal/access"
	"time"

	"gorm.io/gorm"
)

// 用户角色。自用阶段只有一个 admin，但表先建好，为多用户留口子（PLAN 定位：
// 「自用为主、留多用户扩展口」）。
const (
	RoleAdmin = "admin"
	RoleUser  = "user"
)

// 账号状态。
const (
	UserStatusActive   = "active"
	UserStatusDisabled = "disabled"
)

// User is a login identity with explicit role, status and model policy.
//
// 注意与 Token 的区别：User 是「人」，Token 是「人手里的钥匙」。
// 一个 User 可以有多把 Token（不同客户端各一把，便于单独吊销）。
type User struct {
	ID               uint       `gorm:"primaryKey" json:"id"`
	Username         string     `gorm:"uniqueIndex;size:64;not null" json:"username"`
	PasswordHash     string     `gorm:"size:255;not null" json:"-"`
	Role             string     `gorm:"size:16;not null;default:user" json:"role"`
	ModelMode        string     `gorm:"not null;default:allowlist" json:"model_mode"`
	Models           StringList `gorm:"type:text" json:"models"`
	RPMLimit         int        `gorm:"not null;default:0" json:"rpm_limit"`
	TPMLimit         int        `gorm:"not null;default:0" json:"tpm_limit"`
	ConcurrencyLimit int        `gorm:"not null;default:0" json:"concurrency_limit"`
	// Quota 是总额度，UsedQuota 是已用额度。自用阶段可都设 0（不限）。
	Quota     float64 `gorm:"not null;default:0" json:"quota"`
	UsedQuota float64 `gorm:"not null;default:0" json:"used_quota"`
	Status    string  `gorm:"size:16;not null;default:active" json:"status"`

	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
	DeletedAt gorm.DeletedAt `gorm:"index" json:"-"`
}

// Token 是客户端访问网关的凭证。
//
// 和 Channel 的 APIKey 是两个完全不同的东西：Token 是「客户端 → 网关」的钥匙，
// APIKey 是「网关 → 上游」的钥匙。混淆这两个是网关新手最常见的 bug。
type Token struct {
	ID     uint   `gorm:"primaryKey" json:"id"`
	UserID uint   `gorm:"index;not null;default:0" json:"user_id"`
	Name   string `gorm:"size:128;not null;default:''" json:"name"`
	// Key is transient creation output. The compatibility database column is empty; KeyHash verifies calls.
	Key              string `gorm:"size:255;not null;default:''" json:"key,omitempty"`
	KeyHash          string `gorm:"size:64;index" json:"-"`
	KeyPrefix        string `gorm:"size:24" json:"key_prefix"`
	ModelMode        string `gorm:"not null;default:allowlist" json:"model_mode"`
	ConcurrencyLimit int    `gorm:"not null;default:0" json:"concurrency_limit"`
	// Models 限制这把令牌能访问哪些模型；空数组表示不限制。
	// 支持 "*" 通配符。
	Models StringList `gorm:"type:text" json:"models"`
	// Quota/UsedQuota 与 User 上的同名额度配合使用：先扣令牌，再扣用户（P5 计费）。
	Quota     float64 `gorm:"not null;default:0" json:"quota"`
	UsedQuota float64 `gorm:"not null;default:0" json:"used_quota"`
	// RPMLimit/TPMLimit 是限流阈值（每分钟请求数 / 每分钟 token 数）。
	//
	// 0 表示不限——和 Quota 的 0=不限 保持同一套语义，免得调用方
	// 要在两种「没配」之间做区分。
	//
	// 为什么限流和额度是两套东西：额度是「响应后结算」的，防不住
	// 「1000 个并发在结算前全部打到上游」——结算发生在响应之后，
	// 那之前的上游调用已经烧掉了真实的 Key。限流是**请求前**的闸门，
	// 两者互补，谁也替代不了谁。
	RPMLimit int `gorm:"not null;default:0" json:"rpm_limit"`
	TPMLimit int `gorm:"not null;default:0" json:"tpm_limit"`
	// ExpiresAt 是过期时间，Unix 秒；0 表示永不过期。
	// 用 int64 而不是 time.Time：NULL 时间在 Go 里是零值，和「1970 年」无法区分。
	ExpiresAt int64  `gorm:"not null;default:0" json:"expires_at"`
	Status    string `gorm:"size:16;not null;default:active" json:"status"`

	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
	DeletedAt gorm.DeletedAt `gorm:"index" json:"-"`
}

// 令牌状态。
const (
	TokenStatusActive   = "active"
	TokenStatusDisabled = "disabled"
)

// TokenView 是「鉴权时需要的令牌信息」的投影。
//
// 为什么不直接传 *Token：中间件只需要 id/name/models 几个字段，
// 传整个实体会把 quota、used_quota、时间戳这些敏感内部状态暴露给 HTTP 层。
// 用投影做边界，以后给令牌加字段也不用改中间件。
//
// UserID 必须带上：P5 的额度是「先扣令牌、再扣用户」的双层结构，
// 没有它结算时找不到要扣哪个用户。
type TokenView struct {
	Policy access.Policy
	ID     uint
	UserID uint
	Name   string
	Models []string
	// RPMLimit/TPMLimit 是限流阈值，0 表示不限。
	// 限流中间件挂在 Auth 之后、只看上下文，所以阈值必须随投影一起
	// 带过来——让它自己回库查会把一次请求变成两次查库。
	RPMLimit             int
	TPMLimit             int
	UserRPMLimit         int
	UserConcurrencyLimit int
	KeyConcurrencyLimit  int
}

// IsActive 判断令牌当前是否可用（启用且未过期）。
func (t *Token) IsActive(now time.Time) bool {
	if t.Status != TokenStatusActive {
		return false
	}
	return t.ExpiresAt == 0 || t.ExpiresAt > now.Unix()
}

// View 生成鉴权用的投影。
func (t *Token) View() TokenView {
	return TokenView{
		ID: t.ID, UserID: t.UserID, Name: t.Name, Models: []string(t.Models),
		RPMLimit: t.RPMLimit, TPMLimit: t.TPMLimit,
	}
}

func (u *User) Policy() access.Policy {
	return access.Policy{Mode: u.ModelMode, Models: []string(u.Models)}
}
func (t *Token) Policy() access.Policy {
	return access.Policy{Mode: t.ModelMode, Models: []string(t.Models)}
}

// Session contains only a verification digest. It is distinct from an API Key.
type Session struct {
	Hash      string `gorm:"primaryKey;size:64" json:"-"`
	UserID    uint   `gorm:"index;not null" json:"user_id"`
	ExpiresAt int64  `gorm:"index;not null" json:"expires_at"`
}
type SchemaMigration struct {
	Version   int `gorm:"primaryKey"`
	AppliedAt time.Time
}
