package billing

import (
	"encoding/json"
	"errors"
	"net"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"
)

const OperatorDeclaredTokensV1 = "operator_declared_tokens_v1"
const OperatorResponsesV1 = "operator_responses_v1"
const OperatorChatV1 = "operator_chat_v1"
const OperatorEmbeddingsV1 = "operator_embeddings_v1"

// This policy is an operator assertion about the configured relay, not an
// attestation of a remote tokenizer or invoice. It reserves the entire input
// envelope plus output and non-token charges, including bounded control RPCs.
func validateOperatorPolicy(c ProviderCostContract) error {
	u, err := url.Parse(c.BaseURL)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.RawPath != "" || u.Opaque != "" {
		return errors.New("operator token policy requires an exact HTTP base URL")
	}
	loopback := net.ParseIP(u.Hostname())
	if u.Scheme != "https" && !(u.Scheme == "http" && loopback != nil && loopback.IsLoopback()) {
		return errors.New("operator token policy requires HTTPS; only loopback fixtures may use HTTP")
	}
	if c.Protocol != "openai" || c.TariffProfile != OperatorDeclaredTokensV1 {
		return errors.New("operator token policy requires a supported OpenAI-compatible operation")
	}
	switch c.Operation {
	case "responses":
		if c.InputBoundProfile != OperatorResponsesV1 || c.OutputLimitField != "max_output_tokens" || c.MaxOutputTokens <= 0 {
			return ErrProviderContract
		}
	case "chat":
		if c.InputBoundProfile != OperatorChatV1 || c.OutputLimitField != "max_tokens" && c.OutputLimitField != "max_completion_tokens" || c.MaxOutputTokens <= 0 {
			return ErrProviderContract
		}
	case "embeddings":
		if c.InputBoundProfile != OperatorEmbeddingsV1 || c.OutputLimitField != "" || c.MaxOutputTokens != 0 {
			return ErrProviderContract
		}
	default:
		return ErrProviderContract
	}
	if len(c.PolicyVersion) == 0 || len(c.PolicyVersion) > 128 || strings.TrimSpace(c.PolicyVersion) != c.PolicyVersion || !utf8.ValidString(c.PolicyVersion) || strings.ContainsAny(c.PolicyVersion, "\r\n\t") || c.PolicyExpiresAt <= time.Now().Unix() {
		return errors.New("operator token policy requires a version and future expiry")
	}
	for _, b := range []byte(c.PolicyVersion) {
		if b < '!' || b > '~' {
			return errors.New("operator token policy version requires visible ASCII")
		}
	}
	if c.MaxRequestBytes < 1 || c.MaxRequestBytes > 1<<20 || int64(c.MaxRequestBytes) > c.InputTokenCeiling {
		return errors.New("operator token policy requires a 1..1048576 byte request bound within its declared whole-input token envelope")
	}
	return nil
}

func checkOperatorRequest(c ProviderCostContract, preparedURL string, body []byte) error {
	if c.PolicyExpiresAt <= time.Now().Unix() {
		return ErrTariffUnproven
	}
	endpoint := "/v1/responses"
	if c.Operation == "chat" {
		endpoint = "/v1/chat/completions"
	}
	if c.Operation == "embeddings" {
		endpoint = "/v1/embeddings"
	}
	want := strings.TrimRight(c.BaseURL, "/") + endpoint
	ws := strings.Replace(want, "https://", "wss://", 1)
	if strings.HasPrefix(want, "http://") {
		ws = strings.Replace(want, "http://", "ws://", 1)
	}
	if preparedURL != want && (c.Operation != "responses" || preparedURL != ws) || len(body) > c.MaxRequestBytes {
		return ErrProviderLimit
	}
	if c.Operation == "responses" && preparedURL == want {
		fields, valid := wireObject(body)
		_, hasType := fields["type"]
		_, hasStream := fields["stream_id"]
		if !valid || hasType || hasStream {
			return ErrProviderLimit
		}
	}
	var request StrictRequest
	var err error
	if c.Operation == "responses" {
		request, err = ParseResponsesStrictRequest(body)
	} else {
		request, err = ParseStrictRequest(body, c.Operation == "embeddings")
	}
	if err != nil || int64(request.OutputLimit) > c.MaxOutputTokens {
		return ErrProviderLimit
	}
	var wire struct {
		Model string `json:"model"`
	}
	if json.Unmarshal(body, &wire) != nil || wire.Model != c.ActualModel {
		return ErrProviderLimit
	}
	if c.Operation == "embeddings" {
		var in struct {
			Input json.RawMessage `json:"input"`
		}
		if json.Unmarshal(body, &in) != nil {
			return ErrProviderLimit
		}
		if _, single := strictString(in.Input); !single {
			var batch []string
			if json.Unmarshal(in.Input, &batch) != nil || len(batch) > 2048 {
				return ErrProviderLimit
			}
		}
	}
	return nil
}

// ParseResponsesStrictRequest is the shared source/wire permission boundary
// for static-key HTTP, background creation and individual WebSocket rounds.
// It does not expand parent IDs or execute function tools.
func ParseResponsesStrictRequest(body []byte) (StrictRequest, error) {
	invalid := func() (StrictRequest, error) { return StrictRequest{}, ErrUnpricedDimension }
	if len(body) == 0 || len(body) > 1<<20 || !utf8.Valid(body) || uniqueJSON(body) != nil {
		return invalid()
	}
	f, ok := wireObject(body)
	if !ok || !wireKeys(f, "type", "stream_id", "model", "input", "instructions", "tools", "tool_choice", "parallel_tool_calls", "stream", "store", "background", "max_output_tokens", "temperature", "top_p", "service_tier", "truncation") {
		return invalid()
	}
	if name, ok := strictString(f["model"]); !ok || name == "" || len(name) > 256 {
		return invalid()
	}
	if raw, exists := f["stream_id"]; exists {
		kind, valid := strictString(f["type"])
		stream, validStream := strictString(raw)
		if !valid || kind != "response.create" || !validStream || stream != "main" {
			return invalid()
		}
	}
	var limit int
	if json.Unmarshal(f["max_output_tokens"], &limit) != nil || limit < 1 || limit > 2_147_483_647 {
		return invalid()
	}
	for _, name := range []string{"stream", "store", "background", "parallel_tool_calls"} {
		if raw, exists := f[name]; exists && !strictBool(raw) {
			return invalid()
		}
	}
	for name, expected := range map[string]string{"type": "response.create", "service_tier": "default", "truncation": "disabled"} {
		if raw, exists := f[name]; exists {
			if s, ok := strictString(raw); !ok || s != expected {
				return invalid()
			}
		}
	}
	if raw, exists := f["instructions"]; exists {
		if s, ok := strictString(raw); !ok || len(s) > 64<<10 {
			return invalid()
		}
	}
	for name, maximum := range map[string]float64{"temperature": 2, "top_p": 1} {
		if raw, exists := f[name]; exists {
			var number *float64
			if json.Unmarshal(raw, &number) != nil || number == nil || *number < 0 || *number > maximum {
				return invalid()
			}
		}
	}
	if !responsesInput(f["input"]) || !responsesTools(f) {
		return invalid()
	}
	return StrictRequest{OutputLimit: limit, OutputLimitField: "max_output_tokens"}, nil
}

func responsesIdentifier(raw json.RawMessage, limit int) (string, bool) {
	s, ok := strictString(raw)
	if !ok || len(s) == 0 || len(s) > limit {
		return "", false
	}
	for _, b := range []byte(s) {
		if b < '!' || b > '~' {
			return "", false
		}
	}
	return s, true
}

func responsesFunctionName(raw json.RawMessage) (string, bool) {
	s, ok := responsesIdentifier(raw, 64)
	if !ok {
		return "", false
	}
	for _, b := range []byte(s) {
		if !(b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9' || b == '_' || b == '-') {
			return "", false
		}
	}
	return s, true
}

func responsesObject(raw json.RawMessage) bool {
	_, ok := wireObject(raw)
	return ok && len(raw) <= 64<<10 && uniqueJSON(raw) == nil
}

func responsesInput(raw json.RawMessage) bool {
	if s, ok := strictString(raw); ok {
		return len(s) <= 64<<10
	}
	items, ok := wireArray(raw)
	if !ok || len(items) > 256 {
		return false
	}
	calls := map[string]bool{}
	for _, raw := range items {
		item, ok := wireObject(raw)
		if !ok {
			return false
		}
		kind, hasKind := strictString(item["type"])
		if _, exists := item["type"]; exists && !hasKind {
			return false
		}
		if id, exists := item["id"]; exists {
			if _, valid := responsesIdentifier(id, 256); !valid {
				return false
			}
		}
		if status, exists := item["status"]; exists {
			if s, valid := strictString(status); !valid || s != "completed" {
				return false
			}
		}
		switch kind {
		case "function_call":
			if !wireKeys(item, "type", "id", "status", "call_id", "name", "arguments") || len(calls) >= 128 {
				return false
			}
			id, valid := responsesIdentifier(item["call_id"], 128)
			if _, exists := calls[id]; !valid || exists {
				return false
			}
			if _, valid := responsesFunctionName(item["name"]); !valid {
				return false
			}
			args, valid := strictString(item["arguments"])
			if !valid || !responsesObject([]byte(args)) {
				return false
			}
			calls[id] = false
		case "function_call_output":
			if !wireKeys(item, "type", "id", "status", "call_id", "output") {
				return false
			}
			id, valid := responsesIdentifier(item["call_id"], 128)
			resolved, exists := calls[id]
			output, validOutput := strictString(item["output"])
			if !valid || !exists || resolved || !validOutput || len(output) > 64<<10 {
				return false
			}
			calls[id] = true
		case "", "message":
			if !wireKeys(item, "type", "id", "status", "role", "content") {
				return false
			}
			role, valid := strictString(item["role"])
			if !valid || role != "user" && role != "assistant" && role != "system" && role != "developer" {
				return false
			}
			if text, valid := strictString(item["content"]); valid {
				if len(text) > 64<<10 {
					return false
				}
				continue
			}
			blocks, valid := wireArray(item["content"])
			if !valid || len(blocks) > 128 {
				return false
			}
			for _, raw := range blocks {
				block, valid := wireObject(raw)
				typeName, _ := strictString(block["type"])
				text, validText := strictString(block["text"])
				if !valid || !wireKeys(block, "type", "text", "annotations") || typeName != "input_text" && typeName != "output_text" || !validText || len(text) > 64<<10 {
					return false
				}
				if raw, exists := block["annotations"]; exists {
					var annotations []json.RawMessage
					if json.Unmarshal(raw, &annotations) != nil || annotations == nil || len(annotations) != 0 {
						return false
					}
				}
			}
		default:
			return false
		}
	}
	for _, resolved := range calls {
		if !resolved {
			return false
		}
	}
	return true
}

func responsesTools(fields map[string]json.RawMessage) bool {
	names := map[string]bool{}
	if raw, exists := fields["tools"]; exists {
		var tools []map[string]json.RawMessage
		if json.Unmarshal(raw, &tools) != nil || tools == nil || len(tools) > 64 {
			return false
		}
		for _, tool := range tools {
			if tool == nil || !wireKeys(tool, "type", "name", "description", "parameters", "strict") {
				return false
			}
			typeName, valid := strictString(tool["type"])
			name, validName := responsesFunctionName(tool["name"])
			if !valid || typeName != "function" || !validName || names[name] {
				return false
			}
			names[name] = true
			if raw, exists := tool["description"]; exists {
				if s, valid := strictString(raw); !valid || len(s) > 64<<10 {
					return false
				}
			}
			if raw, exists := tool["parameters"]; exists && !responsesObject(raw) {
				return false
			}
			if raw, exists := tool["strict"]; exists && !strictBool(raw) {
				return false
			}
		}
	}
	if raw, exists := fields["tool_choice"]; exists {
		if s, ok := strictString(raw); ok {
			return s == "auto" || s == "none" || s == "required" && len(names) > 0
		}
		choice, valid := wireObject(raw)
		if !valid || len(choice) != 2 || !wireKeys(choice, "type", "name") {
			return false
		}
		typeName, valid := strictString(choice["type"])
		name, validName := responsesFunctionName(choice["name"])
		return valid && typeName == "function" && validName && names[name]
	}
	return true
}
