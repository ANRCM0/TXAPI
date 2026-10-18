package billing

import (
	relaymodel "minigateway/internal/foundation/contracts/execution/v1"
	textvalue "minigateway/internal/foundation/contracts/text/v1"
)

// EstimateTokens 按字符数近似一段文本的 token 数，向上取整。
//
// 向上取整的方向性选择：低估会让少扣费（对额度控制不利），
// 高估一点只是保守——预扣本来就是保守方向。
func EstimateTokens(text string) int { return textvalue.EstimateTokens(text) }

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

// isCJK 判断字符是否属于 CJK 系（汉字/日文假名/韩文音节/中文全角标点）。
// 覆盖主流区间即可——目的是折算比例，不是做 Unicode 字谱。
