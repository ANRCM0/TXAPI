package model

import (
	"time"

	"gorm.io/gorm"
)

// 渠道状态。用字符串而不是布尔/整数：数据库被人直接打开时一眼能看懂，
// 排查线上问题时不用背「status=2 是什么意思」。
const (
	StatusEnabled  = "enabled"
	StatusDisabled = "disabled"
)

// 凭据状态，PLAN「3.3 凭据池调度」里的状态机落点。
// P2 只用到 active/disabled，cooldown/banned 由 P3 的调度器驱动，
// 常量先定义好，避免后续阶段改数据库语义。
const (
	CredentialActive   = "active"
	CredentialCooldown = "cooldown"
	CredentialBanned   = "banned"
	CredentialDisabled = "disabled"
)

// Channel 是「上游服务商配置」，不是「一个 Key」。
//
// 这是本项目和 One API 最本质的区别：One API 里渠道 == 一个 Key，
// 想用 5 个 Key 就得建 5 个渠道，无法表达「这 5 个 Key 是同一家的、应该一起调度」。
// 这里渠道只描述「这个上游在哪、支持什么模型」，Key 全部挂在 credentials 表上。
type Channel struct {
	ID   uint   `gorm:"primaryKey" json:"id"`
	Name string `gorm:"uniqueIndex;size:128;not null" json:"name"`
	// Type 决定用哪个协议适配器，例如 "openai"。
	Type string `gorm:"size:32;not null;default:openai" json:"type"`
	// BaseURL 是上游地址，OpenAI 兼容厂商（DeepSeek/Moonshot/Ollama…）复用同一个适配器。
	BaseURL string `gorm:"size:512;not null" json:"base_url"`
	// Models 是该渠道支持的模型列表，支持 "*" 通配符（兼容任何模型的中转渠道）。
	Models StringList `gorm:"type:text" json:"models"`
	// ModelMapping 把客户端请求的模型名映射成上游真实模型名，例如 {"gpt-4o": "gpt-4o-2024-08-06"}。
	ModelMapping StringMap `gorm:"type:text" json:"model_mapping"`
	// GroupName 是渠道分组，用于分组倍率和权限控制（P5 计费用）。
	GroupName string `gorm:"size:64;not null;default:default;index" json:"group_name"`
	// Priority 是优先级，数值越大越先被选中；相等时才在组内做加权随机。
	// 这就是「主备渠道」的表达方式——备份渠道不该抢主力渠道的流量。
	Priority int `gorm:"not null;default:0;index" json:"priority"`
	// Weight 是同级渠道之间的权重，越大被选中概率越高。
	Weight int    `gorm:"not null;default:1" json:"weight"`
	Status string `gorm:"size:16;not null;default:enabled" json:"status"`

	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
	DeletedAt gorm.DeletedAt `gorm:"index" json:"-"`

	// Credentials 是该渠道下的凭据池。这里建关联只是为了 Preload 时一次读出来，
	// 写入路径全部走显式事务（见 store 包），不依赖 GORM 的级联行为。
	Credentials []Credential `gorm:"foreignKey:ChannelID" json:"credentials,omitempty"`
}

// IsEnabled 判断渠道是否启用。
func (c *Channel) IsEnabled() bool { return c.Status == StatusEnabled }

// Credential 是渠道下的一个上游 Key，构成凭据池。
//
// P2 阶段转发链路只用「挑一个可用的 Key」；P3 会在这个表上做加权随机、
// 失败指数退避冷却、连续失败拉黑、会话亲和。
type Credential struct {
	ID        uint   `gorm:"primaryKey" json:"id"`
	ChannelID uint   `gorm:"index;not null" json:"channel_id"`
	Name      string `gorm:"size:128;not null;default:''" json:"name"`
	// APIKey 是上游渠道的 key，注意与 tokens 表的客户端 key 完全不同。
	APIKey string `gorm:"size:512;not null" json:"api_key"`
	// Weight 是凭据级权重，调度器在渠道内部的多个 Key 之间分流时使用。
	Weight int    `gorm:"not null;default:1" json:"weight"`
	Status string `gorm:"size:16;not null;default:active" json:"status"`
	// FailCount 是连续失败次数，驱动指数退避和自动拉黑。
	FailCount        int   `gorm:"not null;default:0" json:"fail_count"`
	HealthGeneration int64 `gorm:"not null;default:0" json:"-"`
	HealthObservedAt int64 `gorm:"not null;default:0" json:"-"`
	HealthObserved   bool  `gorm:"-" json:"-"`
	// CooldownUntil 是冷却截止时间，Unix 秒；0 表示不在冷却中。
	CooldownUntil int64 `gorm:"not null;default:0" json:"cooldown_until"`
	// LastUsedAt / LastError 用于排查「这个 Key 到底怎么了」。
	LastUsedAt int64  `gorm:"not null;default:0" json:"last_used_at"`
	LastError  string `gorm:"type:text" json:"last_error"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// IsUsable 判断这个凭据当前能否被调度器选中。
//
// 判断条件与 PLAN「3.3 选择算法」一致：不在冷却期内、且没有被人工停用。
//
// 为什么 cooldown 状态在冷却到期后算可用，而不是死等 status 变回 active：
// 状态从 cooldown 改回 active 是**后台恢复任务**的活（见
// scheduler.StartRecovery），那个任务最坏要晚一个扫描周期（默认 30s）
// 才跑。如果这里死等 status，就会出现「冷却时间早就过了、凭据却还选不中」
// 的空窗；更糟的是恢复任务万一没启动（漏接线、进程刚起还没到第一个
// tick），凭据会**永久**卡在 cooldown——凭据池单向枯竭，网关表现为
// 「没有可用渠道」，而运维看到的是「Key 都是好的」。
//
// 所以可用性由**时间**说了算，status 只用来表达「被人工/自动拉黑」这类
// 不可自愈的状态。恢复任务仍然要跑，它的职责是把 DB 里的状态收拾干净
// （管理界面看得见、重启后不留陈旧状态），不再是可用性的前提。
func (c *Credential) IsUsable(now time.Time) bool {
	// disabled / banned 是「不可自愈」的标记，一律不可用
	if c.Status == CredentialDisabled || c.Status == CredentialBanned {
		return false
	}
	// active 直接可用；cooldown 看冷却时间是否已过
	return c.CooldownUntil == 0 || c.CooldownUntil <= now.Unix()
}

// Ability 是路由加速表，复合主键 (group_name, model, channel_id)。
//
// 作用：选渠道时一次索引命中拿到候选列表，不用去扫描 channels 表再解析
// JSON 模型列表。渠道的 models 支持 "*" 通配，通配渠道在这里存 model="*"，
// 查询时把「精确命中」和「* 命中」两次查询合并即可。
type Ability struct {
	GroupName string `gorm:"primaryKey;size:64" json:"group_name"`
	Model     string `gorm:"primaryKey;size:128;index:idx_ability_model" json:"model"`
	ChannelID uint   `gorm:"primaryKey" json:"channel_id"`
	Enabled   bool   `gorm:"not null;default:true" json:"enabled"`
	Priority  int    `gorm:"not null;default:0" json:"priority"`
	Weight    int    `gorm:"not null;default:1" json:"weight"`
}

// TableName 固定表名。GORM 默认会把 Ability 复数成 abilities，这里显式写出，
// 避免依赖 inflection 的行为在升级时变化。
func (Ability) TableName() string { return "abilities" }
