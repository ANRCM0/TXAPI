package protocol

import "encoding/json"

func (r Request) AffinityText() string {
	var envelope struct {
		Messages []affinityMessage `json:"messages"`
	}
	if json.Unmarshal(r.body, &envelope) != nil {
		return ""
	}
	return firstUserContent(envelope.Messages)
}

// Message 是请求体 messages 数组里本包唯一关心的投影。
//
// 只取 role 和 content，且 content 按 any 收：多模态请求的 content 是
// 数组而不是字符串，硬编码 string 会让带图片的请求反序列化直接失败——
// 那种请求本来是该正常转发的，不该被网关自己的会话亲和逻辑拦掉。
type affinityMessage struct {
	Role    string `json:"role"`
	Content any    `json:"content"`
}

// firstUserContent 取首条 user 消息的文本内容。
//
// 只取第一条：多轮对话里后续消息会不断追加，拿它们算指纹会让同一对话
// 的每一轮算出不同的 ID，亲和直接失效。第一条消息足够区分不同对话。
func firstUserContent(messages []affinityMessage) string {
	for _, m := range messages {
		if m.Role != "user" {
			continue
		}
		switch v := m.Content.(type) {
		case string:
			if v != "" {
				return v
			}
		case []any:
			// 多模态：拼出所有 text 片段。图片片段没有文本可拼，
			// 跳过即可——拼出来的字符串仍然是这个对话的稳定指纹。
			out := ""
			for _, part := range v {
				if p, ok := part.(map[string]any); ok {
					if t, ok := p["text"].(string); ok {
						out += t
					}
				}
			}
			if out != "" {
				return out
			}
		}
	}
	return ""
}
