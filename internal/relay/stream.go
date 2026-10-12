package relay

import (
	"bufio"
	"encoding/json"
	"io"
	"strings"

	"minigateway/internal/relay/model"
)

// 这个文件放流式响应的公共工具。

// newStreamScanner 构造一个读 SSE 的扫描器。
//
// 缓冲区开到 1MB：单行 SSE 可能很长（工具调用的参数碎片、大段文本），
// 默认 64KB 会被截断，截断后 json.Unmarshal 直接失败。
func newStreamScanner(body io.Reader) *bufio.Scanner {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	return scanner
}

// usageFromSSELine 从一行 SSE data 里抽 usage。
// 只在 OpenAI 形态（最后一条带 usage 的 chunk）下有效；其他协议返回 nil。
func usageFromSSELine(line []byte) *model.Usage {
	s := strings.TrimSpace(string(line))
	if !strings.HasPrefix(s, "data:") {
		return nil
	}
	data := strings.TrimSpace(strings.TrimPrefix(s, "data:"))
	if data == "" || data == "[DONE]" {
		return nil
	}
	var parsed struct {
		Usage *model.Usage `json:"usage"`
	}
	if json.Unmarshal([]byte(data), &parsed) == nil {
		return parsed.Usage
	}
	return nil
}
