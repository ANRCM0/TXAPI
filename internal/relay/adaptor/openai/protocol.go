package openai

import (
	"encoding/json"
	"strings"

	"minigateway/internal/relay/canonical"
)

// 下面是 ProtocolAdaptor 的实现（协议层转换）。
//
// 规范格式就是以 OpenAI Chat Completions 为基准定义的，所以本文件
// 的转换大多是「直通 + 少量字段规范化」，逻辑量远小于 anthropic/gemini。
// 但它的存在很关键：它让「客户端协议」也能走同一条枢纽路径——
// 将来若支持 Claude 客户端 / Gemini 客户端，只需要在这里注册，
// 出站侧不用动。

// Name 返回协议名。
func (a *Adaptor) Name() string { return "openai" }

// DefaultBaseURL 是 OpenAI 官方地址。
func (a *Adaptor) DefaultBaseURL() string { return "https://api.openai.com" }

// AuthHeader 返回 Bearer 鉴权头。
func (a *Adaptor) AuthHeader(apiKey string) (string, string) {
	return "Authorization", "Bearer " + apiKey
}

// ---- 请求方向：OpenAI → 规范 ----

// ParseRequest 解析 OpenAI 请求体。
//
// content 字段允许「字符串」和「分片数组」两种写法（多模态时是数组），
// 这里统一折叠成 []ContentPart，让下游所有适配器只面对一种形态。
func (a *Adaptor) ParseRequest(body []byte) (*canonical.ChatRequest, error) {
	var raw struct {
		Model       string            `json:"model"`
		Messages    []rawMessage      `json:"messages"`
		MaxTokens   int               `json:"max_tokens"`
		Temperature *float64          `json:"temperature"`
		TopP        *float64          `json:"top_p"`
		Stop        json.RawMessage   `json:"stop"` // 可能是字符串也可能是数组
		Stream      bool              `json:"stream"`
		Tools       []rawTool         `json:"tools"`
		Extra       map[string]json.RawMessage
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, err
	}

	req := &canonical.ChatRequest{
		Model:       raw.Model,
		MaxTokens:   raw.MaxTokens,
		Temperature: raw.Temperature,
		TopP:        raw.TopP,
		Stream:      raw.Stream,
	}
	req.Stop = parseStop(raw.Stop)

	for _, m := range raw.Messages {
		req.Messages = append(req.Messages, m.toCanonical())
	}

	for _, t := range raw.Tools {
		req.Tools = append(req.Tools, canonical.Tool{
			Name:        t.Function.Name,
			Description: t.Function.Description,
			Parameters:  t.Function.Parameters,
		})
	}

	// 收下未建模的字段（n、seed、user、response_format 之类）。
	// 出站适配器决定是映射、透传还是报错——绝不在这里悄悄丢掉。
	req.Extra = extractExtra(body, "model", "messages", "max_tokens",
		"temperature", "top_p", "stop", "stream", "tools")
	return req, nil
}

// BuildRequest 把规范请求构造成 OpenAI 请求体。
//
// 因为规范格式就是 OpenAI 形状，这里几乎是原样落回结构体；
// 消息的 content 形态（字符串 vs 数组）由 canonical.ToOpenAIMessage 决定。
func (a *Adaptor) BuildRequest(req *canonical.ChatRequest) ([]byte, error) {
	out := openAIRequest{
		Model:       req.Model,
		MaxTokens:   req.MaxTokens,
		Temperature: req.Temperature,
		TopP:        req.TopP,
		Stop:        req.Stop,
		Stream:      req.Stream,
	}
	for _, m := range req.Messages {
		encoded, err := json.Marshal(canonical.ToOpenAIMessage(m))
		if err != nil {
			return nil, err
		}
		out.Messages = append(out.Messages, encoded)
	}
	for _, t := range req.Tools {
		out.Tools = append(out.Tools, rawTool{
			Type: "function",
			Function: rawFunction{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  t.Parameters,
			},
		})
	}
	body, err := json.Marshal(out)
	if err != nil {
		return nil, err
	}
	return mergeExtra(body, req.Extra)
}

// ---- 响应方向：OpenAI → 规范 ----

// ParseResponse 解析 OpenAI 非流式响应。
func (a *Adaptor) ParseResponse(body []byte) (*canonical.ChatResponse, error) {
	var raw struct {
		ID      string `json:"id"`
		Model   string `json:"model"`
		Choices []struct {
			Message struct {
				Role      string     `json:"role"`
				Content   rawContent `json:"content"`
				ToolCalls []struct {
					ID       string `json:"id"`
					Type     string `json:"type"`
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage *usageJSON `json:"usage"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, err
	}
	if len(raw.Choices) == 0 {
		return nil, ErrNoChoices
	}

	ch := raw.Choices[0]
	resp := &canonical.ChatResponse{
		ID:           raw.ID,
		Model:        raw.Model,
		FinishReason: ch.FinishReason,
		Usage:        raw.Usage.toUsage(),
	}
	// content 可能是 null（纯工具调用时 OpenAI 就返回 null），空 content 不产生 Part
	resp.Content = append(resp.Content, ch.Message.Content.toParts()...)
	for _, tc := range ch.Message.ToolCalls {
		resp.Content = append(resp.Content, canonical.ContentPart{
			Type:      canonical.PartToolUse,
			ToolUseID: tc.ID,
			Name:      tc.Function.Name,
			Input:     json.RawMessage(tc.Function.Arguments),
		})
	}
	return resp, nil
}

// BuildResponse 把规范响应还原成 OpenAI 非流式响应。
func (a *Adaptor) BuildResponse(resp *canonical.ChatResponse) ([]byte, error) {
	message, err := json.Marshal(canonical.ToOpenAIMessage(canonical.Message{
		Role:    canonical.RoleAssistant,
		Content: resp.Content,
	}))
	if err != nil {
		return nil, err
	}
	out := openAIResponse{
		ID:     resp.ID,
		Object: "chat.completion",
		Model:  resp.Model,
		Choices: []openAIChoice{{
			Index:        0,
			FinishReason: resp.FinishReason,
			Message:      message,
		}},
	}
	if u := resp.Usage; u != nil {
		out.Usage = &usageJSON{
			PromptTokens:     u.PromptTokens,
			CompletionTokens: u.CompletionTokens,
			TotalTokens:      u.TotalTokens,
		}
	}
	return json.Marshal(out)
}

// ---- 流式方向：OpenAI → 规范 ----

// ParseStreamLine 解析一行 OpenAI SSE。
//
// 返回空切片表示这行不产生增量（空行、[DONE] 之外的注释行等）。
func (a *Adaptor) ParseStreamLine(line []byte) ([]*canonical.StreamChunk, error) {
	data, ok := sseData(line)
	if !ok {
		return nil, nil
	}
	if data == "[DONE]" {
		return nil, nil
	}
	var raw struct {
		ID      string `json:"id"`
		Model   string `json:"model"`
		Choices []struct {
			Delta struct {
				Role      string     `json:"role"`
				Content   rawContent `json:"content"`
				ToolCalls []struct {
					Index    int    `json:"index"`
					ID       string `json:"id"`
					Type     string `json:"type"`
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"delta"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage *usageJSON `json:"usage"`
	}
	if err := json.Unmarshal([]byte(data), &raw); err != nil {
		// 上游发了坏行：不抛错中断整条流，跳过这一行（坏行不该拖死整个会话）
		return nil, nil
	}

	chunk := &canonical.StreamChunk{ID: raw.ID, Model: raw.Model, Usage: raw.Usage.toUsage()}
	if len(raw.Choices) > 0 {
		ch := raw.Choices[0]
		chunk.Role = ch.Delta.Role
		chunk.Content = ch.Delta.Content.toParts()
		chunk.FinishReason = ch.FinishReason
		for _, tc := range ch.Delta.ToolCalls {
			chunk.ToolCalls = append(chunk.ToolCalls, canonical.ToolCallDelta{
				Index:     tc.Index,
				ID:        tc.ID,
				Name:      tc.Function.Name,
				Arguments: tc.Function.Arguments,
			})
		}
	}
	return []*canonical.StreamChunk{chunk}, nil
}

// BuildStreamLine 把规范 chunk 序列化成 OpenAI 的一行 SSE。
//
// usage-only 的尾帧（choices 为空）要保留，否则计费拿不到用量；
// 只有增量全空且无结束原因时才跳过。
func (a *Adaptor) BuildStreamLine(chunk *canonical.StreamChunk) ([]byte, error) {
	delta := openAIDelta{Role: chunk.Role}
	// content 分片拼回字符串：OpenAI 流式只认 content 字符串。
	// thinking 块也拼进 content——客户端没有 reasoning_content 的约定时，
	// 丢掉思考内容等于丢数据；主流客户端（Cherry Studio 等）会原样显示。
	var sb strings.Builder
	for _, p := range chunk.Content {
		if p.Type == canonical.PartText || p.Type == canonical.PartThinking {
			sb.WriteString(p.Text)
		}
	}
	if sb.Len() > 0 {
		delta.Content = rawContent(sb.String())
	}
	for _, tc := range chunk.ToolCalls {
		delta.ToolCalls = append(delta.ToolCalls, openAIToolCallDelta{
			Index: tc.Index,
			ID:    tc.ID,
			Type:  "function",
			Function: openAIFunctionDelta{
				Name:      tc.Name,
				Arguments: tc.Arguments,
			},
		})
	}

	empty := delta.Role == "" && delta.Content == "" && len(delta.ToolCalls) == 0
	if empty && chunk.FinishReason == "" && chunk.Usage == nil {
		return nil, nil
	}

	out := openAIChunk{
		ID:     chunk.ID,
		Object: "chat.completion.chunk",
		Model:  chunk.Model,
		Choices: []openAIChunkChoice{{
			Index:        0,
			Delta:        delta,
			FinishReason: chunk.FinishReason,
		}},
	}
	if u := chunk.Usage; u != nil {
		out.Usage = &usageJSON{
			PromptTokens:     u.PromptTokens,
			CompletionTokens: u.CompletionTokens,
			TotalTokens:      u.TotalTokens,
		}
	}
	// 尾帧（只有 usage）时 choices 应为空数组，和 OpenAI 行为一致
	if empty {
		out.Choices = nil
	}
	return json.Marshal(out)
}
