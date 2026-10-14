// Package meta 承载「一次转发所需的全部上下文」。
//
// Meta 是 Adaptor 的输入参数。把渠道信息、模型映射、是否流式
// 全部塞进一个结构体，好处是 Adaptor 接口的每个方法签名都很干净，
// 新增一个字段不用改接口。
package meta

// Meta 描述本次转发的目标渠道和请求特征。
type Meta struct {
	ChannelID   int
	ChannelName string
	// ChannelType 决定用哪个 Adaptor，例如 "openai"。
	ChannelType string
	BaseURL     string
	// APIKey 是上游渠道的 key，注意不是客户端令牌。
	APIKey string
	// OriginModel 是客户端请求的模型名。
	OriginModel string
	// ActualModel 是映射后真正发给上游的模型名。
	// 没配 model_mapping 时两者相同。
	ActualModel string
	IsStream    bool
	// Mode 是转发模式（对话/向量/图片…），决定请求路径和响应解析方式。
	Mode int
	// CredentialID 是本次实际使用的凭据 ID，记账时要带上它——
	// 否则「哪个 Key 出的问题」在日志里查不出来。
	// P2 由 registry 在选 Key 时确定，P3 换成调度器后由调度器回填。
	CredentialIDValue uint
}

// CredentialID 返回本次使用的凭据 ID；未设置时返回 (0, false)。
func (m *Meta) CredentialID() (uint, bool) {
	return m.CredentialIDValue, m.CredentialIDValue != 0
}

// 转发模式。目前只实现对话，留出常量方便后续扩展。
const (
	ModeChatCompletions = iota + 1
	ModeEmbeddings
)
