package ipsentinel

import "github.com/leeska/TZA-Probe/internal/componentproto"

func ParseStrictJSON(raw []byte) (any, error) {
	value, err := componentproto.ParseStrictJSON(raw)
	return value, schemaJSONError(err)
}

func DecodeStrict(raw []byte, destination any) error {
	return schemaJSONError(componentproto.DecodeStrict(raw, destination))
}

func CanonicalSHA256(value any) (string, error) {
	digest, err := componentproto.CanonicalSHA256(value)
	return digest, schemaJSONError(err)
}

func schemaJSONError(err error) error {
	if message, ok := componentproto.ValidationMessage(err); ok {
		return schemaErr("%s", message)
	}
	return err
}
