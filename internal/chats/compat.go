package chats

import (
	"cmp"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"
)

// Entry is one message as the chat JSON renders it. It is built from a
// message row or from a share snapshot (same JSON keys as the importer's).
type Entry struct {
	ID           string          `json:"id"`
	ParentID     string          `json:"parentId,omitempty"`
	Role         string          `json:"role"`
	Model        string          `json:"model,omitempty"`
	Content      string          `json:"content"`
	Parts        json.RawMessage `json:"parts"`
	Files        json.RawMessage `json:"files"`
	Sources      json.RawMessage `json:"sources"`
	PendingCalls json.RawMessage `json:"pendingCalls,omitempty"`
	FollowUps    json.RawMessage `json:"followUps"`
	Usage        json.RawMessage `json:"usage"`
	Status       string          `json:"status"`
	Error        string          `json:"error,omitempty"`
	Created      int64           `json:"createdAt"` // epoch seconds
	createdAt    time.Time
	seq          int64
}

// EntryOf converts a message row.
func EntryOf(m Message) Entry {
	e := Entry{ID: m.ID, Role: m.Role, Model: m.ModelID, Content: m.Content, Parts: m.Parts, Files: m.Files,
		Sources: m.Sources, PendingCalls: m.PendingCalls, FollowUps: m.FollowUps, Usage: m.Usage, Status: m.Status,
		Error: m.Error, Created: m.CreatedAt.Unix(), createdAt: m.CreatedAt}
	if m.ParentID != nil {
		e.ParentID = *m.ParentID
	}
	return e
}

// Live is the in-flight state of a streaming message (from the engine's run).
type Live struct {
	Parts, Files, Sources, PendingCalls any
	Status                              string
	Seq                                 int64
}

// Overlay applies live state to a streaming entry.
func (e *Entry) Overlay(l Live) {
	e.Parts, e.Files, e.Sources, e.PendingCalls = mustJSON(l.Parts, "[]"), mustJSON(l.Files, "[]"), mustJSON(l.Sources, "[]"), mustJSON(l.PendingCalls, "[]")
	e.Status, e.seq = l.Status, l.Seq
}

func mustJSON(v any, empty string) json.RawMessage { return jsonOr(v, empty) }

// History renders entries as Open WebUI's history.messages map (with
// childrenIds) plus Kera's streamed fields, and the active branch as a list.
func History(entries []Entry, currentID string) (map[string]any, []map[string]any, string) {
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].created().Before(entries[j].created()) })
	children := map[string][]string{}
	byID := map[string]Entry{}
	for _, e := range entries {
		byID[e.ID] = e
		if e.ParentID != "" {
			children[e.ParentID] = append(children[e.ParentID], e.ID)
		}
	}
	if _, ok := byID[currentID]; !ok {
		currentID = ""
		if len(entries) > 0 {
			currentID = entries[len(entries)-1].ID
		}
	}
	out := make(map[string]any, len(entries))
	for _, e := range entries {
		out[e.ID] = entryJSON(e, orStrings(children[e.ID]))
	}
	var branch []map[string]any
	seen := map[string]bool{}
	for id := currentID; id != "" && !seen[id]; id = byID[id].ParentID {
		seen[id] = true
		if _, ok := byID[id]; !ok {
			break
		}
		branch = append([]map[string]any{out[id].(map[string]any)}, branch...)
	}
	if branch == nil {
		branch = []map[string]any{}
	}
	return out, branch, currentID
}

func (e Entry) created() time.Time {
	if !e.createdAt.IsZero() {
		return e.createdAt
	}
	return time.Unix(e.Created, 0)
}

func entryJSON(e Entry, children []string) map[string]any {
	var parent any
	if e.ParentID != "" {
		parent = e.ParentID
	}
	status := e.Status
	if status == "" {
		status = "done"
	}
	m := map[string]any{
		"id": e.ID, "parentId": parent, "childrenIds": children, "role": e.Role, "content": e.Content,
		"timestamp": e.created().Unix(), "createdAt": e.created().UnixMilli(), "done": status != "streaming",
		"status": status, "parts": UIParts(e.Parts), "files": raw(e.Files, "[]"), "sources": UISources(e.Sources),
		"pendingCalls": raw(e.PendingCalls, "[]"), "followUps": raw(e.FollowUps, "[]"), "usage": raw(e.Usage, "{}"),
		"statusHistory": []any{},
	}
	if e.Model != "" {
		m["model"] = e.Model
	}
	if e.Error != "" {
		m["error"] = map[string]any{"message": e.Error, "content": e.Error}
	}
	if e.seq > 0 {
		m["seq"] = e.seq
	}
	return m
}

func raw(b json.RawMessage, empty string) json.RawMessage {
	if len(b) == 0 || string(b) == "null" {
		return json.RawMessage(empty)
	}
	return b
}

func orStrings(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// ChatJSON is the Open WebUI-compatible chat object.
func ChatJSON(c Chat, entries []Entry, tags []string) map[string]any {
	cur := ""
	if c.CurrentMessageID != nil {
		cur = *c.CurrentMessageID
	}
	hist, branch, cur := History(entries, cur)
	tags = orStrings(tags)
	var curID any
	if cur != "" {
		curID = cur
	}
	return map[string]any{
		"id": c.ID, "user_id": c.UserID, "title": c.Title, "folder_id": c.FolderID, "share_id": c.ShareID,
		"archived": c.Archived, "pinned": c.Pinned, "created_at": c.CreatedAt.Unix(), "updated_at": c.UpdatedAt.Unix(),
		"meta": map[string]any{"tags": tags}, "tags": tags, "mode": cmp.Or(c.Mode, "chat"),
		"chat": map[string]any{
			"id": c.ID, "title": c.Title, "models": orStrings(c.ModelIDs), "tags": tags, "params": map[string]any{},
			"history":   map[string]any{"currentId": curID, "messages": hist},
			"messages":  branch,
			"timestamp": c.UpdatedAt.UnixMilli(),
		},
	}
}

// ---- normalisation of imported shapes ----------------------------------------------

// UIParts returns parts in the shape the web app renders. Parts written by
// the engine pass through; imported tool_call / tool_result / status parts
// become grouped "steps" parts.
func UIParts(b json.RawMessage) json.RawMessage {
	b = raw(b, "[]")
	var parts []map[string]any
	if json.Unmarshal(b, &parts) != nil {
		return json.RawMessage("[]")
	}
	legacy := false
	for _, p := range parts {
		switch p["type"] {
		case "tool_call", "tool_result", "status":
			legacy = true
		}
	}
	if !legacy {
		return b
	}
	out, err := json.Marshal(convertParts(parts))
	if err != nil {
		return json.RawMessage("[]")
	}
	return out
}

func convertParts(parts []map[string]any) []map[string]any {
	var out []map[string]any
	var steps []map[string]any // current group
	flush := func() {
		if len(steps) > 0 {
			out = append(out, map[string]any{"type": "steps", "steps": steps})
			steps = nil
		}
	}
	find := func(id string) map[string]any {
		lists := [][]map[string]any{steps}
		for _, g := range out {
			if l, ok := g["steps"].([]map[string]any); ok {
				lists = append(lists, l)
			}
		}
		for _, l := range lists {
			for _, s := range l {
				if s["callId"] == id {
					return s
				}
			}
		}
		return nil
	}
	for i, p := range parts {
		switch p["type"] {
		case "tool_call":
			steps = append(steps, map[string]any{"callId": str(p["callId"]), "name": str(p["name"]),
				"args": jsonish(p["arguments"]), "status": "done", "statusLines": []any{}})
		case "tool_result":
			s := find(str(p["callId"]))
			if s == nil {
				s = map[string]any{"callId": str(p["callId"]), "name": str(p["name"]), "status": "done", "statusLines": []any{}}
				steps = append(steps, s)
			}
			s["output"] = clip(str(p["result"]), 4000)
			if p["status"] == "failed" || p["status"] == "error" {
				s["status"] = "error"
			}
		case "status":
			action := str(p["action"])
			if action == "" {
				action = "status"
			}
			s := map[string]any{"callId": fmt.Sprintf("status-%d", i), "name": action, "status": "done", "statusLines": []any{}}
			if d := str(p["description"]); d != "" {
				s["statusLines"] = []any{map[string]any{"description": d, "done": true, "action": action}}
			}
			if q := p["query"]; q != nil {
				s["args"] = map[string]any{"query": q}
			}
			if urls := urlList(p["urls"]); len(urls) > 0 {
				s["urls"] = urls
			}
			steps = append(steps, s)
		default:
			flush()
			out = append(out, p)
		}
	}
	flush()
	if out == nil {
		out = []map[string]any{}
	}
	return out
}

// UISources flattens imported nested citations ({source, document[],
// metadata[]}) into one entry per document; Kera's own sources pass through.
func UISources(b json.RawMessage) json.RawMessage {
	b = raw(b, "[]")
	var list []map[string]any
	if json.Unmarshal(b, &list) != nil {
		return json.RawMessage("[]")
	}
	nested := false
	for _, s := range list {
		if _, ok := s["document"]; ok {
			nested = true
		}
	}
	if !nested {
		return b
	}
	var out []map[string]any
	seen := map[string]bool{}
	for _, s := range list {
		src, _ := s["source"].(map[string]any)
		docs, _ := s["document"].([]any)
		metas, _ := s["metadata"].([]any)
		for i := range max(len(docs), 1) {
			e := map[string]any{"name": str(src["name"])}
			if u := str(src["url"]); isURL(u) {
				e["url"] = u
			}
			if i < len(docs) {
				e["snippet"] = clip(str(docs[i]), 300)
			}
			if i < len(metas) {
				if md, ok := metas[i].(map[string]any); ok {
					if n := str(md["name"]); n != "" {
						e["name"] = n
					}
					if u := str(md["source"]); isURL(u) {
						e["url"] = u
					}
					if f := str(md["file_id"]); f != "" {
						e["fileId"] = f
					}
					if p, ok := md["page"].(float64); ok {
						e["page"] = int(p) + 1
					}
				}
			}
			key := str(e["url"]) + "|" + str(e["fileId"]) + "|" + str(e["name"])
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, e)
		}
	}
	res, err := json.Marshal(out)
	if err != nil || out == nil {
		return json.RawMessage("[]")
	}
	return res
}

func isURL(s string) bool {
	u, err := url.Parse(s)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}

func urlList(v any) []string {
	list, _ := v.([]any)
	var out []string
	for _, x := range list {
		if s := str(x); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func str(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	}
	b, _ := json.Marshal(v)
	return string(b)
}

// jsonish parses a JSON-encoded argument string; other text stays a string.
func jsonish(v any) any {
	s, ok := v.(string)
	if !ok {
		return v
	}
	var out any
	if json.Unmarshal([]byte(s), &out) == nil {
		return out
	}
	return s
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && !utf8Start(s[cut]) {
		cut--
	}
	return strings.TrimSpace(s[:cut]) + "…"
}

func utf8Start(b byte) bool { return b&0xC0 != 0x80 }
