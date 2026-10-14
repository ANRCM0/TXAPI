// Package meta keeps legacy type names during S1 migration.
package meta
import impl "minigateway/internal/protocol/meta"
type Meta = impl.Meta
const (ModeChatCompletions = impl.ModeChatCompletions; ModeEmbeddings = impl.ModeEmbeddings)
