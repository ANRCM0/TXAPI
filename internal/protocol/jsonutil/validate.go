// Package jsonutil validates shared trust-boundary JSON constraints.
package jsonutil

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"unicode/utf8"
)

func Validate(body []byte) error {
	if !utf8.Valid(body) {
		return errors.New("JSON must be valid UTF-8")
	}
	d := json.NewDecoder(bytes.NewReader(body))
	d.UseNumber()
	if err := value(d, 0); err != nil {
		return err
	}
	if _, err := d.Token(); err != io.EOF {
		return errors.New("strict requests require exactly one JSON value")
	}
	return nil
}

func value(d *json.Decoder, depth int) error {
	if depth > 32 {
		return errors.New("strict JSON nesting exceeds the supported limit")
	}
	token, err := d.Token()
	if err != nil {
		return errors.New("invalid strict request JSON")
	}
	delim, compound := token.(json.Delim)
	if !compound {
		return nil
	}
	switch delim {
	case '{':
		seen := make(map[string]bool)
		for d.More() {
			key, err := d.Token()
			if err != nil {
				return errors.New("invalid strict object")
			}
			name, ok := key.(string)
			if !ok || seen[name] {
				return errors.New("strict JSON object contains a repeated field")
			}
			seen[name] = true
			if err := value(d, depth+1); err != nil {
				return err
			}
		}
	case '[':
		for d.More() {
			if err := value(d, depth+1); err != nil {
				return err
			}
		}
	default:
		return errors.New("invalid strict JSON delimiter")
	}
	if _, err := d.Token(); err != nil {
		return errors.New("invalid strict JSON container")
	}
	return nil
}
