package canonical

// 这个文件放「规范格式 ↔ OpenAI 消息形状」的公共转换。
// 之所以放 canonical 包而不是 openai 包：anthropic/gemini 适配器
// 在把自家 content 块数组转成规范格式后，出站给 OpenAI 客户端时
// 也要用同一套还原逻辑。放公共层，避免每个适配器抄一遍。

// ToOpenAIMessage 把规范消息还原成 OpenAI 出站消息的形状。
//
// 规则：
//   - 只有 PartText 且无工具调用 → content 写字符串（保持请求形状与 OpenAI 一致）
//   - 含图片 → content 写成分片数组
//   - role=assistant 带 tool_use 块 → 拆出 tool_calls，content 置空
//   - role=tool 带 tool_result → 写成 tool 角色消息（tool_call_id + content）
func ToOpenAIMessage(msg Message) map[string]any {
	out := map[string]any{"role": msg.Role}

	var texts []string
	var images []string
	var toolCalls []map[string]any
	var toolResult *ContentPart

	for _, p := range msg.Content {
		switch p.Type {
		case PartText:
			texts = append(texts, p.Text)
		case PartImage:
			images = append(images, p.ImageURL)
		case PartToolUse:
			toolCalls = append(toolCalls, map[string]any{
				"id":   p.ToolUseID,
				"type": "function",
				"function": map[string]any{
					"name":      p.Name,
					"arguments": string(p.Input),
				},
			})
		case PartToolResult:
			toolResult = &p
		}
	}

	switch msg.Role {
	case RoleTool:
		// OpenAI 的 tool 消息：content 是纯文本结果
		text := ""
		if toolResult != nil {
			text = toolResult.Text
		}
		out["tool_call_id"] = msg.ToolUseID
		if msg.Name != "" {
			out["name"] = msg.Name
		}
		out["content"] = text
		return out

	case RoleAssistant:
		if len(toolCalls) > 0 {
			// 带工具调用的 assistant 消息 content 为 null（OpenAI 行为）
			out["tool_calls"] = toolCalls
			if len(texts) > 0 {
				out["content"] = joinTexts(texts)
			} else {
				out["content"] = nil
			}
			return out
		}
	}

	// 普通消息：纯文本写字符串，含图片写数组
	if len(images) > 0 {
		var parts []map[string]any
		for _, t := range texts {
			parts = append(parts, map[string]any{"type": "text", "text": t})
		}
		for _, u := range images {
			parts = append(parts, map[string]any{"type": "image_url", "image_url": map[string]string{"url": u}})
		}
		out["content"] = parts
	} else {
		out["content"] = joinTexts(texts)
	}
	return out
}

// joinTexts 用空串拼接多段文本。
// 用空串而不是换行：多段 content 分片本就是同一段输出的切片，
// 插换行会改变原文（比如代码块中间）。
func joinTexts(texts []string) string {
	out := ""
	for _, t := range texts {
		out += t
	}
	return out
}
