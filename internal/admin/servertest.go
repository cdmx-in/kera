package admin

// The editor dialogs' Test buttons: each takes the unsaved form, calls the
// server it names through probe.Client (10 s, size cap, no link-local or
// metadata addresses) and answers a probe.Result. Nothing is saved; a typed
// secret wins, else the stored one of the same id is used; secrets are masked
// in error text and never returned.

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/cdmx-in/kera/internal/api"
	"github.com/cdmx-in/kera/internal/apply"
	"github.com/cdmx-in/kera/internal/auth"
	"github.com/cdmx-in/kera/internal/oauthclient"
	"github.com/cdmx-in/kera/internal/probe"
	"github.com/cdmx-in/kera/internal/toolserver"
)

// get fetches url and returns the status and the start of the body.
func get(ctx context.Context, hc *http.Client, method, url string, hdr http.Header, body []byte) (int, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, method, url, bytes.NewReader(body))
	if err != nil {
		return 0, nil, err
	}
	for k, v := range hdr {
		req.Header[k] = v
	}
	resp, err := hc.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	return resp.StatusCode, b, nil
}

func clip(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return s
}

// ---- tool servers ----------------------------------------------------------------

func (h *handler) storedToolKey(ctx context.Context, id string) string {
	var a apply.ToolAuthColumn
	if err := h.a.DB.QueryRow(ctx, `SELECT auth FROM tool_server WHERE id = $1`, id).Scan(&a); err != nil || a.KeyEnc == "" {
		return ""
	}
	sealed, err := base64.StdEncoding.DecodeString(a.KeyEnc)
	if err != nil {
		return ""
	}
	k, _ := h.a.Keys.DecryptString(sealed)
	return k
}

// testToolServer lists the draft's functions: OpenAPI spec or MCP tools/list.
// A user_oauth server uses the calling admin's own grant; without one it only
// checks that the URL answers.
func (h *handler) testToolServer(w http.ResponseWriter, r *http.Request) {
	var f toolForm
	if err := api.DecodeForm(r, &f); err != nil {
		api.Fail(w, r, err)
		return
	}
	if err := f.validate(); err != nil {
		api.Fail(w, r, err)
		return
	}
	key := ""
	if f.Auth.Key != nil {
		key = *f.Auth.Key
	} else if f.ID != "" {
		key = h.storedToolKey(r.Context(), f.ID)
	}
	if key == "" && (f.Auth.Type == "bearer" || f.Auth.Type == "header") {
		api.Fail(w, r, api.FieldErr("auth.key", "bad_auth", "Enter the key to test with"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), probe.Timeout)
	defer cancel()
	start, hc, res := time.Now(), probe.Client(), &probe.Result{OK: true}
	tok := ""
	if f.Auth.Type == "user_oauth" {
		var err error
		tok, err = oauthclient.For(h.a).AccessToken(ctx, auth.FromContext(ctx).ID, f.Auth.Provider)
		if errors.Is(err, oauthclient.ErrNotConnected) {
			status, _, err := get(ctx, hc, http.MethodGet, f.URL, nil, nil)
			if err != nil {
				res.Fail(err, key)
			} else {
				res.Status, res.Note = status, fmt.Sprintf("Reachable (HTTP %d). Connect your own account first to list tools.", status)
			}
			api.JSON(w, http.StatusOK, res.Since(start))
			return
		}
		if err != nil {
			api.JSON(w, http.StatusOK, res.Fail(err, key).Since(start))
			return
		}
	}
	row := toolserver.Row{ID: f.ID, Kind: f.Kind, URL: f.URL, SpecPath: f.SpecPath, NameFilter: f.NameFilter, Disabled: f.DisabledFunctions,
		Auth: toolserver.Auth{Type: f.Auth.Type, Header: f.Auth.Header, Provider: f.Auth.Provider}}
	names, err := toolserver.Probe(ctx, hc, row, key, tok)
	if err != nil {
		res.Fail(err, key, tok)
	}
	res.Tools = names
	api.JSON(w, http.StatusOK, res.Since(start))
}

// ---- hook servers ----------------------------------------------------------------

// testHook calls GET {url}/health; a server without one (404, 405, 501) gets
// POST {url}/inlet (or /outlet) with a dummy envelope marked "test": true.
func (h *handler) testHook(w http.ResponseWriter, r *http.Request) {
	var f hookForm
	if err := api.DecodeForm(r, &f); err != nil {
		api.Fail(w, r, err)
		return
	}
	if err := f.validate(); err != nil {
		api.Fail(w, r, err)
		return
	}
	key := ""
	if f.Key != nil {
		key = *f.Key
	} else if f.ID != "" {
		var enc []byte
		if h.a.DB.QueryRow(r.Context(), `SELECT key_enc FROM hook_server WHERE id = $1`, f.ID).Scan(&enc) == nil {
			key, _ = h.a.Keys.DecryptString(enc)
		}
	}
	hdr := http.Header{"Content-Type": {"application/json"}}
	if key != "" {
		hdr.Set("Authorization", "Bearer "+key)
	}
	ctx, cancel := context.WithTimeout(r.Context(), probe.Timeout)
	defer cancel()
	start, hc, res := time.Now(), probe.Client(), &probe.Result{}
	base := strings.TrimRight(f.URL, "/")
	status, body, err := get(ctx, hc, http.MethodGet, base+"/health", hdr, nil)
	res.Via = "GET /health"
	if err == nil && (status == http.StatusNotFound || status == http.StatusMethodNotAllowed || status == http.StatusNotImplemented) {
		event := f.Events[0]
		u := auth.FromContext(ctx)
		env := map[string]any{"test": true,
			"hook":    map[string]any{"id": f.ID, "config": f.Config},
			"run":     map[string]any{"id": "test", "chat_id": "", "message_id": "", "temporary": true},
			"user":    map[string]any{"id": u.ID, "name": u.Name, "email": u.Email, "role": u.Role, "groups": []string{}},
			"model":   map[string]any{"id": "", "base_model_id": "", "local": false, "connection": map[string]any{"id": "", "kind": ""}},
			"request": map[string]any{"messages": []map[string]any{{"role": "user", "content": "Kera hook test"}}, "params": map[string]any{}, "features": map[string]any{}, "provider_tools": []any{}},
			"files":   []string{},
		}
		if event == "outlet" {
			env["message"] = map[string]any{"id": "", "content": "Kera hook test", "parts": []any{}}
		}
		b, _ := json.Marshal(env)
		res.Via = "POST /" + event + " (no /health)"
		status, body, err = get(ctx, hc, http.MethodPost, base+"/"+event, hdr, b)
	}
	if err != nil {
		api.JSON(w, http.StatusOK, res.Fail(err, key).Since(start))
		return
	}
	res.Status, res.OK = status, status/100 == 2
	if !res.OK {
		res.Error = probe.Redact(fmt.Sprintf("HTTP %d: %s", status, clip(body)), key)
	}
	api.JSON(w, http.StatusOK, res.Since(start))
}

// ---- OAuth providers ----------------------------------------------------------------

// Google's endpoints, used by sign-in providers that leave both empty (as internal/identity does).
const (
	googleAuthURL  = "https://accounts.google.com/o/oauth2/v2/auth"
	googleTokenURL = "https://oauth2.googleapis.com/token"
)

// testProvider checks the issuer's discovery document against the endpoints
// and that each endpoint answers (any HTTP status counts). The client secret
// is never sent anywhere.
func (h *handler) testProvider(w http.ResponseWriter, r *http.Request) {
	var f providerForm
	if err := api.DecodeForm(r, &f); err != nil {
		api.Fail(w, r, err)
		return
	}
	if err := f.validate(); err != nil {
		api.Fail(w, r, err)
		return
	}
	if f.Login && f.AuthorizationEndpoint == "" && f.TokenEndpoint == "" {
		f.AuthorizationEndpoint, f.TokenEndpoint = googleAuthURL, googleTokenURL
	}
	ctx, cancel := context.WithTimeout(r.Context(), probe.Timeout)
	defer cancel()
	start, hc, res := time.Now(), probe.Client(), &probe.Result{OK: true}
	add := func(c probe.Check) {
		res.Checks = append(res.Checks, c)
		res.OK = res.OK && c.OK
	}
	endpoints := []struct{ name, url string }{{"authorization_endpoint", f.AuthorizationEndpoint},
		{"token_endpoint", f.TokenEndpoint}, {"userinfo_endpoint", f.UserinfoEndpoint}, {"revocation_endpoint", f.RevocationEndpoint}}
	if f.Issuer != "" {
		status, body, err := get(ctx, hc, http.MethodGet, strings.TrimRight(f.Issuer, "/")+"/.well-known/openid-configuration", nil, nil)
		var doc map[string]any
		switch {
		case err != nil:
			add(probe.Check{Name: "Issuer discovery", Detail: err.Error()})
		case status != http.StatusOK:
			add(probe.Check{Name: "Issuer discovery", Detail: fmt.Sprintf("HTTP %d", status)})
		case json.Unmarshal(body, &doc) != nil:
			add(probe.Check{Name: "Issuer discovery", Detail: "not a JSON discovery document"})
		default:
			add(probe.Check{Name: "Issuer discovery", OK: true})
			var diff []string
			for _, e := range endpoints {
				if got, _ := doc[e.name].(string); e.url != "" && got != "" && got != e.url {
					diff = append(diff, fmt.Sprintf("%s is %s", e.name, got))
				}
			}
			add(probe.Check{Name: "Endpoints match the issuer", OK: len(diff) == 0, Detail: "issuer says " + strings.Join(diff, "; ")})
			if len(diff) == 0 {
				res.Checks[len(res.Checks)-1].Detail = ""
			}
		}
	}
	for _, e := range endpoints {
		if e.url == "" {
			continue
		}
		name := strings.ToUpper(e.name[:1]) + strings.ReplaceAll(e.name[1:], "_", " ") + " answers"
		status, _, err := get(ctx, hc, http.MethodGet, e.url, nil, nil)
		if err != nil {
			add(probe.Check{Name: name, Detail: err.Error()})
		} else {
			add(probe.Check{Name: name, OK: true, Detail: fmt.Sprintf("HTTP %d", status)})
		}
	}
	api.JSON(w, http.StatusOK, res.Since(start))
}
