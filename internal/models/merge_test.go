package models

import (
	"testing"
	"time"
)

func str(s string) *string { return &s }

func TestMergePresets(t *testing.T) {
	ol := Connection{ID: "ol", Kind: "ollama"}
	web := Connection{ID: "web", Kind: "responses", Prefix: "web"}
	bs := []baseModel{{conn: ol, providerID: "qwen"}, {conn: web, providerID: "gpt"}, {conn: ol, providerID: "gone"}}
	rows := []row{
		// base override: hides qwen, sets params
		{ID: "qwen", Name: "Qwen", Hidden: true, Active: true, Params: map[string]any{"num_ctx": 65536, "think": true}, SystemPrompt: "base prompt"},
		// preset -> preset -> qwen
		{ID: "local", BaseModelID: str("qwen"), Active: true, Params: map[string]any{"think": false}},
		{ID: "finance", BaseModelID: str("local"), Name: "Finance", Active: true, SystemPrompt: "finance prompt", Params: map[string]any{"temperature": 0.1}},
		{ID: "deeper", BaseModelID: str("finance"), Active: true},
		{ID: "too-deep", BaseModelID: str("deeper"), Active: true},
		{ID: "kera", BaseModelID: str("web.gpt"), Active: true},
		{ID: "orphan", BaseModelID: str("missing"), Active: true},
		{ID: "loop-a", BaseModelID: str("loop-b"), Active: true},
		{ID: "loop-b", BaseModelID: str("loop-a"), Active: true},
		{ID: "gone", Active: false, CreatedAt: time.Unix(5, 0)},
	}
	all := merge(bs, rows)

	q := all["qwen"]
	if q.Name != "Qwen" || !q.Hidden || !q.Local || q.ProviderModel != "qwen" || !q.hasRow {
		t.Fatalf("qwen %+v", q)
	}
	l := all["local"]
	if l.Name != "local" || l.Params["think"] != false || l.Params["num_ctx"] != 65536 || l.SystemPrompt != "base prompt" ||
		l.Connection.ID != "ol" || l.ProviderModel != "qwen" || !l.Local || l.Hidden || l.BaseModelID != "qwen" {
		t.Fatalf("local %+v", l)
	}
	f := all["finance"]
	if f.SystemPrompt != "finance prompt" || f.Params["temperature"] != 0.1 || f.Params["think"] != false || f.Params["num_ctx"] != 65536 {
		t.Fatalf("finance %+v", f)
	}
	if q.Params["think"] != true {
		t.Fatal("preset mutated its base's params")
	}
	if all["deeper"] == nil || all["deeper"].SystemPrompt != "finance prompt" {
		t.Fatalf("3-deep preset missing: %+v", all["deeper"])
	}
	for _, id := range []string{"too-deep", "orphan", "loop-a", "loop-b"} {
		if all[id] != nil {
			t.Errorf("%s should not resolve", id)
		}
	}
	k := all["kera"]
	if k == nil || k.ProviderModel != "gpt" || k.Local || k.Connection.Prefix != "web" {
		t.Fatalf("kera %+v", k)
	}
	if all["web.gpt"] == nil || all["web.gpt"].hasRow {
		t.Fatal("prefixed base missing")
	}
	if all["gone"].active {
		t.Fatal("inactive row should mark the model inactive")
	}
}

func TestToolIDsRoundTrip(t *testing.T) {
	tl := Tools{Servers: []string{"kb"}, Builtins: map[string]bool{"ask_user": false, "execute_code": true}}
	ids := tl.IDs()
	if len(ids) != 2 || ids[0] != "server:kb" || ids[1] != "builtin:execute_code" {
		t.Fatalf("ids %v", ids)
	}
	tl.setIDs(ids)
	if len(tl.Servers) != 1 || tl.Builtins["ask_user"] || !tl.Builtins["execute_code"] || len(tl.Builtins) != 2 {
		t.Fatalf("round trip %+v", tl)
	}
	tl.setIDs([]string{"other", "builtin:search_web"})
	if tl.Servers[0] != "other" || tl.Builtins["execute_code"] || !tl.Builtins["search_web"] {
		t.Fatalf("edit %+v", tl)
	}
	if got := enabledFeatures(map[string]any{"web_search": "native", "memory": false, "code_interpreter": true, "x": ""}); len(got) != 2 || got[0] != "code_interpreter" {
		t.Fatalf("features %v", got)
	}
}

func TestSortModels(t *testing.T) {
	ms := []*Resolved{{ID: "c", Name: "beta"}, {ID: "a", Name: "Alpha"}, {ID: "z", Name: "zed"}, {ID: "k", Name: "Kera"}}
	sortModels(ms, []string{"z", "k"})
	got := ""
	for _, m := range ms {
		got += m.ID
	}
	if got != "zkac" {
		t.Fatalf("order %s", got)
	}
}
