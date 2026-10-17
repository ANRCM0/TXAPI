// Package openai 实现 OpenAI 兼容协议的适配器。
//
// 重要：这个适配器不只服务 OpenAI 本身。所有「OpenAI 兼容」的厂商
// （DeepSeek、Moonshot、SiliconFlow、Ollama、vLLM…）都复用它，
// 只需要在配置里改 base_url 和模型名。
// 这是比 one-api 更省事的地方：它有 40 个适配器目录，其中一半
// 只是 base_url 不同，没必要各写一份。
//
// P4 起，这个包同时承担两个角色：
//  1. 实现 adaptor.Adaptor（HTTP 层转发），行为与 P1 完全一致
//  2. 实现 adaptor.ProtocolAdaptor（协议层转换），作为规范格式的
//     「本家实现」——规范格式就是以 OpenAI 格式为基准定义的，
//     所以这里的转换基本是直通，主要价值是让 anthropic/gemini
//     客户端也能用上同一套枢纽逻辑。
package openai

import (
	"encoding/json"
	"net/http"
	"strings"

	"minigateway/internal/protocol/adaptor"
	"minigateway/internal/protocol/meta"
)

// Adaptor 是 OpenAI 兼容协议的适配器实现。
type Adaptor struct{}

func (*Adaptor) Supports(r adaptor.Requirements) bool {
	return r.Operation == adaptor.Chat || (r.Operation == adaptor.Embeddings && !r.Stream && !r.Tools)
}

// 编译期断言：同时满足两个接口。
var (
	_ adaptor.Adaptor         = (*Adaptor)(nil)
	_ adaptor.ProtocolAdaptor = (*Adaptor)(nil)
	_ adaptor.Capability      = (*Adaptor)(nil)
)

// GetRequestURL 拼出上游地址。
func (a *Adaptor) GetRequestURL(m *meta.Meta) (string, error) {
	base := strings.TrimSuffix(m.BaseURL, "/")
	switch m.Mode {
	case meta.ModeEmbeddings:
		return base + "/v1/embeddings", nil
	default:
		return base + "/v1/chat/completions", nil
	}
}

// SetupRequestHeader 设置鉴权头。
// OpenAI 协议是标准的 Authorization: Bearer <key>。
func (a *Adaptor) SetupRequestHeader(req *http.Request, m *meta.Meta) error {
	req.Header.Set("Authorization", "Bearer "+m.APIKey)
	return nil
}

// ConvertRequest 处理模型名映射。
//
// OpenAI 协议本身不需要格式转换，但需要把客户端写的模型名
// 换成上游真实模型名。做法是只改动 model 字段，
// 其余字段原样保留——这样上游新增的参数（比如新出的 tool 类型）
// 不会被网关吃掉。
func (a *Adaptor) ConvertRequest(body []byte, m *meta.Meta) ([]byte, error) {
	if m.OriginModel == m.ActualModel {
		return body, nil
	}
	// 用 map 而不是结构体：结构体会把未声明的字段丢掉，
	// 而网关必须做透明代理，不能丢字段。
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(body, &payload); err != nil {
		// 解析失败时不做转换，原样透传，把错误交给上游去报，
		// 避免网关自己造出一个语义不同的请求。
		return body, nil
	}
	encoded, err := json.Marshal(m.ActualModel)
	if err != nil {
		return body, nil
	}
	payload["model"] = encoded
	return json.Marshal(payload)
}

func (a *Adaptor) GetChannelName() string { return "openai" }
