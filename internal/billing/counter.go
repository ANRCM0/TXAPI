package billing

import (
	"bytes"
	"encoding/json"
	"strings"

	relaymodel "minigateway/internal/relay/model"
)

// 估算用的「字符 → token」折算比例（经验值）：
//   - 英文约 4 字符/token
//   - 中文约 1 字符/token（一个汉字通常 1~2 个 token）
//
// 为什么不引入真正的 tokenizer（tiktoken 之类）：它是几百 KB 的
// 词表文件或 C 绑定，自用网关不值得为估算精度付这个代价。
// 估算值一律标记 is_estimated，事后可以核对、也可以换真 tokenizer。
const (
	tokensPerASCIIChar = 0.25
	tokensPerCJKChar   = 1.5
)

// EstimateTokens 按字符数近似一段文本的 token 数，向上取整。
//
// 向上取整的方向性选择：低估会让少扣费（对额度控制不利），
// 高估一点只是保守——预扣本来就是保守方向。
func EstimateTokens(text string) int {
	if text == "" {
		return 0
	}
	ascii, cjk := 0, 0
	for _, r := range text {
		if isCJK(r) {
			cjk++
		} else {
			ascii++
		}
	}
	est := float64(ascii)*tokensPerASCIIChar + float64(cjk)*tokensPerCJKChar
	return int(est + 0.999)
}

// EstimateUsage 在「上游没返回 usage」时按字符数近似一次请求的用量。
// 调用方应把结果标记为 is_estimated。
func EstimateUsage(promptText, completionText string) relaymodel.Usage {
	p := EstimateTokens(promptText)
	c := EstimateTokens(completionText)
	return relaymodel.Usage{
		PromptTokens:     p,
		CompletionTokens: c,
		TotalTokens:      p + c,
	}
}

// PromptTextFromBody 从 OpenAI 格式的请求体里抽出全部对话文本。
//
// 为什么不直接按整个 body 长度估：JSON 的语法符号（引号、字段名、
// 转义符）会被当成几百个假 token，让估算严重偏高。只数 messages
// 里的文本内容才接近真实 prompt 规模。
//
// 解析失败时返回空串而不是报错：估算是降级路径，宁可得 0 也不能
// 让记账逻辑把请求搞挂。
func PromptTextFromBody(body []byte) string {
	var req struct {
		Messages []struct {
			Content any `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		return ""
	}
	var sb strings.Builder
	for i := range req.Messages {
		sb.WriteString(contentText(req.Messages[i].Content))
		sb.WriteByte('\n')
	}
	return sb.String()
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

// isCJK 判断字符是否属于 CJK 系（汉字/日文假名/韩文音节/中文全角标点）。
// 覆盖主流区间即可——目的是折算比例，不是做 Unicode 字谱。
func isCJK(r rune) bool {
	switch {
	case r >= 0x4E00 && r <= 0x9FFF, // CJK 统一表意文字
		r >= 0x3400 && r <= 0x4DBF, // 扩展 A
		r >= 0x3040 && r <= 0x30FF, // 平假名/片假名
		r >= 0xAC00 && r <= 0xD7AF, // 韩文音节
		r >= 0xFF00 && r <= 0xFFEF: // 全角字符（含全角标点）
		return true
	}
	// 中文全角标点（，。、；：？！「」【】…等）：token 成本接近
	// 汉字而不是半角字符，归到 CJK 比例里。这些字符在
	// unicode.IsPunct 里和英文标点混在一起，必须单独归类。
	return isWidePunct(r)
}

// isWidePunct 识别中文全角标点（，。、；：？！「」『』【】…等）。
// 这些字符在 unicode.IsPunct 里和英文标点混在一起，但 token 成本
// 接近汉字而不是接近半角字符，单独归类。
func isWidePunct(r rune) bool {
	switch r {
	case '，', '。', '、', '；', '：', '？', '！', '（', '）', '【', '】',
		'《', '》', '「', '」', '『', '』', '—', '…', '·', '～':
		return true
	}
	return false
}
