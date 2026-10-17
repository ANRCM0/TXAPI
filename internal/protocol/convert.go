package protocol

import (
	"encoding/json"
	"fmt"
	"minigateway/internal/protocol/adaptor"
	"minigateway/internal/protocol/adaptor/openai"
	"minigateway/internal/protocol/canonical"
	"minigateway/internal/protocol/meta"
	"minigateway/internal/protocol/model"
	"minigateway/internal/protocol/responses"
)

func ConvertRequestBody(a adaptor.Adaptor, body []byte, m *meta.Meta) ([]byte, error) {
	if m == nil {
		return nil, fmt.Errorf("missing operation metadata")
	}
	operation, known := OperationForMode(m.Mode)
	requirements := RequirementsFor(operation, body)
	requirements.Stream = requirements.Stream || m.IsStream
	if !known || !Supports(a, requirements) {
		return nil, fmt.Errorf("unsupported operation or capabilities")
	}
	if operation == Responses {
		request, err := responses.ParseRequest(body)
		if err != nil {
			return nil, err
		}
		if err := request.ValidateHTTP(); err != nil {
			return nil, err
		}
		return request.WithModel(m.ActualModel)
	}
	if operation == Embeddings {
		// Only an explicitly capable adaptor may handle native embeddings.
		// Never parse its input or output through the Chat canonical model.
		return a.ConvertRequest(body, m)
	}
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
	cReq, err := (&openai.Adaptor{}).ParseRequest(body)
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
func ConvertResponseBody(a adaptor.Adaptor, body []byte, m *meta.Meta) ([]byte, *model.Usage, error) {
	if m != nil && m.Mode == meta.ModeResponses {
		return responses.NewDecoderWithID(m.ResponseID).Decode(body)
	}
	if m != nil && m.Mode == meta.ModeEmbeddings {
		return decodeEmbeddings(body)
	}
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
	out, err := (&openai.Adaptor{}).BuildResponse(cResp)
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
func ConvertStreamLine(a adaptor.Adaptor, line []byte, session adaptor.StreamSession, m *meta.Meta) ([][]byte, error) {
	if m != nil && m.Mode == meta.ModeResponses {
		return nil, fmt.Errorf("native Responses streams require the operation stream decoder")
	}
	upstreamPA, ok := a.(adaptor.ProtocolAdaptor)
	if !ok || upstreamPA.Name() == "openai" {
		// openai 上游：原样透传（P1 行为）
		return [][]byte{line}, nil
	}

	var chunks []*canonical.StreamChunk
	var err error
	if session != nil {
		chunks, err = session.ParseStreamLine(line)
	} else {
		chunks, err = upstreamPA.ParseStreamLine(line)
	}
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
		encoded, err := (&openai.Adaptor{}).BuildStreamLine(ch)
		if err != nil {
			return nil, err
		}
		if len(encoded) > 0 {
			out = append(out, encoded)
		}
	}
	return out, nil
}
