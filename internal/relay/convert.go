package relay

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"minigateway/internal/relay/adaptor"
	"minigateway/internal/relay/adaptor/openai"
	"minigateway/internal/relay/canonical"
	"minigateway/internal/relay/meta"
	"minigateway/internal/relay/model"
	"minigateway/internal/server/middleware"
)

// 这个文件是 P4 的「协议枢纽」，对应 PLAN 3.2 的 N+M 设计。
//
// 整体数据流（客户端说 OpenAI 协议，上游说任意协议）：
//
//	客户端 body ──openaiPA.ParseRequest──▶ 规范 ChatRequest
//	                                        │（模型名映射在这里生效）
//	规范 ChatRequest ──upstreamPA.BuildRequest──▶ 上游 body
//
//	上游 body ──upstreamPA.ParseResponse──▶ 规范 ChatResponse
//	                                        │
//	规范 ChatResponse ──openaiPA.BuildResponse──▶ 客户端 body
//
// 流式同理，逐行翻译。关键点：每收到一行上游 SSE，先转成规范 chunk，
// 再转成客户端协议的行。不能整条流收完再转——那样打字机效果就没了。
//
// 为什么客户端侧固定用 openai 适配器：网关目前只暴露 /v1/chat/completions
// 一个入口（PLAN 里「客户端说 OpenAI 协议」）。将来加 Claude 客户端入口时，
// 只需要在这里多注册一个入站适配器，出站侧完全不用动。

// clientPA 是入站（客户端侧）协议适配器。
// 固定为 OpenAI：网关对客户端只暴露 OpenAI 协议。
var clientPA adaptor.ProtocolAdaptor = &openai.Adaptor{}

// convertRequestBody 把客户端请求体转换成上游能懂的请求体。
//
// 两条路：
//   - 渠道类型是 openai：直接调适配器的 ConvertRequest（只做模型名映射，
//     body 原样透传），保持 P1 的透明代理行为——openai→openai 不做
//     结构化重写，避免未建模字段被我们的结构体吃掉。
//   - 其他渠道类型：走「OpenAI → 规范 → 上游协议」两段转换。
func convertRequestBody(a adaptor.Adaptor, body []byte, m *meta.Meta) ([]byte, error) {
	// 非 openai 渠道才需要走枢纽；openai 渠道走原路，行为与 P1 一致
	if _, ok := a.(interface{ Name() string }); ok {
		if pa, ok := a.(adaptor.ProtocolAdaptor); ok && pa.Name() == "openai" {
			return a.ConvertRequest(body, m)
		}
	}

	upstreamPA, ok := a.(adaptor.ProtocolAdaptor)
	if !ok {
		// 适配器没实现协议层接口：退化成只做模型名映射，不报错。
		// 理由：适配器可能只服务 embeddings 等非对话模式，硬性要求
		// 实现全套协议方法会阻碍扩展。
		return a.ConvertRequest(body, m)
	}

	// 第一段：客户端协议(OpenAI) → 规范
	cReq, err := clientPA.ParseRequest(body)
	if err != nil {
		return nil, fmt.Errorf("解析客户端请求失败: %w", err)
	}

	// 模型名映射在规范层生效：把 OriginModel 换成 ActualModel，
	// 各协议适配器构造请求时直接用 req.Model，不用各自再处理一遍。
	cReq.Model = m.ActualModel

	// 第二段：规范 → 上游协议
	return upstreamPA.BuildRequest(cReq)
}

// convertResponseBody 把上游非流式响应转换成客户端协议的响应体。
// 返回转换后的 body 和抽取出的 usage（供计费）。
func convertResponseBody(a adaptor.Adaptor, body []byte, m *meta.Meta) ([]byte, *model.Usage, error) {
	upstreamPA, ok := a.(adaptor.ProtocolAdaptor)
	if !ok || upstreamPA.Name() == "openai" {
		// openai 渠道：不转换，直接透传（P1 行为）。
		// usage 单独抽一下，免得计费逻辑要为两种路径写两遍。
		var parsed struct {
			Usage *model.Usage `json:"usage"`
		}
		_ = json.Unmarshal(body, &parsed)
		return body, parsed.Usage, nil
	}

	cResp, err := upstreamPA.ParseResponse(body)
	if err != nil {
		return nil, nil, fmt.Errorf("解析上游响应失败: %w", err)
	}
	// 补 id / model：Gemini 响应不带这两个字段（Claude 带），
	// 空 id 会让部分 OpenAI SDK 的缓存/去重逻辑出问题，
	// 空 model 会让客户端的模型名显示异常。
	// 用请求时的模型名（已映射后的 ActualModel）兜底。
	if m != nil {
		if cResp.Model == "" {
			cResp.Model = m.ActualModel
		}
		if cResp.ID == "" {
			cResp.ID = "chatcmpl-gw-" + m.ActualModel
		}
	}
	out, err := clientPA.BuildResponse(cResp)
	if err != nil {
		return nil, nil, fmt.Errorf("构造客户端响应失败: %w", err)
	}
	return out, cResp.Usage, nil
}

// convertStreamLine 把上游的一行 SSE 翻译成客户端协议的多行 SSE。
//
// 返回的每一行都不含结尾换行（调用方负责写 \n）。
// 上游的 [DONE] 之类的协议终止标记在这里被消费掉，由客户端适配器
// 重新生成自己的终止标记（不同协议的 [DONE] 语义一致但位置不同）。
func convertStreamLine(a adaptor.Adaptor, line []byte, sessionID string, m *meta.Meta) ([][]byte, error) {
	upstreamPA, ok := a.(adaptor.ProtocolAdaptor)
	if !ok || upstreamPA.Name() == "openai" {
		// openai 上游：原样透传（P1 行为）
		return [][]byte{line}, nil
	}

	chunks, err := parseStreamLineWithSession(upstreamPA, line, sessionID)
	if err != nil {
		return nil, err
	}
	var out [][]byte
	for _, ch := range chunks {
		if ch == nil {
			continue
		}
		// 补 id / model：Gemini 流式快照不带 id/model（Claude 的
		// message_start 带）。空 id 会让客户端按「无 ID 响应」处理，
		// 个别 SDK 会拒绝。用请求时的模型名兜底。
		if m != nil {
			if ch.Model == "" {
				ch.Model = m.ActualModel
			}
			if ch.ID == "" {
				ch.ID = "chatcmpl-gw-" + m.ActualModel
			}
		}
		encoded, err := clientPA.BuildStreamLine(ch)
		if err != nil {
			return nil, err
		}
		if len(encoded) > 0 {
			out = append(out, encoded)
		}
	}
	return out, nil
}

// parseStreamLineWithSession 调用上游协议适配器解析一行 SSE。
//
// 会话隔离：流式转换是有状态的（Claude 的 translator 要跨行记住
// message id、块类型、用量），所以每个请求会话必须有独立的 translator。
// 通过 SessionStreamer 接口实现：适配器可以选择实现它来获得会话级状态，
// 没实现就退回无状态调用（单测/直接调用场景）。
func parseStreamLineWithSession(pa adaptor.ProtocolAdaptor, line []byte, sessionID string) ([]*canonical.StreamChunk, error) {
	if ss, ok := pa.(adaptor.SessionStreamer); ok {
		return ss.ParseStreamLineSession(line, sessionID)
	}
	return pa.ParseStreamLine(line)
}

// streamDoneMarker 返回客户端协议的流结束标记。
// OpenAI 是 "data: [DONE]"；上游的终止标记由 convertStreamLine 消费。
func streamDoneMarker() []byte {
	return []byte("data: [DONE]")
}

// relayStream 处理协议转换路径下的流式响应。
//
// 与 openai 适配器里的 handleStreamResponse 的区别：
//   - 逐行做协议翻译（上游 Claude/Gemini SSE → OpenAI SSE）
//   - 上游流结束时补发客户端的 [DONE] 标记
//   - 流中途出错不能改状态码（头已经发出），只能记日志后结束
func relayStream(c *gin.Context, a adaptor.Adaptor, resp *http.Response, m *meta.Meta) (*model.Usage, error) {
	defer resp.Body.Close()

	c.Writer.Header().Set("Content-Type", "text/event-stream")
	c.Writer.Header().Set("Cache-Control", "no-cache")
	c.Writer.Header().Set("Connection", "keep-alive")
	c.Writer.Header().Set("X-Accel-Buffering", "no")
	c.Writer.WriteHeader(http.StatusOK)

	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		return nil, fmt.Errorf("当前服务器不支持流式响应")
	}

	// 会话 ID：用请求 ID + 渠道 ID 组合，保证同一请求的流内所有行
	// 共享同一个 translator，不同请求互不干扰。
	sessionID := m.ChannelName + ":" + strconv.Itoa(m.ChannelID)
	if rid, exists := c.Get(middleware.CtxRequestID); exists {
		if s, ok := rid.(string); ok && s != "" {
			sessionID = s + ":" + sessionID
		}
	}
	defer releaseStreamSession(a, sessionID)

	var usage *model.Usage
	scanner := newStreamScanner(resp.Body)

	for scanner.Scan() {
		lines, err := convertStreamLine(a, scanner.Bytes(), sessionID, m)
		if err != nil {
			// 单行转换失败不中断整条流：坏行跳过，继续读后面的行。
			// 理由：流式一旦开始就无法回退，为一行坏数据掐断整个对话
			// 比丢一行数据的伤害大得多。
			log.Printf("[流式转换] 跳过无法解析的上游行: %v", err)
			continue
		}
		for _, ln := range lines {
			// 从翻译后的行里顺手抽 usage（OpenAI 协议最后一条带 usage）
			if u := usageFromSSELine(ln); u != nil {
				usage = u
			}
			if _, err := c.Writer.Write(append(ln, '\n')); err != nil {
				// 客户端断开是常见情况，不算错误
				return usage, nil
			}
			flusher.Flush()
		}
	}

	// 上游流正常结束后，补发客户端协议的终止标记。
	// 没有它，OpenAI SDK 会一直等不到流结束事件。
	if _, err := c.Writer.Write(append(streamDoneMarker(), '\n')); err == nil {
		flusher.Flush()
	}
	return usage, scanner.Err()
}

// releaseStreamSession 流结束后释放会话状态。
// 通过 SessionReleaser 可选接口实现：适配器自己管理状态生命周期。
func releaseStreamSession(a adaptor.Adaptor, sessionID string) {
	if sr, ok := a.(adaptor.SessionReleaser); ok {
		sr.ReleaseStreamSession(sessionID)
	}
}
