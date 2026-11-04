package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

// Field checks shared by the admin forms; the web console mirrors them in
// web/src/features/admin/settings/validate.ts.

var idRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._:-]*$`)

// CheckID requires a new row's id to use lowercase letters, digits, dot, dash,
// underscore and colon. Existing rows keep their ids (imported ones may differ).
func CheckID(field, id string) error {
	if id == "" {
		return FieldErr(field, "bad_id", "ID is required")
	}
	if !idRe.MatchString(id) {
		return FieldErr(field, "bad_id", "Use lowercase letters, digits and . _ : - only")
	}
	return nil
}

// CheckURL requires an http(s) URL with a host and no surrounding spaces.
func CheckURL(field, s string) error {
	if s == "" {
		return FieldErr(field, "bad_url", "URL is required")
	}
	u, err := url.Parse(s)
	if err != nil || strings.TrimSpace(s) != s || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return FieldErr(field, "bad_url", "Must be an http(s) URL with a host, e.g. http://host:8080")
	}
	return nil
}

// DecodeForm is Decode, but a value of the wrong JSON type (1.5 for an
// integer, "x" for a number) becomes an error on that field.
func DecodeForm(r *http.Request, v any) error {
	r.Body = http.MaxBytesReader(nil, r.Body, 32<<20)
	err := json.NewDecoder(r.Body).Decode(v)
	var te *json.UnmarshalTypeError
	if errors.As(err, &te) && te.Field != "" {
		return FieldErr(te.Field, "bad_"+te.Field, "Must be "+article(te.Type.Kind().String()))
	}
	if err != nil {
		return Errf(http.StatusBadRequest, "bad_json", "invalid JSON body: "+err.Error())
	}
	return nil
}

func article(kind string) string {
	switch {
	case strings.HasPrefix(kind, "int"), strings.HasPrefix(kind, "uint"):
		return "a whole number"
	case strings.HasPrefix(kind, "float"):
		return "a number"
	case kind == "bool":
		return "true or false"
	case kind == "slice":
		return "a list"
	}
	return "a " + kind
}
