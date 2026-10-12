package adaptor

import (
	"bytes"
	"io"
)

// bytesReader 包一层是为了让 DoRequest 的签名保持干净，
// 调用方不需要 import bytes。
func bytesReader(b []byte) io.Reader { return bytes.NewReader(b) }
