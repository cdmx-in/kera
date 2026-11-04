package apply

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"

	"github.com/cdmx-in/kera/internal/secret"
	"github.com/cdmx-in/kera/internal/settings"
)

// setting is one config row as apply compares it: canonical plaintext JSON.
type setting struct {
	JSON   string
	Secret bool
}

// sections are the Config fields whose leaves map 1:1 to config keys.
func sections(cfg *Config) map[string]reflect.Value {
	v := reflect.ValueOf(cfg).Elem()
	return map[string]reflect.Value{
		"site": v.FieldByName("Site"), "auth": v.FieldByName("Auth"), "tasks": v.FieldByName("Tasks"),
		"chat": v.FieldByName("Chat"), "rag": v.FieldByName("RAG"), "web": v.FieldByName("Web"),
		"code_interpreter": v.FieldByName("CodeInterpreter"), "spend": v.FieldByName("Spend"),
		"compat": v.FieldByName("Compat"), "cowork": v.FieldByName("Cowork"),
	}
}

// walk visits every leaf of a section struct. Pointers to structs are
// descended into; everything else is a leaf named <prefix>.<yaml name> or by
// its key tag. Fields tagged apply:"-" are handled elsewhere.
func walk(prefix string, t reflect.Type, visit func(key string, path []int, secret bool)) {
	var rec func(prefix string, t reflect.Type, path []int)
	rec = func(prefix string, t reflect.Type, path []int) {
		for i := range t.NumField() {
			f := t.Field(i)
			if f.Tag.Get("apply") == "-" {
				continue
			}
			key := prefix + "." + strings.Split(f.Tag.Get("yaml"), ",")[0]
			if k := f.Tag.Get("key"); k != "" {
				key = k
			}
			p := append(append([]int{}, path...), i)
			if f.Type.Kind() == reflect.Pointer && f.Type.Elem().Kind() == reflect.Struct {
				rec(key, f.Type.Elem(), p)
				continue
			}
			visit(key, p, f.Tag.Get("secret") == "true")
		}
	}
	rec(prefix, t, nil)
}

// field follows path from a section struct value; ok is false when a pointer
// on the way is nil (alloc=false) .
func field(v reflect.Value, path []int, alloc bool) (reflect.Value, bool) {
	for _, i := range path {
		if v.Kind() == reflect.Pointer {
			if v.IsNil() {
				if !alloc {
					return reflect.Value{}, false
				}
				v.Set(reflect.New(v.Type().Elem()))
			}
			v = v.Elem()
		}
		v = v.Field(i)
	}
	return v, true
}

// desiredSettings returns key -> value for every leaf the file sets.
func desiredSettings(cfg *Config) (map[string]setting, error) {
	out := map[string]setting{}
	put := func(key string, v any, secret bool) error {
		b, err := canonical(v)
		if err != nil {
			return fmt.Errorf("setting %s: %w", key, err)
		}
		out[key] = setting{JSON: b, Secret: secret}
		return nil
	}
	var errs []error
	for name, sec := range sections(cfg) {
		if sec.IsNil() {
			continue
		}
		walk(name, sec.Type().Elem(), func(key string, path []int, secret bool) {
			leaf, ok := field(sec, path, false)
			if !ok || leaf.IsNil() {
				return
			}
			errs = append(errs, put(key, leaf.Interface(), secret))
		})
	}
	if b := cfg.Brand; b != nil {
		errs = append(errs, put("ui.brand", b, false))
		if b.ToolDisplay != nil {
			errs = append(errs, put("ui.tool_display", b.ToolDisplay, false))
		}
	}
	if cfg.Integrations != nil {
		errs = append(errs, put("ui.integrations", cfg.Integrations, false))
	}
	if cfg.Suggestions != nil {
		errs = append(errs, put("site.suggestions", cfg.Suggestions, false))
	}
	if cfg.Permissions != nil {
		errs = append(errs, put("user.permissions", mergePerms(defaultPerms(), cfg.Permissions), false))
	}
	if cfg.Auth != nil && cfg.Auth.OAuth != nil && cfg.Auth.OAuth.Google != nil {
		g := cfg.Auth.OAuth.Google
		if g.MergeByEmail != nil {
			errs = append(errs, put("auth.oauth.merge_by_email", *g.MergeByEmail, false))
		}
		if g.UpdatePicture != nil {
			errs = append(errs, put("auth.oauth.update_picture", *g.UpdatePicture, false))
		}
		if g.AllowedDomains != nil {
			errs = append(errs, put("auth.oauth.allowed_domains", g.AllowedDomains, false))
		}
	}
	return out, errors.Join(errs...)
}

// settingKeys lists every key apply can write (for tests and validation).
func settingKeys() map[string]bool {
	keys := map[string]bool{"ui.brand": false, "ui.tool_display": false, "ui.integrations": false,
		"site.suggestions": false, "user.permissions": false, "auth.oauth.merge_by_email": false,
		"auth.oauth.update_picture": false, "auth.oauth.allowed_domains": false}
	for name, sec := range sections(&Config{}) {
		walk(name, sec.Type().Elem(), func(key string, _ []int, secret bool) { keys[key] = secret })
	}
	return keys
}

// fillSettings sets cfg leaves from managed config rows (export). Secret
// values become ${...} placeholders, which are returned.
func fillSettings(cfg *Config, rows map[string]setting) ([]string, error) {
	var placeholders []string
	set := func(dst any, key string) error {
		if err := json.Unmarshal([]byte(rows[key].JSON), dst); err != nil {
			return fmt.Errorf("setting %s: %w", key, err)
		}
		return nil
	}
	for name, sec := range sections(cfg) {
		var err error
		walk(name, sec.Type().Elem(), func(key string, path []int, secret bool) {
			if _, ok := rows[key]; !ok || err != nil {
				return
			}
			leaf, _ := field(sec, path, true)
			if secret {
				p := placeholder(key)
				placeholders = append(placeholders, p)
				leaf.Set(reflect.ValueOf(&p))
				return
			}
			err = set(leaf.Addr().Interface(), key)
		})
		if err != nil {
			return nil, err
		}
	}
	if _, ok := rows["ui.brand"]; ok {
		cfg.Brand = &Brand{}
		if err := set(cfg.Brand, "ui.brand"); err != nil {
			return nil, err
		}
	}
	if _, ok := rows["ui.tool_display"]; ok {
		if cfg.Brand == nil {
			cfg.Brand = &Brand{}
		}
		if err := set(&cfg.Brand.ToolDisplay, "ui.tool_display"); err != nil {
			return nil, err
		}
	}
	for key, dst := range map[string]any{"ui.integrations": &cfg.Integrations, "site.suggestions": &cfg.Suggestions,
		"user.permissions": &cfg.Permissions} {
		if _, ok := rows[key]; ok {
			if err := set(dst, key); err != nil {
				return nil, err
			}
		}
	}
	return placeholders, nil
}

// fillGoogleSettings moves the auth.oauth.* rows into the Google login block.
func fillGoogleSettings(g *GoogleLogin, rows map[string]setting) error {
	for key, dst := range map[string]any{"auth.oauth.merge_by_email": &g.MergeByEmail,
		"auth.oauth.update_picture": &g.UpdatePicture, "auth.oauth.allowed_domains": &g.AllowedDomains} {
		if s, ok := rows[key]; ok {
			if err := json.Unmarshal([]byte(s.JSON), dst); err != nil {
				return fmt.Errorf("setting %s: %w", key, err)
			}
		}
	}
	return nil
}

// canonical renders v as JSON with sorted keys and normalised numbers, so a
// value read back from jsonb compares equal to the one in the file.
func canonical(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return canonicalJSON(b)
}

func canonicalJSON(b []byte) (string, error) {
	var x any
	if err := json.Unmarshal(b, &x); err != nil {
		return "", err
	}
	out, err := json.Marshal(x)
	return string(out), err
}

// encSetting is how a secret setting is stored: {"enc": base64(AES-GCM)}.
type encSetting struct {
	Enc string `json:"enc"`
}

func sealSetting(keys *secret.Keys, plainJSON string) (string, error) {
	var s string
	if err := json.Unmarshal([]byte(plainJSON), &s); err != nil {
		return "", errors.New("secret settings must be strings")
	}
	b, err := keys.Encrypt([]byte(s))
	if err != nil {
		return "", err
	}
	out, err := json.Marshal(encSetting{Enc: base64.StdEncoding.EncodeToString(b)})
	return string(out), err
}

func openSetting(keys *secret.Keys, stored []byte) (string, error) {
	var e encSetting
	if err := json.Unmarshal(stored, &e); err != nil || e.Enc == "" {
		return "", errors.New("not an encrypted value")
	}
	raw, err := base64.StdEncoding.DecodeString(e.Enc)
	if err != nil {
		return "", err
	}
	p, err := keys.Decrypt(raw)
	if err != nil {
		return "", err
	}
	return string(p), nil
}

// ReadSecret returns the plaintext of a secret setting written by apply
// (code_interpreter.token, spend.source.dsn), or "" when it is not set.
func ReadSecret(ctx context.Context, st *settings.Store, keys *secret.Keys, key string) (string, error) {
	raw, err := st.Raw(ctx, key)
	if err != nil || raw == nil {
		return "", err
	}
	s, err := openSetting(keys, raw)
	if err != nil {
		return "", fmt.Errorf("setting %s: %w", key, err)
	}
	return s, nil
}

// placeholder names the environment variable export writes for a secret.
func placeholder(parts ...string) string {
	s := strings.ToUpper(strings.Join(parts, "_"))
	s = strings.Map(func(r rune) rune {
		if r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' {
			return r
		}
		return '_'
	}, s)
	return "${KERA_" + s + "}"
}

// defaultPerms is the built-in user.permissions tree.
func defaultPerms() Permissions {
	var p Permissions
	_ = json.Unmarshal(settings.Defaults["user.permissions"], &p)
	return p
}

func mergePerms(base Permissions, over Permissions) Permissions {
	out := Permissions{}
	for _, src := range []Permissions{base, over} {
		for cat, leaves := range src {
			if out[cat] == nil {
				out[cat] = map[string]bool{}
			}
			for k, v := range leaves {
				out[cat][k] = v
			}
		}
	}
	return out
}
