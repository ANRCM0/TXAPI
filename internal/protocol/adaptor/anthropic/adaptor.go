// Package anthropic 实现 Anthropic Claude 协议适配器。
//
// 包结构：
//   - convert.go  请求/响应转换 + 协议结构体
//   - stream.go   流式事件流翻译（Claude SSE → OpenAI chunk）
//   - adaptor.go  HTTP 层外壳（实现 adaptor.Adaptor）+ 接口绑定
//
// 每条流由 relay 单独创建 translator，状态不存放在共享适配器或全局 map 中。
package anthropic

import (
	"net/http"
	"strings"

	"minigateway/internal/protocol/adaptor"
	"minigateway/internal/protocol/adaptor/openai"
	"minigateway/internal/protocol/canonical"
	"minigateway/internal/protocol/meta"
)

// openaiPA 是入站（客户端侧）协议适配器。
// 客户端固定说 OpenAI 协议，anthropic 适配器只做「出站」转换。
var openaiPA = &openai.Adaptor{}

// Adaptor 是 Anthropic 协议适配器。
type Adaptor struct{}

func (*Adaptor) SupportsOperation(operation string, stream, tools bool) bool {
	return operation == "chat"
}

// 编译期断言：同时满足两个接口。
var (
	_ adaptor.Adaptor         = (*Adaptor)(nil)
	_ adaptor.ProtocolAdaptor = (*Adaptor)(nil)
)

// GetChannelName 返回渠道展示名。
func (a *Adaptor) GetChannelName() string { return "anthropic" }

// Name 返回协议名。
func (a *Adaptor) Name() string { return "anthropic" }

// DefaultBaseURL 是 Anthropic 官方地址。
func (a *Adaptor) DefaultBaseURL() string { return "https://api.anthropic.com" }

// AuthHeader 返回 x-api-key 鉴权头。
// 注意：Anthropic 还需要 anthropic-version 头，由 SetupRequestHeader 补上
// （AuthHeader 只负责 key 部分，版本头不是「鉴权」语义）。
func (a *Adaptor) AuthHeader(apiKey string) (string, string) {
	return "x-api-key", apiKey
}

// GetRequestURL 拼出上游地址：{base}/v1/messages
func (a *Adaptor) GetRequestURL(m *meta.Meta) (string, error) {
	return strings.TrimSuffix(m.BaseURL, "/") + "/v1/messages", nil
}

// SetupRequestHeader 设置鉴权头和 API 版本头。
//
// anthropic-version 是必填头：不带上游直接 400。
// 版本值写死而不是取配置：让行为可预期，配置错了反而难排查。
func (a *Adaptor) SetupRequestHeader(req *http.Request, m *meta.Meta) error {
	req.Header.Set("x-api-key", m.APIKey)
	req.Header.Set("anthropic-version", anthropicVersion)
	return nil
}

// ConvertRequest 是 adaptor.Adaptor 接口方法。
//
// 直接转发路径（非枢纽模式）下：客户端协议视为 OpenAI，
// 做「OpenAI → 规范 → Claude」两段转换。
// 枢纽模式下（relay.convertRequestBody）不会走到这里，
// 但保留实现以防其他调用方直接用 Adaptor 接口。
func (a *Adaptor) ConvertRequest(body []byte, m *meta.Meta) ([]byte, error) {
	req, err := openaiPA.ParseRequest(body)
	if err != nil {
		return nil, err
	}
	req.Model = m.ActualModel
	return a.BuildRequest(req)
}

// ---- ProtocolAdaptor 实现（委托给 convert.go / stream.go 的包级函数）----

func (a *Adaptor) ParseRequest(body []byte) (*canonical.ChatRequest, error) {
	return ParseRequest(body)
}

func (a *Adaptor) BuildRequest(req *canonical.ChatRequest) ([]byte, error) {
	return BuildRequest(req)
}

func (a *Adaptor) ParseResponse(body []byte) (*canonical.ChatResponse, error) {
	return ParseResponse(body)
}

func (a *Adaptor) BuildResponse(resp *canonical.ChatResponse) ([]byte, error) {
	return BuildResponse(resp)
}

// ParseStreamLine 解析一行 Claude SSE。
func (a *Adaptor) ParseStreamLine(line []byte) ([]*canonical.StreamChunk, error) {
	return newTranslator().ParseStreamLine(line)
}

func (a *Adaptor) NewStreamSession() adaptor.StreamSession { return newTranslator() }

// BuildStreamLine 是 ProtocolAdaptor 方法。
// Claude 作为出站协议时由它把规范 chunk 写成 Claude SSE——
// 本项目的客户端固定是 OpenAI，所以这个方法实际只在
// 「Claude 客户端 → 其他上游」的未来场景用到，保留实现。
func (a *Adaptor) BuildStreamLine(chunk *canonical.StreamChunk) ([]byte, error) {
	return buildClaudeStreamLine(chunk)
}
