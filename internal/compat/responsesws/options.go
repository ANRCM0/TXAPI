package responsesws

import (
	"bytes"
	"encoding/json"
	"errors"
)

func oneOf(raw json.RawMessage, values ...string) bool {
	value, valid := text(raw)
	if !valid {
		return false
	}
	for _, allowed := range values {
		if value == allowed {
			return true
		}
	}
	return false
}

// These are provider request fields. None supplies identity, authorization or
// routing facts, and no schema or opaque reasoning content is executed here.
func validateNativeOptions(fields map[string]json.RawMessage, stored bool) error {
	for _, name := range []string{"stream", "generate"} {
		if raw, present := fields[name]; present {
			var value bool
			if json.Unmarshal(raw, &value) != nil || name == "stream" && !value || name == "generate" && !value && stored {
				return errors.New("invalid native WS stream or prewarm option")
			}
		}
	}
	if raw, present := fields["include"]; present {
		var include []string
		if json.Unmarshal(raw, &include) != nil || include == nil || len(include) > 1 || len(include) == 1 && include[0] != "reasoning.encrypted_content" {
			return errors.New("unsupported native WS include")
		}
	}
	if raw, present := fields["reasoning"]; present {
		options, err := object(raw, "effort summary")
		if err != nil {
			return err
		}
		if effort, present := options["effort"]; present && !oneOf(effort, "none", "minimal", "low", "medium", "high", "xhigh") {
			return errors.New("unsupported native WS reasoning effort")
		}
		if summary, present := options["summary"]; present && !oneOf(summary, "auto", "concise", "detailed") {
			return errors.New("unsupported native WS reasoning summary")
		}
	}
	if raw, present := fields["text"]; present {
		options, err := object(raw, "verbosity format")
		if err != nil {
			return err
		}
		if verbosity, present := options["verbosity"]; present && !oneOf(verbosity, "low", "medium", "high") {
			return errors.New("unsupported native WS verbosity")
		}
		if format, present := options["format"]; present {
			value, err := object(format, "type")
			if err != nil || !oneOf(value["type"], "text") {
				return errors.New("unsupported native WS text format")
			}
		}
	}
	if raw, present := fields["prompt_cache_key"]; present {
		key, valid := text(raw)
		if !valid || key == "" || len(key) > 512 {
			return errors.New("invalid native WS prompt cache key")
		}
	}
	if raw, present := fields["client_metadata"]; present {
		var metadata map[string]string
		if len(raw) > 16<<10 || json.Unmarshal(raw, &metadata) != nil || metadata == nil || len(metadata) > 16 {
			return errors.New("invalid native WS client metadata")
		}
		for key, value := range metadata {
			if len(key) == 0 || len(key) > 128 || len(value) > 2048 {
				return errors.New("native WS client metadata exceeds limits")
			}
		}
	}
	return nil
}

func reasoningPart(raw json.RawMessage, summary, closed bool) error {
	var fields map[string]json.RawMessage
	var err error
	if closed {
		fields, err = object(raw, "type text")
	} else {
		err = json.Unmarshal(raw, &fields)
	}
	if err != nil || fields == nil {
		return errors.New("invalid native WS reasoning part")
	}
	kind, _ := text(fields["type"])
	value, valid := text(fields["text"])
	if !valid || len(value) > 64<<10 || summary && kind != "summary_text" || !summary && kind != "reasoning_text" && kind != "text" {
		return errors.New("unsupported native WS reasoning part")
	}
	return nil
}

func validateReasoningItem(raw json.RawMessage, input, closed bool) error {
	var fields map[string]json.RawMessage
	var err error
	if closed {
		fields, err = object(raw, "type id status summary content encrypted_content")
	} else {
		err = json.Unmarshal(raw, &fields)
	}
	if err != nil || fields == nil || !oneOf(fields["type"], "reasoning") {
		return errors.New("invalid native WS reasoning item")
	}
	opaque, valid := text(fields["encrypted_content"])
	if input && (!valid || opaque == "") || valid && len(opaque) > 128<<10 {
		return errors.New("native WS reasoning history requires bounded encrypted content")
	}
	if value, present := fields["encrypted_content"]; present && !bytes.Equal(value, []byte("null")) && !valid {
		return errors.New("invalid native WS encrypted content")
	}
	if id, present := fields["id"]; present {
		value, valid := text(id)
		if !valid || value == "" || len(value) > 256 {
			return errors.New("invalid native WS reasoning ID")
		}
	}
	if status, present := fields["status"]; present && !bytes.Equal(status, []byte("null")) && !oneOf(status, "in_progress", "completed", "incomplete") {
		return errors.New("invalid native WS reasoning status")
	}
	for _, key := range []string{"summary", "content"} {
		value, present := fields[key]
		if !present || !input && bytes.Equal(value, []byte("null")) {
			continue
		}
		var parts []json.RawMessage
		if json.Unmarshal(value, &parts) != nil || parts == nil || len(parts) > 64 {
			return errors.New("native WS reasoning parts exceed limits")
		}
		for _, part := range parts {
			if err := reasoningPart(part, key == "summary", closed); err != nil {
				return err
			}
		}
	}
	return nil
}

func reasoningPrompt(raw json.RawMessage) string {
	var items []json.RawMessage
	_ = json.Unmarshal(raw, &items)
	var out bytes.Buffer
	for _, item := range items {
		var value struct{ Type string }
		if json.Unmarshal(item, &value) == nil && value.Type == "reasoning" {
			out.Write(item)
		}
	}
	return out.String() // An estimate projection, never a provider token proof.
}
