package toolserver

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const petSpec = `{
  "openapi": "3.1.0",
  "paths": {
    "/search": {"get": {"operationId": "search_kb", "summary": "Search the KB", "description": "Full text.",
      "parameters": [{"name": "q", "in": "query", "required": true, "schema": {"type": "string"}, "description": "query text"},
                     {"name": "tags", "in": "query", "schema": {"type": "array", "items": {"type": "string"}}},
                     {"name": "limit", "in": "query", "schema": {"type": "integer", "default": 6}}]}},
    "/pets/{petId}": {
      "parameters": [{"$ref": "#/components/parameters/PetId"}],
      "get": {"operationId": "get_pet"},
      "put": {"operationId": "update_pet", "requestBody": {"required": true, "content": {"application/json": {"schema": {"$ref": "#/components/schemas/Pet"}}}}}
    },
    "/raw": {"post": {"requestBody": {"content": {"application/json": {"schema": {"type": "array", "items": {"type": "string"}}}}}}},
    "/clash/{name}": {"post": {"operationId": "clash", "parameters": [{"name": "name", "in": "path", "schema": {"type": "string"}}],
      "requestBody": {"content": {"application/json": {"schema": {"type": "object", "properties": {"name": {"type": "string"}}}}}}}},
    "/form": {"post": {"operationId": "form_only", "requestBody": {"content": {"multipart/form-data": {"schema": {"type": "object"}}}}}}
  },
  "components": {
    "parameters": {"PetId": {"name": "petId", "in": "path", "required": true, "schema": {"type": "integer"}}},
    "schemas": {
      "Pet": {"type": "object", "required": ["name"], "properties": {"name": {"type": "string"}, "owner": {"$ref": "#/components/schemas/Owner"}}},
      "Owner": {"type": "object", "properties": {"pets": {"type": "array", "items": {"$ref": "#/components/schemas/Pet"}}}}
    }
  }
}`

func TestConvertOpenAPI(t *testing.T) {
	specs, ops, err := ConvertOpenAPI([]byte(petSpec))
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]map[string]any{}
	for _, s := range specs {
		var p map[string]any
		if err := json.Unmarshal(s.Parameters, &p); err != nil {
			t.Fatal(err)
		}
		byName[s.Name] = p
		if s.Name == "search_kb" && s.Description != "Search the KB\nFull text." {
			t.Errorf("description %q", s.Description)
		}
	}
	for _, n := range []string{"search_kb", "get_pet", "update_pet", "post_raw", "clash", "form_only"} {
		if byName[n] == nil {
			t.Fatalf("missing %s in %v", n, byName)
		}
	}
	q := byName["search_kb"]["properties"].(map[string]any)["q"].(map[string]any)
	if q["description"] != "query text" || byName["search_kb"]["required"].([]any)[0] != "q" {
		t.Errorf("search_kb params %v", byName["search_kb"])
	}
	up := byName["update_pet"]
	props := up["properties"].(map[string]any)
	if props["petId"].(map[string]any)["type"] != "integer" || props["name"] == nil {
		t.Errorf("update_pet props %v", props)
	}
	// the Pet -> Owner -> Pet cycle is cut, not looped forever
	owner := props["owner"].(map[string]any)["properties"].(map[string]any)["pets"].(map[string]any)
	if owner["items"] == nil {
		t.Errorf("owner not resolved: %v", owner)
	}
	if req := strings.Join(toStrings(up["required"]), ","); req != "petId,name" {
		t.Errorf("update_pet required %s", req)
	}
	if !ops["post_raw"].BodyWhole || byName["post_raw"]["properties"].(map[string]any)["body"] == nil {
		t.Errorf("non-object body not passed whole: %+v", ops["post_raw"])
	}
	if !ops["clash"].BodyWhole {
		t.Errorf("clashing body should be whole: %+v", ops["clash"])
	}
	if ops["form_only"].Body {
		t.Errorf("form body should be skipped")
	}
}

func toStrings(v any) []string {
	var out []string
	arr, _ := v.([]any)
	for _, x := range arr {
		out = append(out, x.(string))
	}
	return out
}

func TestConvertOpenAPIYAML(t *testing.T) {
	specs, _, err := ConvertOpenAPI([]byte("openapi: 3.0.0\npaths:\n  /ping:\n    get:\n      operationId: ping\n"))
	if err != nil || len(specs) != 1 || specs[0].Name != "ping" {
		t.Fatalf("specs %+v err %v", specs, err)
	}
}

func TestOpenAPICall(t *testing.T) {
	var got *http.Request
	var body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/openapi.json":
			if r.Header.Get("Authorization") != "Bearer tok" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			_, _ = io.WriteString(w, petSpec)
		case "/pets/7":
			got = r.Clone(context.Background())
			b, _ := io.ReadAll(r.Body)
			body = string(b)
			_, _ = io.WriteString(w, `{"content":"saved Rex","parts":[{"type":"cards","items":[]}],"files":["f9"]}`)
		case "/search":
			got = r.Clone(context.Background())
			_, _ = io.WriteString(w, `[{"id":1}]`)
		case "/raw":
			w.WriteHeader(http.StatusUnprocessableEntity)
			_, _ = io.WriteString(w, `{"detail":"bad"}`)
		}
	}))
	defer srv.Close()
	s := &Server{m: &Manager{http: srv.Client()}, row: Row{ID: "pets", Kind: "openapi", URL: srv.URL + "/", SpecPath: "openapi.json",
		Auth: Auth{Type: "bearer", key: "tok"}}}
	c, err := s.listOpenAPI(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	var rec recorder
	res, err := s.callOpenAPI(context.Background(), rec.call("update_pet", `{"petId":7,"name":"Rex","extra":1}`), c.ops["update_pet"], "")
	if err != nil {
		t.Fatal(err)
	}
	if got.Method != http.MethodPut || body != `{"name":"Rex"}` || got.Header.Get("Authorization") != "Bearer tok" ||
		got.Header.Get("X-Kera-Chat-Id") != "chat-1" || got.Header.Get("Content-Type") != "application/json" {
		t.Fatalf("request %s %s %v", got.Method, body, got.Header)
	}
	if res.Content != "saved Rex" || len(res.Parts) != 1 || res.Files[0] != "f9" {
		t.Fatalf("result %+v", res)
	}
	res, _ = s.callOpenAPI(context.Background(), rec.call("search_kb", `{"q":"four eyes","tags":["a","b"],"limit":6}`), c.ops["search_kb"], "")
	if got.URL.RawQuery != "limit=6&q=four+eyes&tags=a&tags=b" || res.Content != `[{"id":1}]` {
		t.Fatalf("query %q content %q", got.URL.RawQuery, res.Content)
	}
	res, _ = s.callOpenAPI(context.Background(), rec.call("post_raw", `{"body":["x"]}`), c.ops["post_raw"], "")
	if !res.IsError || !strings.Contains(res.Content, "HTTP 422") {
		t.Fatalf("error result %+v", res)
	}
	if _, err := s.callOpenAPI(context.Background(), rec.call("update_pet", `{"petId":".."}`), c.ops["update_pet"], ""); err == nil {
		t.Fatal("a .. path parameter was sent")
	}
}
