// Package adaptor 定义网关的扩展点：Adaptor 接口。
//
// 这是整个项目最值钱的设计，抄自 one-api/relay/adaptor/interface.go。
// 所有厂商的差异（鉴权方式、请求体格式、响应结构、用量字段位置）
// 都被压进这一个接口。新增一个厂商 = 新建一个包实现这几个方法，
// 不需要改动任何核心代码。
package adaptor

import (
	"io"
	"net/http"

	"minigateway/internal/protocol/meta"
)

// Adaptor 是上游厂商适配器。
type Adaptor interface {
	// GetRequestURL 返回本次请求要打的上游完整地址。
	// 例如 https://api.openai.com/v1/chat/completions
	GetRequestURL(m *meta.Meta) (string, error)

	// SetupRequestHeader 设置上游请求头，主要是鉴权。
	// 不同厂商鉴权方式不同：OpenAI 是 Authorization: Bearer，
	// Anthropic 是 x-api-key，Gemini 是 URL 上的 ?key=。
	SetupRequestHeader(req *http.Request, m *meta.Meta) error

	// ConvertRequest 把请求体转换成上游能懂的格式。
	//
	// 参数 body 是客户端原始 JSON。返回 nil 表示「格式与上游一致，原样透传」。
	// 这是协议转换的核心：OpenAI 格式进、Claude 格式出，就发生在这里。
	ConvertRequest(body []byte, m *meta.Meta) ([]byte, error)

	// GetChannelName 返回渠道展示名，用于日志。
	GetChannelName() string
}

// DoRequest 是所有适配器共用的发请求逻辑。
//
// 为什么放在接口外面做成函数而不是接口方法：
// 发 HTTP 请求这一步，所有厂商的做法完全一样（net/http 打过去、带上 body）。
// 把它抽成公共函数，适配器只需要专注自己那部分差异，避免 40 个适配器
// 抄 40 遍一模一样的代码——这正是 one-api 里可以改进的地方。
func DoRequest(client *http.Client, url string, headers http.Header, body []byte) (*http.Response, error) {
	var reader io.Reader
	if body != nil {
		reader = bytesReader(body)
	}
	req, err := http.NewRequest(http.MethodPost, url, reader)
	if err != nil {
		return nil, err
	}
	// 先铺公共头，再让调用方设置的头覆盖（鉴权头由 SetupRequestHeader 提供）
	req.Header.Set("Content-Type", "application/json")
	for k, vs := range headers {
		for _, v := range vs {
			req.Header.Set(k, v)
		}
	}
	return client.Do(req)
}
