package billing

import (
	relaymodel "minigateway/internal/foundation/contracts/execution/v1"
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
