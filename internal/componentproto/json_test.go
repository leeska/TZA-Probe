package componentproto

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestUnrelatedComponentsShareStrictDecodingAndCanonicalDigests(t *testing.T) {
	var notification struct {
		Channel string `json:"channel"`
	}
	var targetSet struct {
		Targets []string `json:"targets"`
	}
	if err := DecodeStrict([]byte(`{"channel":"local"}`), &notification); err != nil {
		t.Fatal(err)
	}
	if err := DecodeStrict([]byte(`{"targets":["node-a"]}`), &targetSet); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{
		`{"channel":"one","channel":"two"}`, `{"channel":"one","root_hook":"command"}`,
		`{"channel":"\ud800"}`, `{"channel":"one"}{}`, strings.Repeat("[", 17) + strings.Repeat("]", 17),
	} {
		if err := DecodeStrict([]byte(raw), &notification); err == nil {
			t.Fatalf("unsafe component configuration accepted: %s", raw)
		}
	}
	first, err := CanonicalSHA256(map[string]any{"revision": json.Number("1"), "config": map[string]string{"channel": "local"}})
	if err != nil {
		t.Fatal(err)
	}
	second, err := CanonicalSHA256(map[string]any{"config": map[string]string{"channel": "local"}, "revision": 1})
	if err != nil || first != second {
		t.Fatal("equivalent component configurations have different identities", first, second, err)
	}
}
