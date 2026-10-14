// Package anthropic 实现 Anthropic Claude 协议适配器。
//
// 协议要点（与 OpenAI 的差异，全部是「有损转换」的高发区）：
//   - 鉴权：x-api-key 头 + anthropic-version 头（不是 Bearer）
//   - 端点：POST /v1/messages
//   - system：顶层独立字段，不在 messages 里（OpenAI 是 role=system 消息）
//   - role 只有 user/assistant，没有 system/tool；
//     工具结果放在 user 消息的 tool_result 内容块里
//   - content 永远是块数组，没有「纯字符串」写法
//   - 响应：content 块数组（text / tool_use），usage 只有
//     input_tokens/output_tokens，没有 total_tokens（要自己加）
//   - 流式：SSE 事件流（message_start / content_block_delta /
//     message_delta / message_stop），事件类型在 event: 行里，
//     data 里还有 type 字段，两者必须对上
//
// 转换策略：能转则转，不能转则明确报错，绝不静默丢字段。
package anthropic

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"minigateway/internal/protocol/canonical"
	"minigateway/internal/protocol/model"
)

// anthropicVersion 是 Anthropic API 的版本头。
// 写死一个已知可用值：这个头会随 API 演进，写 "latest" 之类的
// 在未来可能静默改变行为，写死版本至少行为可预期。
const anthropicVersion = "2023-06-01"

// ErrUnsupported 标记「这个请求里有 Claude 表达不了的参数」。
// 由上层转成 400 返回给客户端，绝不在网关内部悄悄丢掉。
var ErrUnsupported = errors.New("anthropic: 不支持的参数")

// ---- 请求方向结构体 ----

// claudeRequest 是 Anthropic 的请求体。
type claudeRequest struct {
	Model       string          `json:"model"`
	Messages    []claudeMessage `json:"messages"`
	System      string          `json:"system,omitempty"`
	MaxTokens   int             `json:"max_tokens"`
	Temperature *float64        `json:"temperature,omitempty"`
	TopP        *float64        `json:"top_p,omitempty"`
	Stop        []string        `json:"stop_sequences,omitempty"`
	Stream      bool            `json:"stream,omitempty"`
	Tools       []claudeTool    `json:"tools,omitempty"`
}

// claudeMessage 是 Anthropic 的消息。content 恒为块数组。
type claudeMessage struct {
	Role    string          `json:"role"`
	Content []claudeContent `json:"content"`
}

// claudeContent 是内容块。用 omitempty 让不同块类型只出现自己的字段。
type claudeContent struct {
	Type string `json:"type"`

	// text 块
	Text string `json:"text,omitempty"`

	// tool_use 块
	ID    string          `json:"id,omitempty"`
	Name  string          `json:"name,omitempty"`
	Input json.RawMessage `json:"input,omitempty"`

	// tool_result 块
	ToolUseID string `json:"tool_use_id,omitempty"`
	Content   string `json:"content,omitempty"`
	IsError   bool   `json:"is_error,omitempty"`

	// image 块
	Source *claudeImageSource `json:"source,omitempty"`
}

// claudeImageSource 是图片块的数据源。
// Claude 只接受 base64 内联数据，不接受 URL——这是和 OpenAI 的重要差异，
// 遇到 http(s) 图片 URL 必须明确报错（见 BuildRequest）。
type claudeImageSource struct {
	Type      string `json:"type"` // "base64"
	MediaType string `json:"media_type"`
	Data      string `json:"data"`
}

// claudeTool 是 Claude 的工具声明。
type claudeTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"input_schema"`
}

// ---- 响应方向结构体 ----

// claudeResponse 是 Anthropic 的非流式响应。
type claudeResponse struct {
	ID           string          `json:"id"`
	Type         string          `json:"type"`
	Role         string          `json:"role"`
	Model        string          `json:"model"`
	Content      []claudeContent `json:"content"`
	StopReason   string          `json:"stop_reason"`
	StopSequence string          `json:"stop_sequence,omitempty"`
	Usage        claudeUsage     `json:"usage"`
}

// claudeUsage 是 Anthropic 的用量。
// 注意没有 total_tokens，计费侧要自己相加。
type claudeUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// toUsage 转成网关内部 Usage，total 自己算。
func (u claudeUsage) toUsage() *model.Usage {
	return &model.Usage{
		PromptTokens:     u.InputTokens,
		CompletionTokens: u.OutputTokens,
		TotalTokens:      u.InputTokens + u.OutputTokens,
	}
}

// ---- 流式方向结构体 ----

// claudeStreamEvent 是 Anthropic 的流式事件（一行 SSE 的 data 部分）。
// 所有事件共用一个结构体，靠 Type 区分；用 RawMessage 接可选字段。
type claudeStreamEvent struct {
	Type string `json:"type"`

	// message_start: message 字段带完整 message（含 usage.input_tokens）
	Message *claudeResponse `json:"message,omitempty"`

	// content_block_start / content_block_delta: index + delta
	Index int             `json:"index,omitempty"`
	Delta json.RawMessage `json:"delta,omitempty"`

	// message_delta: delta.stop_reason + usage.output_tokens
	Usage *claudeUsage `json:"usage,omitempty"`

	// error 事件
	Error *struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

// claudeContentDelta 是 content_block_delta 的 delta 载荷。
// text_delta 和 input_json_delta 共用，靠 Type 区分。
type claudeContentDelta struct {
	Type        string `json:"type"`
	Text        string `json:"text"`
	PartialJSON string `json:"partial_json"`
}

// ---- 请求转换 ----

// ParseRequest 把 Claude 请求体解析成规范格式。
//
// 主要工作：
//  1. system 顶层字段 → 规范里的 role=system 消息（放最前面）
//  2. content 块数组 → 规范 ContentPart（tool_use / tool_result 直接映射）
//  3. tool_result 块在 Claude 里属于 user 消息，规范里拆成独立 role=tool 消息
func ParseRequest(body []byte) (*canonical.ChatRequest, error) {
	var raw claudeRequest
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, err
	}

	req := &canonical.ChatRequest{
		Model:       raw.Model,
		MaxTokens:   raw.MaxTokens,
		Temperature: raw.Temperature,
		TopP:        raw.TopP,
		Stop:        raw.Stop,
		Stream:      raw.Stream,
	}
	// system 是顶层字段：转成规范的首条 system 消息。
	// 空字符串不生成消息（Claude 允许 system 为空，但规范里空消息没意义）。
	if raw.System != "" {
		req.Messages = append(req.Messages, canonical.Message{
			Role:    canonical.RoleSystem,
			Content: []canonical.ContentPart{{Type: canonical.PartText, Text: raw.System}},
		})
	}

	for _, m := range raw.Messages {
		// tool_result 块在 Claude 里挂在 user 消息下。
		// 规范格式把它拆成独立的 role=tool 消息（和 OpenAI 对齐），
		// 这样出站到 OpenAI 客户端时不用再拼一次。
		var userParts []canonical.ContentPart
		for _, blk := range m.Content {
			switch blk.Type {
			case "text":
				userParts = append(userParts, canonical.ContentPart{Type: canonical.PartText, Text: blk.Text})
			case "image":
				// base64 图转 data URL：规范格式用 data URL 统一表达图片
				if blk.Source != nil && blk.Source.Type == "base64" {
					userParts = append(userParts, canonical.ContentPart{
						Type:     canonical.PartImage,
						ImageURL: fmt.Sprintf("data:%s;base64,%s", blk.Source.MediaType, blk.Source.Data),
					})
				}
			case "tool_use":
				userParts = append(userParts, canonical.ContentPart{
					Type:      canonical.PartToolUse,
					ToolUseID: blk.ID,
					Name:      blk.Name,
					Input:     blk.Input,
				})
			case "tool_result":
				// 先 flush 已积累的 user 内容块，再单独发 tool 消息
				if len(userParts) > 0 {
					req.Messages = append(req.Messages, canonical.Message{Role: m.Role, Content: userParts})
					userParts = nil
				}
				req.Messages = append(req.Messages, canonical.Message{
					Role:      canonical.RoleTool,
					ToolUseID: blk.ToolUseID,
					Content:   []canonical.ContentPart{{Type: canonical.PartToolResult, Text: blk.Content, IsError: blk.IsError}},
				})
			case "thinking":
				// thinking 块（扩展思考）：规范格式有 PartThinking，
				// 但入站请求里出现 thinking 块意味着客户端在传回思考历史——
				// 这在多轮对话里是合法场景，保留为 PartThinking。
				userParts = append(userParts, canonical.ContentPart{Type: canonical.PartThinking, Text: blk.Text})
			}
		}
		if len(userParts) > 0 {
			req.Messages = append(req.Messages, canonical.Message{Role: m.Role, Content: userParts})
		}
	}

	for _, t := range raw.Tools {
		req.Tools = append(req.Tools, canonical.Tool{
			Name:        t.Name,
			Description: t.Description,
			Parameters:  t.InputSchema,
		})
	}
	req.Extra = claudeExtraFields(body)
	return req, nil
}

// BuildRequest 把规范请求构造成 Claude 请求体。
func BuildRequest(req *canonical.ChatRequest) ([]byte, error) {
	// max_tokens 在 Claude 协议里是必填且无默认值。
	// 客户端（OpenAI 协议）不传时网关必须兜底：不填上游直接 400。
	// 兜底值取一个保守值，并在注释里说明——不能默默传 0。
	maxTokens := req.MaxTokens
	if maxTokens <= 0 {
		maxTokens = 4096
	}

	out := claudeRequest{
		Model:       req.Model,
		MaxTokens:   maxTokens,
		Temperature: req.Temperature,
		TopP:        req.TopP,
		Stop:        req.Stop,
		Stream:      req.Stream,
	}

	// system 提取 + 角色映射
	for _, m := range req.Messages {
		switch m.Role {
		case canonical.RoleSystem:
			// Claude 的 system 是顶层字段，多条 system 消息用换行拼接
			text := canonical.TextOf(m)
			if text != "" {
				if out.System != "" {
					out.System += "\n\n" + text
				} else {
					out.System = text
				}
			}
			continue
		case canonical.RoleTool:
			// 工具结果在 Claude 里是 user 消息的 tool_result 块。
			// 连续多条 tool 消息要合并进同一个 user 消息，否则 Claude 会
			// 报「tool_result 没有紧跟对应的 tool_use」。
			appendToolResult(&out, m)
			continue
		case canonical.RoleUser, canonical.RoleAssistant:
			// 正常角色直接映射
		default:
			return nil, fmt.Errorf("%w: Claude 不支持角色 %q", ErrUnsupported, m.Role)
		}

		blocks, err := contentBlocks(m)
		if err != nil {
			return nil, err
		}
		if len(blocks) == 0 {
			// 空 content 的消息发给 Claude 会被拒（content 不能为空数组）。
			// 兜底塞一个空格，保证消息结构合法——比丢消息好。
			blocks = []claudeContent{{Type: "text", Text: " "}}
		}
		out.Messages = append(out.Messages, claudeMessage{Role: m.Role, Content: blocks})
	}

	// 合并相邻的 tool_result user 消息：Claude 要求同一批 tool_use 的
	// 结果必须在紧随其后的同一条 user 消息里。OpenAI 客户端习惯把
	// 多个工具结果拆成多条 tool 消息，这里必须合并。
	out.Messages = mergeToolResultMessages(out.Messages)

	for _, t := range req.Tools {
		schema := t.Parameters
		if len(schema) == 0 {
			// Claude 要求 input_schema 必须是合法 JSON Schema，
			// 缺省时给一个空对象 schema 而不是 null
			schema = json.RawMessage(`{"type":"object","properties":{}}`)
		}
		out.Tools = append(out.Tools, claudeTool{
			Name:        t.Name,
			Description: t.Description,
			InputSchema: schema,
		})
	}

	body, err := json.Marshal(out)
	if err != nil {
		return nil, err
	}
	return mergeExtra(body, req.Extra, claudeSupportedExtra)
}

// contentBlocks 把规范消息的内容块转成 Claude 内容块。
func contentBlocks(m canonical.Message) ([]claudeContent, error) {
	var blocks []claudeContent
	for _, p := range m.Content {
		switch p.Type {
		case canonical.PartText:
			blocks = append(blocks, claudeContent{Type: "text", Text: p.Text})
		case canonical.PartThinking:
			// 思考内容：Claude 的 thinking 块需要 signature 字段才能回传，
			// 网关拿不到 signature，所以转成 text 块。
			// 这不是静默丢弃（内容还在），但思考块的结构信息丢了——
			// 在报告里如实说明这个降级。
			blocks = append(blocks, claudeContent{Type: "text", Text: p.Text})
		case canonical.PartImage:
			src, err := imageSource(p.ImageURL)
			if err != nil {
				return nil, err
			}
			blocks = append(blocks, claudeContent{Type: "image", Source: src})
		case canonical.PartToolUse:
			input := p.Input
			if len(input) == 0 {
				input = json.RawMessage(`{}`)
			}
			blocks = append(blocks, claudeContent{
				Type:  "tool_use",
				ID:    p.ToolUseID,
				Name:  p.Name,
				Input: input,
			})
		case canonical.PartToolResult:
			// tool_result 在 user 消息里才是合法位置；如果它出现在
			// assistant 消息里（规范格式允许），搬去 user 消息。
			// 这里保持原样，由 mergeToolResultMessages 统一处理。
			blocks = append(blocks, claudeContent{
				Type:      "tool_result",
				ToolUseID: p.ToolUseID,
				Content:   p.Text,
				IsError:   p.IsError,
			})
		}
	}
	return blocks, nil
}

// imageSource 把规范图片 URL 转成 Claude 的 base64 数据源。
//
// Claude 只接受 base64 内联图片，不接受 http(s) URL。
// 遇到 URL 明确报错——不能转成空字符串让上游静默收下一张空图。
func imageSource(imageURL string) (*claudeImageSource, error) {
	if !strings.HasPrefix(imageURL, "data:") {
		return nil, fmt.Errorf("%w: Claude 只接受 base64 内联图片，收到 URL %q（请先把图片转成 data URL）", ErrUnsupported, imageURL)
	}
	// data:<mime>;base64,<data>
	rest := strings.TrimPrefix(imageURL, "data:")
	parts := strings.SplitN(rest, ";base64,", 2)
	if len(parts) != 2 {
		return nil, fmt.Errorf("%w: 无法解析 data URL %q", ErrUnsupported, imageURL)
	}
	return &claudeImageSource{Type: "base64", MediaType: parts[0], Data: parts[1]}, nil
}

// appendToolResult 把规范 tool 消息追加成 user 消息的 tool_result 块。
func appendToolResult(out *claudeRequest, m canonical.Message) {
	text := canonical.TextOf(m)
	block := claudeContent{
		Type:      "tool_result",
		ToolUseID: m.ToolUseID,
		Content:   text,
	}
	for _, p := range m.Content {
		if p.Type == canonical.PartToolResult {
			block.IsError = p.IsError
		}
	}
	// 挂到最后一条 user 消息上；没有 user 消息就新建一条
	if n := len(out.Messages); n > 0 && out.Messages[n-1].Role == canonical.RoleUser {
		out.Messages[n-1].Content = append(out.Messages[n-1].Content, block)
		return
	}
	out.Messages = append(out.Messages, claudeMessage{
		Role:    canonical.RoleUser,
		Content: []claudeContent{block},
	})
}

// mergeToolResultMessages 合并相邻的、只含 tool_result 的 user 消息。
//
// 为什么需要：OpenAI 客户端把多个工具调用结果拆成多条 role=tool 消息，
// 上面每条都生成了独立的 user 消息。Claude 要求同一批 tool_use 的结果
// 合并在一条 user 消息里，否则报错。相邻合并是安全的：tool_result 之间
// 没有顺序依赖（都在回复同一批 tool_use）。
func mergeToolResultMessages(msgs []claudeMessage) []claudeMessage {
	out := make([]claudeMessage, 0, len(msgs))
	for _, m := range msgs {
		if len(out) > 0 && isToolResultOnly(m) && isToolResultOnly(out[len(out)-1]) {
			last := &out[len(out)-1]
			last.Content = append(last.Content, m.Content...)
			continue
		}
		out = append(out, m)
	}
	return out
}

// isToolResultOnly 判断消息是否只含 tool_result 块。
func isToolResultOnly(m claudeMessage) bool {
	if m.Role != canonical.RoleUser || len(m.Content) == 0 {
		return false
	}
	for _, b := range m.Content {
		if b.Type != "tool_result" {
			return false
		}
	}
	return true
}

// ---- 响应转换 ----

// ParseResponse 把 Claude 非流式响应解析成规范格式。
func ParseResponse(body []byte) (*canonical.ChatResponse, error) {
	var raw claudeResponse
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, err
	}
	resp := &canonical.ChatResponse{
		ID:           raw.ID,
		Model:        raw.Model,
		FinishReason: mapStopReason(raw.StopReason),
		Usage:        raw.Usage.toUsage(),
	}
	for _, blk := range raw.Content {
		switch blk.Type {
		case "text":
			resp.Content = append(resp.Content, canonical.ContentPart{Type: canonical.PartText, Text: blk.Text})
		case "thinking":
			resp.Content = append(resp.Content, canonical.ContentPart{Type: canonical.PartThinking, Text: blk.Text})
		case "tool_use":
			resp.Content = append(resp.Content, canonical.ContentPart{
				Type:      canonical.PartToolUse,
				ToolUseID: blk.ID,
				Name:      blk.Name,
				Input:     blk.Input,
			})
		}
	}
	return resp, nil
}

// BuildResponse 把规范响应构造成 Claude 响应体。
func BuildResponse(resp *canonical.ChatResponse) ([]byte, error) {
	out := claudeResponse{
		ID:         resp.ID,
		Type:       "message",
		Role:       "assistant",
		Model:      resp.Model,
		StopReason: unmapStopReason(resp.FinishReason),
	}
	if out.ID == "" {
		out.ID = "msg_gateway"
	}
	for _, p := range resp.Content {
		switch p.Type {
		case canonical.PartText:
			out.Content = append(out.Content, claudeContent{Type: "text", Text: p.Text})
		case canonical.PartThinking:
			out.Content = append(out.Content, claudeContent{Type: "thinking", Text: p.Text})
		case canonical.PartToolUse:
			input := p.Input
			if len(input) == 0 {
				input = json.RawMessage(`{}`)
			}
			out.Content = append(out.Content, claudeContent{Type: "tool_use", ID: p.ToolUseID, Name: p.Name, Input: input})
		}
	}
	if len(out.Content) == 0 {
		out.Content = []claudeContent{{Type: "text", Text: ""}}
	}
	if u := resp.Usage; u != nil {
		out.Usage = claudeUsage{InputTokens: u.PromptTokens, OutputTokens: u.CompletionTokens}
	}
	return json.Marshal(out)
}

// ---- 结束原因映射 ----

// mapStopReason 把 Claude 的 stop_reason 映射成规范（OpenAI 词汇表）。
//
// 映射表（Claude 官方文档）：
//
//	end_turn       → stop       正常结束
//	max_tokens     → length     触顶
//	stop_sequence  → stop       命中停止序列
//	tool_use       → tool_calls 要调工具
//	pause_turn     → stop       长任务暂停（罕见，归到 stop）
func mapStopReason(reason string) string {
	switch reason {
	case "end_turn", "stop_sequence", "pause_turn":
		return canonical.FinishStop
	case "max_tokens":
		return canonical.FinishLength
	case "tool_use":
		return canonical.FinishToolCalls
	case "":
		return ""
	default:
		// 未知原因不猜：原样返回，客户端至少能看到上游的真实值
		return reason
	}
}

// unmapStopReason 反向映射：规范 → Claude。
func unmapStopReason(reason string) string {
	switch reason {
	case canonical.FinishStop:
		return "end_turn"
	case canonical.FinishLength:
		return "max_tokens"
	case canonical.FinishToolCalls:
		return "tool_use"
	case "":
		return ""
	default:
		return "end_turn"
	}
}

// ---- Extra 字段处理 ----

// claudeSupportedExtra 是「Claude 能表达、但规范格式没建模」的字段白名单。
// 白名单之外的 Extra 字段会在 BuildRequest 里被丢弃并记警告——
// 不静默：日志里会写明丢了什么。
var claudeSupportedExtra = map[string]bool{
	// Claude 支持但规范未建模的：
	"top_k":        true, // Claude 特有采样参数
	"metadata":     true, // 请求元数据
	"thinking":     true, // 扩展思考配置（type/budget_tokens）
	"service_tier": true, // 服务层级
	// 注意：frequency_penalty / presence_penalty / seed / n 等
	// OpenAI 参数 Claude 完全不支持，不在白名单里——出现即记警告。
}

// claudeExtraFields 从 Claude 请求体里摘出未建模字段。
func claudeExtraFields(body []byte) map[string]json.RawMessage {
	return extractExtra(body, "model", "messages", "system", "max_tokens",
		"temperature", "top_p", "stop_sequences", "stream", "tools")
}
