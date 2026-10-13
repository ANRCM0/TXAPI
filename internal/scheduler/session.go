package scheduler

import (
	"crypto/sha256"
	"encoding/hex"
)

// SessionID 派生本次请求的会话标识，供凭据亲和用。
//
// 取值优先级：
//  1. 请求头 X-Session-Id —— 客户端（Cherry Studio、NextChat 这类）显式
//     声明，最可靠；
//  2. 令牌 ID + 首条 user 消息内容的哈希 —— 客户端不传时的兜底。
//     为什么用「首条 user 消息」：多轮对话的第一条消息就是整个对话的
//     主题，同一对话的每一轮请求都带着它，所以它是个稳定的会话指纹。
//     带上令牌 ID 是为了让两个令牌问同一个问题时落到不同凭据，
//     否则它们会互相挤占同一把 Key 的缓存。
//
// 返回空字符串表示算不出会话标识（没有 user 消息）——调用方据此降级到
// 纯加权随机，而不是硬造一个每次都变的 ID（那会让亲和彻底失效，
// 还白付一次哈希）。
//
// 为什么在 relay 里算而不是在 handler 里算：请求体在 relay 里已经是
// 解析好的 []byte，而首条消息的抽取要看 messages 数组——handler 为了
// 保持「透明代理」只解析了 model/stream/max_tokens 三个字段，再为会话
// 亲和多解析一个 messages 会让那个刻意的边界变模糊。在 relay 里算，
// handler 的「只解析路由必需字段」约定保持不变。
func SessionID(header string, tokenID uint, messages []Message) string {
	if header != "" {
		return "h:" + header
	}
	first := firstUserContent(messages)
	if first == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(strconvToken(tokenID) + "\x00" + first))
	return "t:" + hex.EncodeToString(sum[:8])
}

// Message 是请求体 messages 数组里本包唯一关心的投影。
//
// 只取 role 和 content，且 content 按 any 收：多模态请求的 content 是
// 数组而不是字符串，硬编码 string 会让带图片的请求反序列化直接失败——
// 那种请求本来是该正常转发的，不该被网关自己的会话亲和逻辑拦掉。
type Message struct {
	Role    string `json:"role"`
	Content any    `json:"content"`
}

// firstUserContent 取首条 user 消息的文本内容。
//
// 只取第一条：多轮对话里后续消息会不断追加，拿它们算指纹会让同一对话
// 的每一轮算出不同的 ID，亲和直接失效。第一条消息足够区分不同对话。
func firstUserContent(messages []Message) string {
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

// strconvToken 把令牌 ID 拼进指纹。用十进制字符串而不是 %d 格式化，
// 避免为一行引入 fmt。
func strconvToken(id uint) string {
	if id == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for id > 0 {
		i--
		buf[i] = byte('0' + id%10)
		id /= 10
	}
	return string(buf[i:])
}
