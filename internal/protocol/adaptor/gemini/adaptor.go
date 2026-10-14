// Package gemini 的 HTTP 层外壳。
//
// 包结构：
//   - convert.go  请求/响应转换 + URL 构造 + 协议结构体
//   - stream.go   流式增量翻译（Gemini SSE → OpenAI chunk）
//   - adaptor.go  HTTP 层外壳（实现 adaptor.Adaptor）+ 接口绑定
//   - json.go     JSON 辅助函数
//
// 与 anthropic 的一个重要区别：Gemini 的模型名不在请求体里，
// 在 URL path 上。所以 BuildRequest 需要 modelName 参数——
// 这个参数由 adaptor 层从 meta.ActualModel 注入。
package gemini

import (
	"net/http"

	"minigateway/internal/protocol/adaptor"
	"minigateway/internal/protocol/adaptor/openai"
	"minigateway/internal/protocol/canonical"
	"minigateway/internal/protocol/meta"
)

// Adaptor 是 Gemini 协议适配器。
type Adaptor struct{}

// 编译期断言：同时满足两个接口。
var (
	_ adaptor.Adaptor         = (*Adaptor)(nil)
	_ adaptor.ProtocolAdaptor = (*Adaptor)(nil)
)

// openaiPA 是入站（客户端侧）协议适配器。
var openaiPA = &openai.Adaptor{}

// GetChannelName 返回渠道展示名。
func (a *Adaptor) GetChannelName() string { return "gemini" }

// Name 返回协议名。
func (a *Adaptor) Name() string { return "gemini" }

// DefaultBaseURL 是 Gemini 官方地址。
func (a *Adaptor) DefaultBaseURL() string { return "https://generativelanguage.googleapis.com" }

// AuthHeader 返回空——Gemini 不用鉴权头，key 在 URL query 上。
// 返回空字符串是「协议约定」：调用方看到空 name 就知道走 URL 鉴权。
func (a *Adaptor) AuthHeader(apiKey string) (string, string) {
	return "", ""
}

// GetRequestURL 拼出上游地址。
// 模型名从 meta.ActualModel 取（已做映射），key 拼在 query 上。
func (a *Adaptor) GetRequestURL(m *meta.Meta) (string, error) {
	return BuildURL(m.BaseURL, m.ActualModel, m.APIKey, m.IsStream)
}

// SetupRequestHeader 不设置鉴权头（key 已在 URL 里）。
// 只设 Content-Type（由公共发请求逻辑统一设置，这里显式声明意图）。
func (a *Adaptor) SetupRequestHeader(req *http.Request, m *meta.Meta) error {
	// Gemini 不需要鉴权头，什么都不加。
	// 保留方法是为了接口完整性（其他协议需要加头）。
	return nil
}

// ConvertRequest 是 adaptor.Adaptor 接口方法（直接转发路径用）。
func (a *Adaptor) ConvertRequest(body []byte, m *meta.Meta) ([]byte, error) {
	req, err := openaiPA.ParseRequest(body)
	if err != nil {
		return nil, err
	}
	req.Model = m.ActualModel
	return a.BuildRequest(req)
}

// ---- ProtocolAdaptor 实现 ----

// ParseRequest 解析 Gemini 请求体。
// Gemini 请求体里没有模型名（在 URL），所以返回的规范请求 Model 为空，
// 由调用方（relay）从 meta.ActualModel 填充。
func (a *Adaptor) ParseRequest(body []byte) (*canonical.ChatRequest, error) {
	return ParseRequest(body)
}

// BuildRequest 构造 Gemini 请求体。
// modelName 由调用方从 meta.ActualModel 提供（规范请求的 Model 字段
// 在 OpenAI 入站时已被 relay 设为 ActualModel，这里直接用）。
func (a *Adaptor) BuildRequest(req *canonical.ChatRequest) ([]byte, error) {
	return BuildRequest(req, req.Model)
}

func (a *Adaptor) ParseResponse(body []byte) (*canonical.ChatResponse, error) {
	return ParseResponse(body)
}

func (a *Adaptor) BuildResponse(resp *canonical.ChatResponse) ([]byte, error) {
	return BuildResponse(resp)
}

// ParseStreamLine 是单行解析入口；完整流应使用 NewStreamSession 保留逐流状态。
func (a *Adaptor) ParseStreamLine(line []byte) ([]*canonical.StreamChunk, error) {
	return newTranslator().ParseStreamLine(line)
}

func (a *Adaptor) NewStreamSession() adaptor.StreamSession { return newTranslator() }

// BuildStreamLine 是 ProtocolAdaptor 方法（Gemini 客户端出站用，当前项目未启用）。
func (a *Adaptor) BuildStreamLine(chunk *canonical.StreamChunk) ([]byte, error) {
	return buildGeminiStreamLine(chunk)
}
