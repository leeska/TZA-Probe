package componentproto

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"unicode/utf8"

	"github.com/cyberphone/json-canonicalization/go/src/webpki.org/jsoncanonicalizer"
)

const (
	MaxJSONDepth = 16
	MaxSafeInt   = 9007199254740991
)

type ValidationError struct {
	Message string
}

func (err *ValidationError) Error() string {
	return "invalid component JSON: " + err.Message
}

func schemaErr(message string) error {
	return &ValidationError{Message: message}
}

func ValidationMessage(err error) (string, bool) {
	var validation *ValidationError
	if errors.As(err, &validation) {
		return validation.Message, true
	}
	return "", false
}

func ParseStrictJSON(data []byte) (any, error) {
	if !utf8.Valid(data) {
		return nil, schemaErr("invalid UTF-8")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	value, err := parseValue(decoder, 0)
	if err != nil {
		return nil, err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil, schemaErr("trailing JSON data")
	}
	if _, err := canonicalize(data); err != nil {
		return nil, schemaErr("invalid I-JSON")
	}
	return value, nil
}

func parseValue(decoder *json.Decoder, depth int) (any, error) {
	token, err := decoder.Token()
	if err != nil {
		return nil, schemaErr("invalid JSON")
	}
	delimiter, container := token.(json.Delim)
	if !container {
		return token, nil
	}
	if depth >= MaxJSONDepth {
		return nil, schemaErr("JSON depth exceeds limit")
	}
	switch delimiter {
	case '{':
		object := map[string]any{}
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return nil, schemaErr("invalid object key")
			}
			key, ok := keyToken.(string)
			if !ok {
				return nil, schemaErr("expected string key")
			}
			if _, exists := object[key]; exists {
				return nil, schemaErr("duplicate object key")
			}
			value, err := parseValue(decoder, depth+1)
			if err != nil {
				return nil, err
			}
			object[key] = value
		}
		if end, err := decoder.Token(); err != nil || end != json.Delim('}') {
			return nil, schemaErr("invalid object end")
		}
		return object, nil
	case '[':
		array := []any{}
		for decoder.More() {
			value, err := parseValue(decoder, depth+1)
			if err != nil {
				return nil, err
			}
			array = append(array, value)
		}
		if end, err := decoder.Token(); err != nil || end != json.Delim(']') {
			return nil, schemaErr("invalid array end")
		}
		return array, nil
	default:
		return nil, schemaErr("unexpected delimiter")
	}
}

func DecodeStrict(raw []byte, destination any) error {
	if _, err := ParseStrictJSON(raw); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return schemaErr("JSON decode failed")
	}
	return nil
}

func CanonicalJSON(value any) ([]byte, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	if _, err := ParseStrictJSON(raw); err != nil {
		return nil, err
	}
	return canonicalize(raw)
}

func canonicalize(raw []byte) ([]byte, error) {
	wrapped := make([]byte, 0, len(raw)+2)
	wrapped = append(wrapped, '[')
	wrapped = append(wrapped, raw...)
	wrapped = append(wrapped, ']')
	result, err := jsoncanonicalizer.Transform(wrapped)
	if err != nil {
		return nil, err
	}
	return result[1 : len(result)-1], nil
}

func CanonicalSHA256(value any) (string, error) {
	canonical, err := CanonicalJSON(value)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(canonical)
	return hex.EncodeToString(digest[:]), nil
}
