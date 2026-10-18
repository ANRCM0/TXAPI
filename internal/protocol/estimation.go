package protocol

import (
	"bytes"
	"encoding/json"
	"strings"
)

// PromptTextFromBody 从 OpenAI 格式的请求体里抽出全部输入文本。
//
// 为什么不直接按整个 body 长度估：JSON 的语法符号（引号、字段名、
// 转义符）会被当成几百个假 token，让估算严重偏高。只数真实文本内容
// 才接近真实 prompt 规模。
//
// 兼容两种请求形态：
//   - 对话：messages[].content（P1 起就有）
//   - embeddings：input，可以是字符串或字符串数组（P6 批次 3 加的
//     /v1/embeddings 端点走这条）。embeddings 请求体里根本没有
//     messages 字段，不认 input 的话这类请求的估算恒为 0——
//     TPM 预估值和缺 usage 时的降级计费都会失真。
//
// 解析失败时返回空串而不是报错：估算是降级路径，宁可得 0 也不能
// 让记账逻辑把请求搞挂。
func PromptTextFromBody(body []byte) string {
	var req struct {
		Messages []struct {
			Content any `json:"content"`
		} `json:"messages"`
		// Input 是 embeddings 端点的输入。any 是因为它既可以是
		// 单个字符串，也可以是字符串数组（批量向量化）。
		Input any `json:"input"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		return ""
	}
	var sb strings.Builder
	for i := range req.Messages {
		sb.WriteString(contentText(req.Messages[i].Content))
		sb.WriteByte('\n')
	}
	sb.WriteString(inputText(req.Input))
	return sb.String()
}

// inputText 把 embeddings 的 input 字段摊平成一段文本。
//
// 支持三种形态：字符串、字符串数组（最常见的批量向量化），
// 以及 token id 数组（[[1,2],[3,4]]，预分词输入）——后者
// 摊不成文本，按 0 计（估算本来就允许偏差，而且这种请求
// 极少见，为它引入 token 计数不值得）。
func inputText(v any) string {
	switch in := v.(type) {
	case string:
		return in
	case []any:
		var sb strings.Builder
		for _, item := range in {
			if s, ok := item.(string); ok {
				sb.WriteString(s)
				sb.WriteByte('\n')
			}
		}
		return sb.String()
	}
	return ""
}

// CompletionTextFromResponse 从上游响应里抽出回答文本。
//
// 兼容两种形态：非流式 JSON（choices[].message.content）和流式 SSE
// （data: 行里 choices[].delta.content 拼接）。网关对外的响应
// 永远是 OpenAI 形态（协议枢纽的输出），所以两种就够。
// anthropic/gemini 渠道的响应在 relay 里已经被转成 OpenAI 格式，
// 这里拿到的就是转换后的客户端视角字节。
func CompletionTextFromResponse(resp []byte) string {
	if len(resp) == 0 {
		return ""
	}
	if looksLikeSSE(resp) {
		return completionFromSSE(resp)
	}
	return completionFromJSON(resp)
}

// completionFromJSON 解析非流式响应里的回答文本。
func completionFromJSON(resp []byte) string {
	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(resp, &out); err != nil {
		return ""
	}
	var sb strings.Builder
	for i := range out.Choices {
		sb.WriteString(out.Choices[i].Message.Content)
	}
	return sb.String()
}

// completionFromSSE 拼接流式响应里所有 delta.content。
// 逐行解析而非整段 json.Unmarshal：SSE 是多条独立 JSON 事件，
// 整体解析本来就不是合法 JSON。
func completionFromSSE(resp []byte) string {
	var sb strings.Builder
	for _, line := range strings.Split(string(resp), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "" || data == "[DONE]" {
			continue
		}
		var chunk struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue
		}
		for i := range chunk.Choices {
			sb.WriteString(chunk.Choices[i].Delta.Content)
		}
	}
	return sb.String()
}

// looksLikeSSE 粗判一段响应是不是 SSE：出现 "data:" 行即认为是。
// 只是用于选择解析分支，误判的后果也只是估算不准，不会出错。
func looksLikeSSE(resp []byte) bool {
	return bytes.Index(resp, []byte("data:")) >= 0
}

// contentText 兼容 content 的两种形态：纯字符串，或
// [{type:"text", text:"..."}] 的多模态数组。
func contentText(v any) string {
	switch c := v.(type) {
	case string:
		return c
	case []any:
		var sb strings.Builder
		for _, item := range c {
			m, ok := item.(map[string]any)
			if !ok {
				continue
			}
			if t, ok := m["text"].(string); ok {
				sb.WriteString(t)
			}
		}
		return sb.String()
	}
	return ""
}
