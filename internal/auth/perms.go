package auth

import (
	"encoding/json"
	"strings"
)

// Permissions is a nested map of booleans, e.g. {"features": {"api_keys": true}}.
type Permissions map[string]any

// Merge returns base with every true leaf from overlays added (any-true wins:
// groups can grant, never revoke).
func Merge(base Permissions, overlays ...Permissions) Permissions {
	out := clone(base)
	for _, o := range overlays {
		mergeInto(out, o)
	}
	return out
}

// Allowed reports whether the dotted path (e.g. "features.api_keys") is true.
func (p Permissions) Allowed(path string) bool {
	var cur any = map[string]any(p)
	for _, k := range strings.Split(path, ".") {
		m, ok := cur.(map[string]any)
		if !ok {
			return false
		}
		cur = m[k]
	}
	b, _ := cur.(bool)
	return b
}

// ParsePermissions decodes a JSON object (nil or invalid yields empty).
func ParsePermissions(raw []byte) Permissions {
	p := Permissions{}
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &p)
	}
	return p
}

func mergeInto(dst map[string]any, src map[string]any) {
	for k, v := range src {
		switch sv := v.(type) {
		case map[string]any:
			dm, ok := dst[k].(map[string]any)
			if !ok {
				dm = map[string]any{}
				dst[k] = dm
			}
			mergeInto(dm, sv)
		case bool:
			if sv {
				dst[k] = true
			} else if _, exists := dst[k]; !exists {
				dst[k] = false
			}
		}
	}
}

func clone(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		if sub, ok := v.(map[string]any); ok {
			out[k] = clone(sub)
		} else {
			out[k] = v
		}
	}
	return out
}
