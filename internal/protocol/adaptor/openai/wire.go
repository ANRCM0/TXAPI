package openai

import (
	"encoding/json"
	"errors"
	"strings"

	"minigateway/internal/protocol/canonical"
	"minigateway/internal/protocol/model"
)

// 这个文件放 OpenAI 协议的结构体和 JSON 辅助函数。
// anthropic/gemini 适配器也各有同名文件（结构体形状不同，不共享）。

// ErrNoChoices 上游响应里没有 choices（比如被安全策略拦截时的空响应）。
// 单独成错误变量，便于调用方判断要不要重试。
var ErrNoChoices = errors.New("上游响应缺少 choices")

// ---- 请求方向的结构体 ----

// rawMessage 是 OpenAI 协议里的入站消息。
// content 用 rawContent 兼容「字符串 / 分片数组」两种写法。
type rawMessage struct {
	Role       string        `json:"role"`
	Content    rawContent    `json:"content"`
	ToolCallID string        `json:"tool_call_id"`
	Name       string        `json:"name"`
	ToolCalls  []rawToolCall `json:"tool_calls"`
}

// rawToolCall 是 assistant 消息里的工具调用。
type rawToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

// rawContent 是「字符串或分片数组」的兼容类型。
//
// 为什么要自定义 UnmarshalJSON：OpenAI 的 content 在纯文本时是字符串，
// 多模态时是数组。用 json.RawMessage 虽然也能活，但每个调用点都要特判，
// 散落到各处容易漏。统一在这里折叠成一种表示。
type rawContent string

// UnmarshalJSON 把字符串 / 数组 / null 三种形态统一成纯文本。
// 注意：数组形态下只保留文本分片，图片分片在 toCanonical 里按原始 JSON 处理
// （那边能看到完整的 type 字段，不会丢图片）。
func (c *rawContent) UnmarshalJSON(data []byte) error {
	if len(data) == 0 || string(data) == "null" {
		*c = ""
		return nil
	}
	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		*c = rawContent(s)
		return nil
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(data, &parts); err != nil {
		return err
	}
	// Preserve structured content until every part can be inspected.
	*c = rawContent(string(data))
	return nil
}

// toCanonical 把 OpenAI 消息转成规范消息。
//
// content 的数组形态要能拿到图片分片，所以直接对原始 JSON 再解一次
// （rawContent 已经折叠过，只剩文本）。
func (m rawMessage) toCanonical() canonical.Message {
	msg := canonical.Message{Role: m.Role, ToolUseID: m.ToolCallID, Name: m.Name}

	// 先按数组试：能解开说明是多模态形态，逐块映射（图片不丢）
	var parts []struct {
		Type     string `json:"type"`
		Text     string `json:"text"`
		ImageURL struct {
			URL string `json:"url"`
		} `json:"image_url"`
	}
	if json.Unmarshal([]byte(m.Content), &parts) == nil {
		for _, p := range parts {
			switch p.Type {
			case "text":
				msg.Content = append(msg.Content, canonical.ContentPart{Type: canonical.PartText, Text: p.Text})
			case "image_url":
				msg.Content = append(msg.Content, canonical.ContentPart{Type: canonical.PartImage, ImageURL: p.ImageURL.URL})
			}
		}
	} else if m.Content != "" {
		msg.Content = append(msg.Content, canonical.ContentPart{Type: canonical.PartText, Text: string(m.Content)})
	}
	if m.Role == canonical.RoleTool {
		for i := range msg.Content {
			msg.Content[i].Type = canonical.PartToolResult
		}
	}

	// tool_calls 拆成独立的 tool_use 内容块，跟 Claude 的 content 块数组对齐
	for _, tc := range m.ToolCalls {
		msg.Content = append(msg.Content, canonical.ContentPart{
			Type:      canonical.PartToolUse,
			ToolUseID: tc.ID,
			Name:      tc.Function.Name,
			Input:     json.RawMessage(tc.Function.Arguments),
		})
	}
	return msg
}

// toParts 把已解析的内容转成规范内容块。
// 纯文本形态直接包一个 PartText；多模态数组形态按 type 映射。
// （解析入口是 toCanonical，那里处理了完整的图片分片）
func (c rawContent) toParts() []canonical.ContentPart {
	if c == "" {
		return nil
	}
	var parts []struct {
		Type     string `json:"type"`
		Text     string `json:"text"`
		ImageURL struct {
			URL string `json:"url"`
		} `json:"image_url"`
	}
	if json.Unmarshal([]byte(c), &parts) == nil {
		var out []canonical.ContentPart
		for _, p := range parts {
			switch p.Type {
			case "text":
				out = append(out, canonical.ContentPart{Type: canonical.PartText, Text: p.Text})
			case "image_url":
				out = append(out, canonical.ContentPart{Type: canonical.PartImage, ImageURL: p.ImageURL.URL})
			}
		}
		return out
	}
	return []canonical.ContentPart{{Type: canonical.PartText, Text: string(c)}}
}

// rawTool 是 OpenAI 的 tools 数组元素。
type rawTool struct {
	Type     string      `json:"type"`
	Function rawFunction `json:"function"`
}

// rawFunction 是 OpenAI 的函数声明。
type rawFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

// ---- 出站结构体（规范 → OpenAI）----

// openAIRequest 是 OpenAI 请求体。
// messages 用 json.RawMessage 而不是强类型：出站消息形状由
// canonical.ToOpenAIMessage 统一生成，这里只做信封。
type openAIRequest struct {
	Model       string            `json:"model"`
	Messages    []json.RawMessage `json:"messages"`
	MaxTokens   int               `json:"max_tokens,omitempty"`
	Temperature *float64          `json:"temperature,omitempty"`
	TopP        *float64          `json:"top_p,omitempty"`
	Stop        []string          `json:"stop,omitempty"`
	Stream      bool              `json:"stream,omitempty"`
	Tools       []rawTool         `json:"tools,omitempty"`
}

// openAIResponse 是 OpenAI 非流式响应体。
type openAIResponse struct {
	ID      string         `json:"id"`
	Object  string         `json:"object"`
	Model   string         `json:"model"`
	Choices []openAIChoice `json:"choices"`
	Usage   *usageJSON     `json:"usage,omitempty"`
}

// openAIChoice 是 OpenAI 响应选项。
type openAIChoice struct {
	Index        int             `json:"index"`
	Message      json.RawMessage `json:"message"`
	FinishReason string          `json:"finish_reason"`
}

// openAIChunk 是 OpenAI 流式 chunk。
type openAIChunk struct {
	ID      string              `json:"id"`
	Object  string              `json:"object"`
	Model   string              `json:"model"`
	Choices []openAIChunkChoice `json:"choices"`
	Usage   *usageJSON          `json:"usage,omitempty"`
}

// openAIChunkChoice 是流式 chunk 的选项。
type openAIChunkChoice struct {
	Index        int         `json:"index"`
	Delta        openAIDelta `json:"delta"`
	FinishReason string      `json:"finish_reason,omitempty"`
}

// openAIDelta 是流式增量。
type openAIDelta struct {
	Role      string                `json:"role,omitempty"`
	Content   rawContent            `json:"content,omitempty"`
	ToolCalls []openAIToolCallDelta `json:"tool_calls,omitempty"`
}

// openAIToolCallDelta 是流式工具调用增量。
type openAIToolCallDelta struct {
	Index    int                 `json:"index"`
	ID       string              `json:"id,omitempty"`
	Type     string              `json:"type,omitempty"`
	Function openAIFunctionDelta `json:"function"`
}

// openAIFunctionDelta 是流式函数参数增量。
type openAIFunctionDelta struct {
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
}

// usageJSON 是 OpenAI 的 usage 形状。
// Anthropic/Gemini 的 usage 字段名不同，由各自适配器映射进来。
type usageJSON struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// toUsage 转成网关内部 Usage。
func (u *usageJSON) toUsage() *model.Usage {
	if u == nil {
		return nil
	}
	return &model.Usage{
		PromptTokens:     u.PromptTokens,
		CompletionTokens: u.CompletionTokens,
		TotalTokens:      u.TotalTokens,
	}
}

// ---- JSON 辅助函数（anthropic/gemini 通过各自副本使用，形状不同不共享）----

// sseData 取出一行 SSE 的 data 载荷。
// 第二返回值 false 表示这行不是 data 行（空行、event: 行、注释行）。
func sseData(line []byte) (string, bool) {
	s := strings.TrimSpace(string(line))
	if s == "" || strings.HasPrefix(s, ":") {
		return "", false
	}
	if !strings.HasPrefix(s, "data:") {
		return "", false
	}
	return strings.TrimSpace(strings.TrimPrefix(s, "data:")), true
}

// parseStop 兼容 stop 的两种写法：字符串或字符串数组。
func parseStop(raw json.RawMessage) []string {
	if len(raw) == 0 {
		return nil
	}
	var arr []string
	if json.Unmarshal(raw, &arr) == nil {
		return arr
	}
	var s string
	if json.Unmarshal(raw, &s) == nil && s != "" {
		return []string{s}
	}
	return nil
}

// extractExtra 从请求体里摘出「未建模字段」。
//
// 做法：整体反序列化成 map，删掉已建模的键，剩下的原样保留。
// 用 map 而不是结构体，就是为了不丢字段——这是网关透明代理的基本要求。
func extractExtra(body []byte, modeled ...string) map[string]json.RawMessage {
	var all map[string]json.RawMessage
	if json.Unmarshal(body, &all) != nil {
		return nil
	}
	for _, k := range modeled {
		delete(all, k)
	}
	if len(all) == 0 {
		return nil
	}
	return all
}

// mergeExtra 把未建模字段合并回出站请求体。
// 目标协议的同名键由出站结构体先生成，Extra 不覆盖它们（结构体是权威）。
func mergeExtra(body []byte, extra map[string]json.RawMessage) ([]byte, error) {
	if len(extra) == 0 {
		return body, nil
	}
	var all map[string]json.RawMessage
	if json.Unmarshal(body, &all) != nil {
		return body, nil
	}
	for k, v := range extra {
		if _, exists := all[k]; exists {
			continue
		}
		all[k] = v
	}
	return json.Marshal(all)
}
