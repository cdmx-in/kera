package api

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
)

// Error is the JSON error body. OpenAI-compatible routes wrap it as
// {"error": {...}}; everything else returns {"detail": message, "code": code}
// (the shape existing Open WebUI API clients already read).
type Error struct {
	Status  int    `json:"-"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *Error) Error() string { return e.Message }

// Errf builds an API error.
func Errf(status int, code, msg string) *Error {
	return &Error{Status: status, Code: code, Message: msg}
}

// JSON writes v with status.
func JSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Debug("write json", "err", err)
	}
}

// Fail writes err as a JSON error; unknown errors become 500 and are logged.
func Fail(w http.ResponseWriter, r *http.Request, err error) {
	var e *Error
	if !errors.As(err, &e) {
		slog.Error("request failed", "method", r.Method, "path", r.URL.Path, "err", err)
		e = Errf(http.StatusInternalServerError, "internal", "internal error")
	}
	JSON(w, e.Status, map[string]any{"detail": e.Message, "code": e.Code})
}

// Decode reads a JSON body (max 32 MiB) into v.
func Decode(r *http.Request, v any) error {
	r.Body = http.MaxBytesReader(nil, r.Body, 32<<20)
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		return Errf(http.StatusBadRequest, "bad_json", "invalid JSON body: "+err.Error())
	}
	return nil
}
