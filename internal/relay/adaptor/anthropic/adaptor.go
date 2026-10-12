// Package anthropic 实现 Anthropic Claude 协议适配器。
//
// 包结构：
//   - convert.go  请求/响应转换 + 协议结构体
//   - stream.go   流式事件流翻译（Claude SSE → OpenAI chunk）
//   - adaptor.go  HTTP 层外壳（实现 adaptor.Adaptor）+ 接口绑定
//
// 每个 Anthropic 请求会话需要一个独立的流翻译器（状态跨行保持），
// 所以 adaptor.go 里用 map[会话ID]*translator 缓存——不能把 translator
// 挂在适配器实例上：适配器是全局共享的，多请求并发会串状态。
package anthropic

import (
	"io"
	"net/http"
	"strings"
	"sync"

	"github.com/gin-gonic/gin"

	"minigateway/internal/relay/adaptor"
	"minigateway/internal/relay/adaptor/openai"
	"minigateway/internal/relay/canonical"
	"minigateway/internal/relay/meta"
	"minigateway/internal/relay/model"
)

// openaiPA 是入站（客户端侧）协议适配器。
// 客户端固定说 OpenAI 协议，anthropic 适配器只做「出站」转换。
var openaiPA = &openai.Adaptor{}

// Adaptor 是 Anthropic 协议适配器。
type Adaptor struct{}

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

// DoResponse 是 adaptor.Adaptor 接口方法。
// 枢纽模式下 relay 自己处理响应（不走这里），保留实现保持接口完整。
func (a *Adaptor) DoResponse(c *gin.Context, resp *http.Response, m *meta.Meta) (*model.Usage, *model.ErrorWithStatusCode) {
	// 直接转发路径：读整体 → 转成 OpenAI 响应 → 回写
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, upstreamError("读取上游响应失败: " + err.Error())
	}
	cResp, err := a.ParseResponse(body)
	if err != nil {
		return nil, upstreamError("解析上游响应失败: " + err.Error())
	}
	out, err := openaiPA.BuildResponse(cResp)
	if err != nil {
		return nil, upstreamError("构造客户端响应失败: " + err.Error())
	}
	c.Data(http.StatusOK, "application/json", out)
	return cResp.Usage, nil
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
//
// 每个请求会话有独立 translator：用请求 ID 做 key 缓存。
// 并发安全：translator 只在单条流的读取循环里被顺序访问，
// mutex 只保护 map 本身的并发读写（不同会话可能同时创建）。
var (
	translators   = map[string]*translator{}
	translatorsMu sync.Mutex
)

func (a *Adaptor) ParseStreamLine(line []byte) ([]*canonical.StreamChunk, error) {
	// 无会话上下文时用共享 translator：只支持单流场景（单测/直接调用）。
	// 正式转发路径走 relay.convertStreamLine，那里会传入会话 ID。
	t := getTranslator("__shared__")
	return t.ParseStreamLine(line)
}

// ParseStreamLineSession 在指定会话上下文里解析一行 Claude SSE。
// 每个 sessionID 有独立 translator，互不串状态。
func (a *Adaptor) ParseStreamLineSession(line []byte, sessionID string) ([]*canonical.StreamChunk, error) {
	return getTranslator(sessionID).ParseStreamLine(line)
}

// getTranslator 取（或创建）某个会话的流翻译器。
//
// 缓存在流结束后由 ReleaseStreamSession 清理，不会无限增长。
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

// ReleaseStreamSession 流结束后删除会话的 translator，释放内存。
func (a *Adaptor) ReleaseStreamSession(sessionID string) {
	translatorsMu.Lock()
	defer translatorsMu.Unlock()
	delete(translators, sessionID)
}

// BuildStreamLine 是 ProtocolAdaptor 方法。
// Claude 作为出站协议时由它把规范 chunk 写成 Claude SSE——
// 本项目的客户端固定是 OpenAI，所以这个方法实际只在
// 「Claude 客户端 → 其他上游」的未来场景用到，保留实现。
func (a *Adaptor) BuildStreamLine(chunk *canonical.StreamChunk) ([]byte, error) {
	return buildClaudeStreamLine(chunk)
}

// upstreamError 构造上游错误。
func upstreamError(msg string) *model.ErrorWithStatusCode {
	return &model.ErrorWithStatusCode{
		StatusCode: http.StatusBadGateway,
		Error:      model.ErrorDetail{Message: msg, Type: "upstream_error"},
	}
}
