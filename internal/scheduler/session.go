package scheduler

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// SessionKey 派生本次请求的会话标识，供凭据亲和用。
//
// 取值优先级：
//  1. 请求头 X-Session-Id —— 客户端（Cherry Studio、NextChat 这类）显式（服务端只取摘要，并加可信用户范围）
//     声明，最可靠；
//  2. 可信用户 ID + 令牌 ID + 首条 user 消息内容的哈希 —— 客户端不传时的兜底。
//     为什么用「首条 user 消息」：多轮对话的第一条消息就是整个对话的
//     主题，同一对话的每一轮请求都带着它，所以它是个稳定的会话指纹。
//     带上用户和令牌 ID 是为了让不同用户/令牌问同一个问题时落到不同凭据，
//     否则它们会互相挤占同一把 Key 的缓存。
//
// 返回空字符串表示算不出会话标识（没有 user 消息）——调用方据此降级到
// 纯加权随机，而不是硬造一个每次都变的 ID（那会让亲和彻底失效，
// 还白付一次哈希）。
//
// SessionKey hashes a codec-provided affinity projection in the trusted owner scope.
func SessionKey(header string, userID, tokenID uint, first string) string {
	if header != "" {
		sum := sha256.Sum256([]byte(strings.TrimSpace(header)))
		return "h:" + strconvToken(userID) + ":" + hex.EncodeToString(sum[:])
	}
	if first == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(strconvToken(userID) + "\x00" + strconvToken(tokenID) + "\x00" + first))
	return "t:" + hex.EncodeToString(sum[:])
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
