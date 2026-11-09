package toolserver

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"gopkg.in/yaml.v3"

	"github.com/cdmx-in/kera/internal/tools"
)

// OpenAPI 3.x tool servers: every operation becomes one function. Path,
// query and header parameters and the properties of a JSON object body are
// flattened into one argument object; a body that is not an object (or
// whose properties clash with a parameter) is passed as the "body" argument.
// ponytail: JSON bodies only (no form/multipart), servers[] is ignored (calls
// go to the configured URL), only document-local $refs are resolved.

const maxResponse = 32 << 20

var httpMethods = []string{"get", "post", "put", "patch", "delete"}

type operation struct {
	Method    string
	Path      string
	Params    []opParam
	Body      bool
	BodyWhole bool     // the whole body is the "body" argument
	BodyProps []string // body properties flattened into the arguments
}

type opParam struct {
	Name string
	In   string // path | query | header
}

func (s *Server) specURL() string {
	p := s.row.SpecPath
	if strings.HasPrefix(p, "http://") || strings.HasPrefix(p, "https://") {
		return p
	}
	if p == "" {
		p = "openapi.json"
	}
	return strings.TrimRight(s.row.URL, "/") + "/" + strings.TrimLeft(p, "/")
}

func (s *Server) listOpenAPI(ctx context.Context, tok string) (cachedSpecs, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.specURL(), nil)
	if err != nil {
		return cachedSpecs{}, err
	}
	s.authorize(req.Header, tok)
	req.Header.Set("Accept", "application/json, application/yaml;q=0.9")
	resp, err := s.m.http.Do(req)
	if err != nil {
		return cachedSpecs{}, fmt.Errorf("fetch spec: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return cachedSpecs{}, fmt.Errorf("fetch spec: %w", httpError(resp))
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponse))
	if err != nil {
		return cachedSpecs{}, fmt.Errorf("read spec: %w", err)
	}
	specs, ops, err := ConvertOpenAPI(body)
	if err != nil {
		return cachedSpecs{}, err
	}
	return cachedSpecs{specs: specs, ops: ops}, nil
}

// ConvertOpenAPI turns an OpenAPI document (JSON or YAML) into function specs
// and the call mapping per function name.
func ConvertOpenAPI(doc []byte) ([]tools.Spec, map[string]*operation, error) {
	var root map[string]any
	if err := json.Unmarshal(doc, &root); err != nil {
		if yerr := yaml.Unmarshal(doc, &root); yerr != nil {
			return nil, nil, fmt.Errorf("parse spec: %w", err)
		}
	}
	paths, _ := root["paths"].(map[string]any)
	if paths == nil {
		return nil, nil, fmt.Errorf("spec has no paths")
	}
	r := resolver{root: root}
	var specs []tools.Spec
	ops := map[string]*operation{}
	for _, p := range sortedKeys(paths) {
		item, _ := r.resolve(paths[p]).(map[string]any)
		for _, m := range httpMethods {
			raw, ok := item[m].(map[string]any)
			if !ok {
				continue
			}
			spec, op := r.operation(p, m, item, raw)
			if _, dup := ops[spec.Name]; dup {
				continue
			}
			specs = append(specs, spec)
			ops[spec.Name] = op
		}
	}
	return specs, ops, nil
}

func (r resolver) operation(path, method string, item, raw map[string]any) (tools.Spec, *operation) {
	op := &operation{Method: strings.ToUpper(method), Path: path}
	name, _ := raw["operationId"].(string)
	if name == "" {
		name = method + "_" + strings.Trim(path, "/")
	}
	props := map[string]any{}
	var required []string
	// path-level parameters first, the operation's own override by name+in
	params := map[string]map[string]any{}
	var order []string
	for _, list := range []any{item["parameters"], raw["parameters"]} {
		arr, _ := list.([]any)
		for _, x := range arr {
			p, _ := r.resolve(x).(map[string]any)
			n, _ := p["name"].(string)
			in, _ := p["in"].(string)
			if n == "" || (in != "path" && in != "query" && in != "header") {
				continue
			}
			k := in + ":" + n
			if _, seen := params[k]; !seen {
				order = append(order, k)
			}
			params[k] = p
		}
	}
	for _, k := range order {
		p := params[k]
		n, in := p["name"].(string), p["in"].(string)
		schema, _ := r.resolve(p["schema"]).(map[string]any)
		if schema == nil {
			schema = map[string]any{"type": "string"}
		}
		if d, ok := p["description"].(string); ok && d != "" {
			if _, has := schema["description"]; !has {
				schema["description"] = d
			}
		}
		props[n] = schema
		if req, _ := p["required"].(bool); req || in == "path" {
			required = append(required, n)
		}
		op.Params = append(op.Params, opParam{Name: n, In: in})
	}
	if body, ok := r.resolve(raw["requestBody"]).(map[string]any); ok {
		if schema := jsonSchema(body); schema != nil {
			op.Body = true
			bodyReq, _ := body["required"].(bool)
			bp, isObj := schema["properties"].(map[string]any)
			clash := false
			for k := range bp {
				if _, ok := props[k]; ok {
					clash = true
				}
			}
			if isObj && !clash {
				for _, k := range sortedKeys(bp) {
					props[k] = bp[k]
					op.BodyProps = append(op.BodyProps, k)
				}
				if rq, ok := schema["required"].([]any); ok {
					for _, x := range rq {
						if s, ok := x.(string); ok {
							required = append(required, s)
						}
					}
				}
			} else {
				op.BodyWhole = true
				props["body"] = schema
				if bodyReq {
					required = append(required, "body")
				}
			}
		}
	}
	params2 := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		params2["required"] = required
	}
	return tools.Spec{Name: funcName(name), Description: describe(raw), Parameters: mustJSON(params2)}, op
}

// jsonSchema returns the resolved schema of a JSON request body.
func jsonSchema(body map[string]any) map[string]any {
	content, _ := body["content"].(map[string]any)
	for _, ct := range sortedKeys(content) {
		if ct == "application/json" || strings.HasSuffix(ct, "+json") || ct == "*/*" {
			mt, _ := content[ct].(map[string]any)
			if s, ok := mt["schema"].(map[string]any); ok {
				return s
			}
			return map[string]any{"type": "object"}
		}
	}
	return nil
}

func describe(op map[string]any) string {
	sum, _ := op["summary"].(string)
	desc, _ := op["description"].(string)
	out := strings.TrimSpace(sum)
	if d := strings.TrimSpace(desc); d != "" && d != out {
		if out != "" {
			out += "\n"
		}
		out += d
	}
	if utf8.RuneCountInString(out) > 1024 {
		out = string([]rune(out)[:1024])
	}
	return out
}

var nonName = regexp.MustCompile(`[^a-zA-Z0-9_-]+`)

// funcName makes a function name providers accept: [a-zA-Z0-9_-]{1,64}.
func funcName(s string) string {
	s = strings.Trim(nonName.ReplaceAllString(s, "_"), "_")
	if len(s) > 64 {
		s = s[:64]
	}
	if s == "" {
		s = "operation"
	}
	return s
}

type resolver struct{ root map[string]any }

// resolve returns a copy of v with document-local $refs inlined. A ref that
// is already being expanded (a cycle) or a chain deeper than 32 becomes an
// open object schema.
func (r resolver) resolve(v any) any { return r.expand(v, nil) }

func (r resolver) expand(v any, active []string) any {
	switch x := v.(type) {
	case map[string]any:
		if ref, ok := x["$ref"].(string); ok {
			target := r.pointer(ref)
			if target == nil || slices.Contains(active, ref) || len(active) > 32 {
				return map[string]any{"type": "object"}
			}
			return r.expand(target, append(active, ref))
		}
		out := make(map[string]any, len(x))
		for k, val := range x {
			out[k] = r.expand(val, active)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, val := range x {
			out[i] = r.expand(val, active)
		}
		return out
	}
	return v
}

func (r resolver) pointer(ref string) any {
	if !strings.HasPrefix(ref, "#/") {
		return nil
	}
	var cur any = r.root
	for _, part := range strings.Split(ref[2:], "/") {
		part = strings.ReplaceAll(strings.ReplaceAll(part, "~1", "/"), "~0", "~")
		m, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = m[part]
	}
	return cur
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// ---- calls -----------------------------------------------------------------------

func (s *Server) callOpenAPI(ctx context.Context, c tools.Call, op *operation, tok string) (tools.Result, error) {
	if op == nil {
		return tools.Result{}, fmt.Errorf("no operation for %s", c.Name)
	}
	args := map[string]any{}
	if len(bytes.TrimSpace(c.Args)) > 0 && string(c.Args) != "null" {
		if err := json.Unmarshal(c.Args, &args); err != nil {
			return errResult("arguments are not a JSON object: %v", err), nil
		}
	}
	req, err := buildRequest(ctx, strings.TrimRight(s.row.URL, "/"), op, args)
	if err != nil {
		return tools.Result{}, err
	}
	s.authorize(req.Header, tok)
	contextHeaders(req.Header, c)
	resp, err := s.m.http.Do(req)
	if err != nil {
		return tools.Result{}, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponse))
	if err != nil {
		return tools.Result{}, fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode >= 300 {
		return errResult("HTTP %d: %s", resp.StatusCode, Truncate(strings.TrimSpace(string(body)), 2000)), nil
	}
	return openAPIResult(body), nil
}

func buildRequest(ctx context.Context, base string, op *operation, args map[string]any) (*http.Request, error) {
	path := op.Path
	q := url.Values{}
	hdr := http.Header{}
	for _, p := range op.Params {
		v, ok := args[p.Name]
		if !ok || v == nil {
			continue
		}
		switch p.In {
		case "path":
			sv := scalar(v)
			if sv == "." || sv == ".." { // would climb to another endpoint of the server
				return nil, fmt.Errorf("path parameter %s may not be %q", p.Name, sv)
			}
			path = strings.ReplaceAll(path, "{"+p.Name+"}", url.PathEscape(sv))
		case "query":
			if arr, ok := v.([]any); ok {
				for _, x := range arr {
					q.Add(p.Name, scalar(x))
				}
			} else {
				q.Set(p.Name, scalar(v))
			}
		case "header":
			hdr.Set(p.Name, scalar(v))
		}
	}
	u := base + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	var body io.Reader
	if op.Body {
		var payload any
		if op.BodyWhole {
			payload = args["body"]
		} else {
			m := map[string]any{}
			for _, k := range op.BodyProps {
				if v, ok := args[k]; ok {
					m[k] = v
				}
			}
			payload = m
		}
		if payload != nil {
			body = bytes.NewReader(mustJSON(payload))
			hdr.Set("Content-Type", "application/json")
		}
	}
	req, err := http.NewRequestWithContext(ctx, op.Method, u, body)
	if err != nil {
		return nil, err
	}
	for k, v := range hdr {
		req.Header[k] = v
	}
	req.Header.Set("Accept", "application/json, */*;q=0.5")
	return req, nil
}

func scalar(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case bool, nil:
		return fmt.Sprint(x)
	}
	return string(mustJSON(v))
}

// openAPIResult: a JSON object with "content" feeds only that to the model
// and "parts" (UI blocks), "files" and "sources" to the chat, "images"
// (file ids) to the model and the tool step, and "inputs" (file ids) to later
// tool calls and the step only; anything else is passed to the model as is.
func openAPIResult(body []byte) tools.Result {
	var env struct {
		Content *json.RawMessage  `json:"content"`
		Parts   []json.RawMessage `json:"parts"`
		Files   []string          `json:"files"`
		Images  []string          `json:"images"`
		Inputs  []string          `json:"inputs"`
		Sources []tools.Source    `json:"sources"`
	}
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) > 0 && trimmed[0] == '{' && json.Unmarshal(trimmed, &env) == nil && env.Content != nil {
		var text string
		if json.Unmarshal(*env.Content, &text) != nil {
			text = string(*env.Content)
		}
		return tools.Result{Content: text, Parts: env.Parts, Files: env.Files, Images: env.Images, Inputs: env.Inputs, Sources: env.Sources}
	}
	return tools.Result{Content: string(trimmed)}
}
