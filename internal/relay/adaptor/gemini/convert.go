// Package gemini 实现 Google Gemini 协议适配器。
//
// 协议要点（与 OpenAI 的差异）：
//   - 鉴权：key 拼在 URL query 上（?key=xxx），不用鉴权头
//   - 端点：POST /v1beta/models/{model}:generateContent
//     流式是 :streamGenerateContent?alt=sse（用 query 参数声明 SSE，不是 Accept 头）
//   - role 只有 user/model，没有 assistant/system：
//     assistant → model，system → 顶层 systemInstruction 字段
//   - 请求体：contents[{role, parts[{text|inlineData|functionCall|functionResponse}]}]
//   - 工具：tools[{function_declarations:[{name,description,parameters}]}]
//     （注意 function_declarations 是数组包一层，和 Claude 的扁平 tools 不同）
//   - usage 叫 usageMetadata：promptTokenCount / candidatesTokenCount /
//     totalTokenCount（名字和 OpenAI 完全不同，要逐个映射）
//   - 流式：每个 SSE data 是一条完整的 GenerateContentResponse 快照
//     （不是增量 delta！一条里可能同时含 parts 和 finishReason）
//   - finishReason 枚举：STOP / MAX_TOKENS / SAFETY / RECITATION / OTHER
//
// 最大的坑：Gemini 流式不是 delta 语义。OpenAI 的每个 chunk 是增量，
// Gemini 的每条 SSE 是该候选当前的「完整快照」。翻译时必须把快照
// 当成增量处理（取最后新增的部分），否则客户端会收到重复文本。
package gemini

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"minigateway/internal/relay/canonical"
	"minigateway/internal/relay/model"
)

// ErrUnsupported 标记「Gemini 表达不了的请求」。
var ErrUnsupported = errors.New("gemini: 不支持的参数")

// ---- 请求方向结构体 ----

// geminiRequest 是 Gemini 的请求体。
type geminiRequest struct {
	Contents          []geminiContent  `json:"contents"`
	SystemInstruction *geminiContent   `json:"systemInstruction,omitempty"`
	GenerationConfig  *geminiGenConfig `json:"generationConfig,omitempty"`
	Tools             []geminiToolSet  `json:"tools,omitempty"`
}

// geminiContent 是 Gemini 的内容块。parts 是分片数组。
type geminiContent struct {
	Role  string       `json:"role,omitempty"`
	Parts []geminiPart `json:"parts"`
}

// geminiPart 是 Gemini 的内容分片。
// 用 omitempty 让不同类型只出现自己的字段。
type geminiPart struct {
	Text             string                  `json:"text,omitempty"`
	InlineData       *geminiInlineData       `json:"inlineData,omitempty"`
	FunctionCall     *geminiFunctionCall     `json:"functionCall,omitempty"`
	FunctionResponse *geminiFunctionResponse `json:"functionResponse,omitempty"`
}

// geminiInlineData 是内联二进制数据（base64）。
type geminiInlineData struct {
	MimeType string `json:"mimeType"`
	Data     string `json:"data"`
}

// geminiFunctionCall 是模型发起的函数调用。
type geminiFunctionCall struct {
	Name string          `json:"name"`
	Args json.RawMessage `json:"args,omitempty"`
}

// geminiFunctionResponse 是函数执行结果。
// Gemini 没有 tool_use_id 概念——靠 name 匹配调用。
type geminiFunctionResponse struct {
	Name     string          `json:"name"`
	Response json.RawMessage `json:"response,omitempty"`
}

// geminiGenConfig 是生成配置。
type geminiGenConfig struct {
	Temperature     *float64 `json:"temperature,omitempty"`
	TopP            *float64 `json:"topP,omitempty"`
	MaxOutputTokens int      `json:"maxOutputTokens,omitempty"`
	StopSequences   []string `json:"stopSequences,omitempty"`
	CandidateCount  int      `json:"candidateCount,omitempty"`
	Seed            *int64   `json:"seed,omitempty"`
}

// geminiToolSet 是 Gemini 的工具集合。
// 注意多包了一层 function_declarations 数组——和 Claude 的扁平结构不同。
type geminiToolSet struct {
	FunctionDeclarations []geminiFunctionDecl `json:"functionDeclarations"`
}

// geminiFunctionDecl 是函数声明。parameters 直接是 JSON Schema。
type geminiFunctionDecl struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

// ---- 响应方向结构体 ----

// geminiResponse 是 Gemini 的非流式响应。
type geminiResponse struct {
	Candidates []geminiCandidate `json:"candidates"`
	Usage      *geminiUsage      `json:"usageMetadata,omitempty"`
	// promptFeedback 在请求被安全策略拦截时出现（candidates 为空）
	PromptFeedback *struct {
		BlockReason string `json:"blockReason,omitempty"`
	} `json:"promptFeedback,omitempty"`
}

// geminiCandidate 是候选回答。
type geminiCandidate struct {
	Content      geminiContent `json:"content"`
	FinishReason string        `json:"finishReason,omitempty"`
	Index        int           `json:"index,omitempty"`
}

// geminiUsage 是 Gemini 的用量。
// 字段名和 OpenAI 完全不同，逐个映射。
type geminiUsage struct {
	PromptTokenCount     int `json:"promptTokenCount"`
	CandidatesTokenCount int `json:"candidatesTokenCount"`
	TotalTokenCount      int `json:"totalTokenCount"`
}

// toUsage 转成网关内部 Usage。
// totalTokenCount 缺失时自己加（Gemini 老版本可能不给 total）。
func (u *geminiUsage) toUsage() *model.Usage {
	if u == nil {
		return nil
	}
	total := u.TotalTokenCount
	if total == 0 {
		total = u.PromptTokenCount + u.CandidatesTokenCount
	}
	return &model.Usage{
		PromptTokens:     u.PromptTokenCount,
		CompletionTokens: u.CandidatesTokenCount,
		TotalTokens:      total,
	}
}

// ---- 请求转换 ----

// ParseRequest 把 Gemini 请求体解析成规范格式。
func ParseRequest(body []byte) (*canonical.ChatRequest, error) {
	var raw geminiRequest
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, err
	}

	req := &canonical.ChatRequest{}

	// systemInstruction → 规范 system 消息（放最前面）
	if raw.SystemInstruction != nil && len(raw.SystemInstruction.Parts) > 0 {
		var text string
		for _, p := range raw.SystemInstruction.Parts {
			text += p.Text
		}
		if text != "" {
			req.Messages = append(req.Messages, canonical.Message{
				Role:    canonical.RoleSystem,
				Content: []canonical.ContentPart{{Type: canonical.PartText, Text: text}},
			})
		}
	}

	for _, c := range raw.Contents {
		// Gemini 的 model 角色 → 规范的 assistant
		role := c.Role
		switch role {
		case "model":
			role = canonical.RoleAssistant
		case "user":
			role = canonical.RoleUser
		case "":
			role = canonical.RoleUser
		default:
			return nil, fmt.Errorf("%w: Gemini 不支持角色 %q", ErrUnsupported, c.Role)
		}

		msg := canonical.Message{Role: role}
		for _, p := range c.Parts {
			switch {
			case p.Text != "":
				msg.Content = append(msg.Content, canonical.ContentPart{Type: canonical.PartText, Text: p.Text})
			case p.InlineData != nil:
				// base64 内联数据 → data URL
				msg.Content = append(msg.Content, canonical.ContentPart{
					Type:     canonical.PartImage,
					ImageURL: fmt.Sprintf("data:%s;base64,%s", p.InlineData.MimeType, p.InlineData.Data),
				})
			case p.FunctionCall != nil:
				msg.Content = append(msg.Content, canonical.ContentPart{
					Type:  canonical.PartToolUse,
					Name:  p.FunctionCall.Name,
					Input: p.FunctionCall.Args,
					// Gemini 没有调用 ID，用函数名兜底（出站时映射回 name）
					ToolUseID: p.FunctionCall.Name,
				})
			case p.FunctionResponse != nil:
				// 函数结果是 user 角色的 part。规范里拆成 role=tool 消息。
				// Gemini 靠 name 匹配调用，没有 ID——这里 Name 就是关键。
				text := ""
				if len(p.FunctionResponse.Response) > 0 {
					text = string(p.FunctionResponse.Response)
				}
				req.Messages = append(req.Messages, canonical.Message{
					Role:      canonical.RoleTool,
					ToolUseID: p.FunctionResponse.Name,
					Name:      p.FunctionResponse.Name,
					Content:   []canonical.ContentPart{{Type: canonical.PartToolResult, Text: text}},
				})
			}
		}
		if len(msg.Content) > 0 {
			req.Messages = append(req.Messages, msg)
		}
	}

	// 工具声明：从 tools[].functionDeclarations 拍平
	for _, ts := range raw.Tools {
		for _, fd := range ts.FunctionDeclarations {
			req.Tools = append(req.Tools, canonical.Tool{
				Name:        fd.Name,
				Description: fd.Description,
				Parameters:  fd.Parameters,
			})
		}
	}

	// generationConfig → 规范参数
	if raw.GenerationConfig != nil {
		gc := raw.GenerationConfig
		req.Temperature = gc.Temperature
		req.TopP = gc.TopP
		req.MaxTokens = gc.MaxOutputTokens
		req.Stop = gc.StopSequences
	}

	// contents 在 Gemini 里叫 contents，规范 Extra 记原名以便回写
	req.Extra = geminiExtraFields(body)
	return req, nil
}

// BuildRequest 把规范请求构造成 Gemini 请求体。
func BuildRequest(req *canonical.ChatRequest, modelName string) ([]byte, error) {
	out := geminiRequest{}
	var genCfg geminiGenConfig
	hasGenCfg := false

	if req.Temperature != nil {
		genCfg.Temperature = req.Temperature
		hasGenCfg = true
	}
	if req.TopP != nil {
		genCfg.TopP = req.TopP
		hasGenCfg = true
	}
	if req.MaxTokens > 0 {
		genCfg.MaxOutputTokens = req.MaxTokens
		hasGenCfg = true
	}
	if len(req.Stop) > 0 {
		genCfg.StopSequences = req.Stop
		hasGenCfg = true
	}
	if hasGenCfg {
		out.GenerationConfig = &genCfg
	}

	for _, m := range req.Messages {
		switch m.Role {
		case canonical.RoleSystem:
			// system → 顶层 systemInstruction
			text := canonical.TextOf(m)
			if text != "" {
				if out.SystemInstruction == nil {
					out.SystemInstruction = &geminiContent{}
				}
				out.SystemInstruction.Parts = append(out.SystemInstruction.Parts, geminiPart{Text: text})
			}
		case canonical.RoleTool:
			// 工具结果 → user 角色的 functionResponse part
			// Gemini 没有 tool_use_id，靠 name 匹配。规范里的 Name 字段
			// 在入站时已经从 tool_call_id 补过（见 ParseRequest），
			// 这里直接用它；为空说明映射失败，明确报错。
			name := m.Name
			if name == "" {
				name = m.ToolUseID
			}
			if name == "" {
				return nil, fmt.Errorf("%w: 工具结果缺少函数名（Gemini 的 functionResponse 要求 name）", ErrUnsupported)
			}
			resp := json.RawMessage(`{}`)
			if t := canonical.TextOf(m); t != "" {
				// Gemini 的 response 是任意 JSON 对象，文本结果包一层
				resp = json.RawMessage(`{"result":` + jsonString(t) + `}`)
			}
			appendUserPart(&out, geminiPart{
				FunctionResponse: &geminiFunctionResponse{Name: name, Response: resp},
			})
		case canonical.RoleUser:
			parts, err := contentParts(m)
			if err != nil {
				return nil, err
			}
			out.Contents = append(out.Contents, geminiContent{Role: "user", Parts: parts})
		case canonical.RoleAssistant:
			parts, err := contentParts(m)
			if err != nil {
				return nil, err
			}
			// Gemini 的 assistant 叫 model
			out.Contents = append(out.Contents, geminiContent{Role: "model", Parts: parts})
		default:
			return nil, fmt.Errorf("%w: Gemini 不支持角色 %q", ErrUnsupported, m.Role)
		}
	}

	// 工具：拍平进 functionDeclarations
	if len(req.Tools) > 0 {
		var decls []geminiFunctionDecl
		for _, t := range req.Tools {
			decls = append(decls, geminiFunctionDecl{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  t.Parameters,
			})
		}
		out.Tools = []geminiToolSet{{FunctionDeclarations: decls}}
	}

	body, err := json.Marshal(out)
	if err != nil {
		return nil, err
	}
	// 注意：Gemini 的模型名不在 body 里（在 URL path），
	// 所以这里不 mergeExtra 的 model 字段处理——Extra 只含 body 级字段。
	return mergeExtra(body, req.Extra, geminiSupportedExtra)
}

// contentParts 把规范内容块转成 Gemini part。
func contentParts(m canonical.Message) ([]geminiPart, error) {
	var parts []geminiPart
	for _, p := range m.Content {
		switch p.Type {
		case canonical.PartText:
			parts = append(parts, geminiPart{Text: p.Text})
		case canonical.PartThinking:
			// Gemini 没有思考块：转成 text。
			// 降级（不是丢弃）——内容保留，结构信息丢失。
			parts = append(parts, geminiPart{Text: p.Text})
		case canonical.PartImage:
			mime, data, err := decodeDataURL(p.ImageURL)
			if err != nil {
				return nil, fmt.Errorf("%w: %v", ErrUnsupported, err)
			}
			parts = append(parts, geminiPart{
				InlineData: &geminiInlineData{MimeType: mime, Data: data},
			})
		case canonical.PartToolUse:
			// 工具调用 → functionCall part。args 是任意 JSON。
			args := p.Input
			if len(args) == 0 {
				args = json.RawMessage(`{}`)
			}
			parts = append(parts, geminiPart{
				FunctionCall: &geminiFunctionCall{Name: p.Name, Args: args},
			})
		case canonical.PartToolResult:
			// tool_result 出现在 assistant 消息里是异常情况
			//（规范里它应该已经在 role=tool 消息中）。跳过而不是报错：
			// 这是网关内部表示的问题，不该让用户看到。
		}
	}
	if len(parts) == 0 {
		// Gemini 的 parts 不能为空数组，兜底塞空格
		parts = []geminiPart{{Text: " "}}
	}
	return parts, nil
}

// appendUserPart 把 part 追加到最后一条 user content 上，没有就新建。
// Gemini 没有「同一批结果必须在一条消息里」的硬性要求（不像 Claude），
// 但合并相邻 user 消息仍然是好习惯：减少 API 往返。
func appendUserPart(out *geminiRequest, part geminiPart) {
	if n := len(out.Contents); n > 0 && out.Contents[n-1].Role == "user" {
		out.Contents[n-1].Parts = append(out.Contents[n-1].Parts, part)
		return
	}
	out.Contents = append(out.Contents, geminiContent{Role: "user", Parts: []geminiPart{part}})
}

// decodeDataURL 解析 data URL（data:<mime>;base64,<data>）。
func decodeDataURL(imageURL string) (mime, data string, err error) {
	if !strings.HasPrefix(imageURL, "data:") {
		return "", "", fmt.Errorf("Gemini 只接受 base64 内联数据，收到 URL %q", imageURL)
	}
	rest := strings.TrimPrefix(imageURL, "data:")
	parts := strings.SplitN(rest, ";base64,", 2)
	if len(parts) != 2 {
		return "", "", fmt.Errorf("无法解析 data URL %q", imageURL)
	}
	return parts[0], parts[1], nil
}

// ---- 响应转换 ----

// ParseResponse 把 Gemini 非流式响应解析成规范格式。
func ParseResponse(body []byte) (*canonical.ChatResponse, error) {
	var raw geminiResponse
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, err
	}

	resp := &canonical.ChatResponse{Usage: raw.Usage.toUsage()}

	// 被安全策略拦截：candidates 为空，promptFeedback.blockReason 有值
	if len(raw.Candidates) == 0 {
		if raw.PromptFeedback != nil && raw.PromptFeedback.BlockReason != "" {
			resp.FinishReason = canonical.FinishContentFilter
			resp.Content = []canonical.ContentPart{{
				Type: canonical.PartText,
				Text: "[被 Gemini 安全策略拦截: " + raw.PromptFeedback.BlockReason + "]",
			}}
			return resp, nil
		}
		return nil, errors.New("gemini: 响应缺少 candidates")
	}

	cand := raw.Candidates[0]
	resp.Model = "" // Gemini 响应不带模型名，由上层（relay）用请求时的模型名填充
	resp.FinishReason = mapFinishReason(cand.FinishReason)

	for _, p := range cand.Content.Parts {
		switch {
		case p.Text != "":
			resp.Content = append(resp.Content, canonical.ContentPart{Type: canonical.PartText, Text: p.Text})
		case p.FunctionCall != nil:
			resp.Content = append(resp.Content, canonical.ContentPart{
				Type:      canonical.PartToolUse,
				ToolUseID: p.FunctionCall.Name,
				Name:      p.FunctionCall.Name,
				Input:     p.FunctionCall.Args,
			})
		}
	}
	return resp, nil
}

// BuildResponse 把规范响应构造成 Gemini 响应体。
func BuildResponse(resp *canonical.ChatResponse) ([]byte, error) {
	cand := geminiCandidate{
		Content:      geminiContent{Role: "model"},
		FinishReason: unmapFinishReason(resp.FinishReason),
	}
	for _, p := range resp.Content {
		switch p.Type {
		case canonical.PartText:
			cand.Content.Parts = append(cand.Content.Parts, geminiPart{Text: p.Text})
		case canonical.PartThinking:
			cand.Content.Parts = append(cand.Content.Parts, geminiPart{Text: p.Text})
		case canonical.PartToolUse:
			args := p.Input
			if len(args) == 0 {
				args = json.RawMessage(`{}`)
			}
			cand.Content.Parts = append(cand.Content.Parts, geminiPart{
				FunctionCall: &geminiFunctionCall{Name: p.Name, Args: args},
			})
		}
	}
	out := geminiResponse{Candidates: []geminiCandidate{cand}}
	if u := resp.Usage; u != nil {
		out.Usage = &geminiUsage{
			PromptTokenCount:     u.PromptTokens,
			CandidatesTokenCount: u.CompletionTokens,
			TotalTokenCount:      u.TotalTokens,
		}
	}
	return json.Marshal(out)
}

// ---- finishReason 映射 ----

// mapFinishReason 把 Gemini 的 finishReason 映射成规范（OpenAI 词汇表）。
//
// STOP       → stop
// MAX_TOKENS → length
// SAFETY     → content_filter
// RECITATION → content_filter（引用内容过滤，语义接近）
// OTHER      → stop（兜底）
func mapFinishReason(reason string) string {
	switch reason {
	case "STOP", "":
		return canonical.FinishStop
	case "MAX_TOKENS":
		return canonical.FinishLength
	case "SAFETY", "RECITATION":
		return canonical.FinishContentFilter
	case "OTHER":
		return canonical.FinishStop
	default:
		return reason
	}
}

// unmapFinishReason 反向映射：规范 → Gemini。
func unmapFinishReason(reason string) string {
	switch reason {
	case canonical.FinishStop, "":
		return "STOP"
	case canonical.FinishLength:
		return "MAX_TOKENS"
	case canonical.FinishContentFilter:
		return "SAFETY"
	case canonical.FinishToolCalls:
		// Gemini 的工具调用完成也是 STOP
		return "STOP"
	default:
		return "STOP"
	}
}

// ---- Extra 字段处理 ----

// geminiSupportedExtra 是 Gemini 能表达、规范未建模的字段白名单。
var geminiSupportedExtra = map[string]bool{
	"candidateCount":   true, // 候选数量 → generationConfig.candidateCount
	"seed":             true, // 随机种子
	"topK":             true, // 采样参数
	"safetySettings":   true, // 安全设置
	"generationConfig": true, // 整个生成配置（客户端直接传时）
}

// geminiExtraFields 从 Gemini 请求体里摘出未建模字段。
// 注意 contents/model 之类的字段不在此列：contents 已建模，
// model 根本不在 body 里（在 URL path）。
func geminiExtraFields(body []byte) map[string]json.RawMessage {
	return extractExtra(body, "contents", "systemInstruction",
		"generationConfig", "tools")
}

// jsonString 把字符串包成 JSON 字面量。
func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// BuildURL 构造 Gemini 的上游 URL。
//
// 关键点：
//  1. 模型名在 path 里：/v1beta/models/{model}:generateContent
//  2. key 在 query 里：?key=xxx
//  3. 流式用 :streamGenerateContent + alt=sse（不是 Accept 头）
//
// 模型名要做 URL 转义：模型名里可能带点（gemini-1.5-pro），
// 直接拼会有路径注入风险（虽然内部配置，但转义是好习惯）。
func BuildURL(baseURL, modelName, apiKey string, stream bool) (string, error) {
	action := ":generateContent"
	if stream {
		action = ":streamGenerateContent"
	}
	path := "/v1beta/models/" + url.PathEscape(modelName) + action

	q := url.Values{}
	q.Set("key", apiKey)
	if stream {
		q.Set("alt", "sse")
	}

	return strings.TrimSuffix(baseURL, "/") + path + "?" + q.Encode(), nil
}
