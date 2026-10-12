// Package gemini 的 HTTP 层外壳。
//
// 包结构：
//   - convert.go  请求/响应转换 + URL 构造 + 协议结构体
//   - stream.go   流式快照翻译（Gemini SSE → OpenAI chunk）
//   - adaptor.go  HTTP 层外壳（实现 adaptor.Adaptor）+ 接口绑定
//   - json.go     JSON 辅助函数
//
// 与 anthropic 的一个重要区别：Gemini 的模型名不在请求体里，
// 在 URL path 上。所以 BuildRequest 需要 modelName 参数——
// 这个参数由 adaptor 层从 meta.ActualModel 注入。
package gemini

import (
	"io"
	"net/http"
	"sync"

	"github.com/gin-gonic/gin"

	"minigateway/internal/relay/adaptor"
	"minigateway/internal/relay/adaptor/openai"
	"minigateway/internal/relay/canonical"
	"minigateway/internal/relay/meta"
	"minigateway/internal/relay/model"
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

// DoResponse 是 adaptor.Adaptor 接口方法（直接转发路径用）。
// 枢纽模式下 relay 自己处理，保留实现保持接口完整。
func (a *Adaptor) DoResponse(c *gin.Context, resp *http.Response, m *meta.Meta) (*model.Usage, *model.ErrorWithStatusCode) {
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, upstreamError("读取上游响应失败: " + err.Error())
	}
	cResp, err := a.ParseResponse(body)
	if err != nil {
		return nil, upstreamError("解析上游响应失败: " + err.Error())
	}
	// Gemini 响应不带模型名，回写时用请求的模型名
	cResp.Model = m.ActualModel
	out, err := openaiPA.BuildResponse(cResp)
	if err != nil {
		return nil, upstreamError("构造客户端响应失败: " + err.Error())
	}
	c.Data(http.StatusOK, "application/json", out)
	return cResp.Usage, nil
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

// ParseStreamLine 无状态调用（单测用）。正式路径走 ParseStreamLineSession。
func (a *Adaptor) ParseStreamLine(line []byte) ([]*canonical.StreamChunk, error) {
	return getTranslator("__shared__").ParseStreamLine(line)
}

// ParseStreamLineSession 在会话上下文里解析一行 Gemini SSE。
// 每个会话有独立 translator：lastTextLen 不能跨请求共享
// （否则两个并发流会互相把对方的文本当成重复）。
func (a *Adaptor) ParseStreamLineSession(line []byte, sessionID string) ([]*canonical.StreamChunk, error) {
	return getTranslator(sessionID).ParseStreamLine(line)
}

// ReleaseStreamSession 流结束后清理会话状态。
func (a *Adaptor) ReleaseStreamSession(sessionID string) {
	translatorsMu.Lock()
	defer translatorsMu.Unlock()
	delete(translators, sessionID)
}

// getTranslator 取（或创建）会话的流翻译器。
func getTranslator(sessionID string) *translator {
	translatorsMu.Lock()
	defer translatorsMu.Unlock()
	t, ok := translators[sessionID]
	if !ok {
		t = newTranslator()
		translators[sessionID] = t
	}
	return t
}

// translators 是会话 translator 缓存（mutex 保护 map 并发读写）。
var (
	translators   = map[string]*translator{}
	translatorsMu sync.Mutex
)

// BuildStreamLine 是 ProtocolAdaptor 方法（Gemini 客户端出站用，当前项目未启用）。
func (a *Adaptor) BuildStreamLine(chunk *canonical.StreamChunk) ([]byte, error) {
	return buildGeminiStreamLine(chunk)
}

// upstreamError 构造上游错误。
func upstreamError(msg string) *model.ErrorWithStatusCode {
	return &model.ErrorWithStatusCode{
		StatusCode: http.StatusBadGateway,
		Error:      model.ErrorDetail{Message: msg, Type: "upstream_error"},
	}
}
