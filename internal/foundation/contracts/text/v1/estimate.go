package text

const Version = "1"
const (
	tokensPerASCIIChar = 0.25
	tokensPerCJKChar   = 1.5
)

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

func isCJK(r rune) bool {
	switch {
	case r >= 0x4E00 && r <= 0x9FFF,
		r >= 0x3400 && r <= 0x4DBF,
		r >= 0x3040 && r <= 0x30FF,
		r >= 0xAC00 && r <= 0xD7AF,
		r >= 0xFF00 && r <= 0xFFEF:
		return true
	}
	return isWidePunct(r)
}

func isWidePunct(r rune) bool {
	switch r {
	case '\uff0c', '\u3002', '\u3001', '\uff1b', '\uff1a', '\uff1f', '\uff01', '\uff08', '\uff09', '\u3010', '\u3011',
		'\u300a', '\u300b', '\u300c', '\u300d', '\u300e', '\u300f', '\u2014', '\u2026', '\xb7', '\uff5e':
		return true
	}
	return false
}
