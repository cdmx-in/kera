package apply

import (
	"encoding/base64"
	"encoding/json"
	"reflect"

	"github.com/cdmx-in/kera/internal/secret"
)

// Helpers for code outside apply (the admin API) that edits the same rows,
// so a value saved in the UI is stored exactly as apply would store it.

// SettingKeys reports every config key apply can write; true marks secret
// keys (stored as {"enc": ...}, read with ReadSecret, never shown).
func SettingKeys() map[string]bool { return settingKeys() }

// IntegerSettings reports the keys whose value must be a whole number
// (their defaults alone cannot tell: rag.relevance_threshold defaults to 0).
func IntegerSettings() map[string]bool {
	out := map[string]bool{}
	intPtr := reflect.TypeFor[*int]()
	for name, sec := range sections(&Config{}) {
		t := sec.Type().Elem()
		walk(name, t, func(key string, path []int, _ bool) {
			if t.FieldByIndex(path).Type == intPtr {
				out[key] = true
			}
		})
	}
	return out
}

// SealSecret encrypts a secret setting value the way apply stores it.
func SealSecret(keys *secret.Keys, plain string) (json.RawMessage, error) {
	b, err := json.Marshal(plain)
	if err != nil {
		return nil, err
	}
	s, err := sealSetting(keys, string(b))
	return json.RawMessage(s), err
}

// SealToolKey encrypts a tool-server key for ToolAuthColumn.KeyEnc ("" stays "").
func SealToolKey(keys *secret.Keys, plain string) (string, error) {
	b, err := keys.EncryptString(plain)
	if err != nil || b == nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(b), nil
}
