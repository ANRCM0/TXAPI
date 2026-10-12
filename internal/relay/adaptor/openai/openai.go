// Package openai 实现 OpenAI 兼容协议的适配器。
//
// 重要：这个适配器不只服务 OpenAI 本身。所有「OpenAI 兼容」的厂商
// （DeepSeek、Moonshot、SiliconFlow、Ollama、vLLM…）都复用它，
// 只需要在配置里改 base_url 和模型名。
// 这是比 one-api 更省事的地方：它有 40 个适配器目录，其中一半
// 只是 base_url 不同，没必要各写一份。
package openai

import (
	"bufio"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"minigateway/internal/relay/adaptor"
	"minigateway/internal/relay/meta"
	"minigateway/internal/relay/model"
)

// Adaptor 是 OpenAI 兼容协议的适配器实现。
type Adaptor struct{}

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

// DoResponse 把上游响应回给客户端，并抽取用量。
//
// 这里分两条路：流式（SSE）和普通 JSON。
// 流式必须边收边发，否则打字机效果会消失。
func (a *Adaptor) DoResponse(c *gin.Context, resp *http.Response, m *meta.Meta) (*model.Usage, *model.ErrorWithStatusCode) {
	if resp.StatusCode != http.StatusOK {
		return handleErrorResponse(resp)
	}
	if m.IsStream {
		return handleStreamResponse(c, resp)
	}
	return handleNormalResponse(c, resp)
}

func (a *Adaptor) GetChannelName() string { return "openai" }

// handleNormalResponse 处理非流式响应。
func handleNormalResponse(c *gin.Context, resp *http.Response) (*model.Usage, *model.ErrorWithStatusCode) {
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, &model.ErrorWithStatusCode{
			StatusCode: http.StatusBadGateway,
			Error:      model.ErrorDetail{Message: "读取上游响应失败: " + err.Error(), Type: "upstream_error"},
		}
	}
	// 先解析 usage 再回写：解析用的是内存里的 body，不影响后续回写。
	var parsed struct {
		Usage *model.Usage `json:"usage"`
	}
	_ = json.Unmarshal(body, &parsed)

	c.Data(http.StatusOK, "application/json", body)
	return parsed.Usage, nil
}

// handleStreamResponse 处理 SSE 流式响应。
//
// 关键点：
//  1. 用 bufio.Scanner 按行读，逐行转发，不要缓冲整个响应。
//  2. 每次写完必须 Flush，否则数据卡在缓冲区里，客户端看不到打字机效果。
//  3. usage 通常在最后一个 chunk 里（需要 stream_options.include_usage），
//     所以边转发边找，不能等流结束才处理。
func handleStreamResponse(c *gin.Context, resp *http.Response) (*model.Usage, *model.ErrorWithStatusCode) {
	defer resp.Body.Close()

	c.Writer.Header().Set("Content-Type", "text/event-stream")
	c.Writer.Header().Set("Cache-Control", "no-cache")
	c.Writer.Header().Set("Connection", "keep-alive")
	// 关掉 Nginx 之类的缓冲，否则流式会被攒成一坨再发
	c.Writer.Header().Set("X-Accel-Buffering", "no")
	c.Writer.WriteHeader(http.StatusOK)

	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		return nil, &model.ErrorWithStatusCode{
			StatusCode: http.StatusInternalServerError,
			Error:      model.ErrorDetail{Message: "当前服务器不支持流式响应", Type: "server_error"},
		}
	}

	var usage *model.Usage
	scanner := bufio.NewScanner(resp.Body)
	// 单个 SSE 行可能很长（比如一次吐一大段文本），默认 64KB 不够用
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	for scanner.Scan() {
		line := scanner.Text()

		// 从数据行里找 usage。带 usage 的 chunk 一般是最后一条。
		if strings.HasPrefix(line, "data: ") {
			data := strings.TrimPrefix(line, "data: ")
			if data != "[DONE]" {
				var chunk struct {
					Usage *model.Usage `json:"usage"`
				}
				if json.Unmarshal([]byte(data), &chunk) == nil && chunk.Usage != nil {
					usage = chunk.Usage
				}
			}
		}

		// 原样转发这一行（含换行），保持 SSE 协议完整
		if _, err := c.Writer.WriteString(line + "\n"); err != nil {
			// 客户端断开连接是常见情况，不算上游错误
			return usage, nil
		}
		flusher.Flush()
	}

	if err := scanner.Err(); err != nil && err != io.EOF {
		// 已经往客户端写了数据，无法再改状态码，只能记录用量后返回
		return usage, nil
	}
	return usage, nil
}

// handleErrorResponse 解析上游错误，转成统一的错误结构。
//
// 网关不能把上游的原始错误直接吐给客户端：
// 一是格式各家不同，二是可能泄露上游渠道信息（比如 key 的提示）。
func handleErrorResponse(resp *http.Response) (*model.Usage, *model.ErrorWithStatusCode) {
	body, _ := io.ReadAll(resp.Body)
	defer resp.Body.Close()

	detail := model.ErrorDetail{
		Message: string(body),
		Type:    "upstream_error",
	}
	// 尽量从上游错误体里抽出可读的 message
	var parsed struct {
		Error struct {
			Message string `json:"message"`
			Type    string `json:"type"`
			Code    any    `json:"code"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &parsed) == nil && parsed.Error.Message != "" {
		detail.Message = parsed.Error.Message
		detail.Type = parsed.Error.Type
		detail.Code = parsed.Error.Code
	}
	return nil, &model.ErrorWithStatusCode{StatusCode: resp.StatusCode, Error: detail}
}

// 确保 Adaptor 实现了接口，编译期就能发现漏方法。
var _ adaptor.Adaptor = (*Adaptor)(nil)
