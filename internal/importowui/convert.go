package importowui

import (
	"encoding/json"
	"html"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Part is one entry of message.parts. When a message has any parts, the list
// is its complete ordered rendering (text segments included); content always
// holds the plain markdown answer.
type Part = map[string]any

// Message is one converted chat message, ready for the message table and for
// shared-chat snapshots.
type Message struct {
	ID             string         `json:"id"`
	ParentID       string         `json:"parentId,omitempty"`
	Role           string         `json:"role"`
	Model          string         `json:"model,omitempty"`
	Content        string         `json:"content"`
	Parts          []Part         `json:"parts"`
	Files          []any          `json:"files"`
	Sources        []any          `json:"sources"`
	FollowUps      []any          `json:"followUps"`
	Usage          map[string]any `json:"usage"`
	Status         string         `json:"status"`
	Error          string         `json:"error,omitempty"`
	ContextSummary string         `json:"-"`
	CreatedAt      time.Time      `json:"-"`
	Timestamp      int64          `json:"createdAt"`
}

// Tree is a converted chat history.
type Tree struct {
	Messages  []Message // ordered by creation time
	CurrentID string    // active leaf
	NoRole    int       // source entries dropped for having no usable role
	Dangling  int       // messages whose parent was missing (made roots)
}

// srcMessage is one entry of the source history map. Fields whose JSON type
// varies are left as any/RawMessage and normalized below.
type srcMessage struct {
	ID             string           `json:"id"`
	ParentID       *string          `json:"parentId"`
	Role           string           `json:"role"`
	Content        any              `json:"content"`
	Model          string           `json:"model"`
	Timestamp      float64          `json:"timestamp"`
	Done           *bool            `json:"done"`
	Files          []any            `json:"files"`
	Sources        []any            `json:"sources"`
	StatusHistory  []map[string]any `json:"statusHistory"`
	Usage          map[string]any   `json:"usage"`
	Error          any              `json:"error"`
	FollowUps      []any            `json:"followUps"`
	Output         []map[string]any `json:"output"`
	ContextSummary any              `json:"contextSummary"`
}

// ConvertHistory turns a source chat blob ({history: {messages, currentId}})
// into message rows. outputs holds per-message output item arrays from the
// separate message table (keyed by message id); entries there win over an
// output array stored inside the blob. fallback is used for messages without
// a timestamp.
func ConvertHistory(blob []byte, outputs map[string][]map[string]any, fallback time.Time) (Tree, error) {
	var doc struct {
		History struct {
			Messages  map[string]json.RawMessage `json:"messages"`
			CurrentID string                     `json:"currentId"`
		} `json:"history"`
	}
	if len(blob) > 0 {
		if err := json.Unmarshal(blob, &doc); err != nil {
			return Tree{}, err
		}
	}
	var t Tree
	for key, raw := range doc.History.Messages {
		var sm srcMessage
		if err := json.Unmarshal(raw, &sm); err != nil {
			t.NoRole++ // ponytail: undecodable entries are counted with the role-less ones
			continue
		}
		if sm.Role != "user" && sm.Role != "assistant" && sm.Role != "system" {
			t.NoRole++
			continue
		}
		out := outputs[key]
		if len(out) == 0 {
			out = sm.Output
		}
		m := convertMessage(sm, out, fallback)
		m.ID = key // the map key is authoritative; a few entries lack an id field
		t.Messages = append(t.Messages, m)
	}
	sort.Slice(t.Messages, func(i, j int) bool {
		a, b := t.Messages[i], t.Messages[j]
		if !a.CreatedAt.Equal(b.CreatedAt) {
			return a.CreatedAt.Before(b.CreatedAt)
		}
		return a.ID < b.ID
	})
	known := make(map[string]bool, len(t.Messages))
	for _, m := range t.Messages {
		known[m.ID] = true
	}
	for i := range t.Messages {
		if p := t.Messages[i].ParentID; p != "" && !known[p] {
			t.Messages[i].ParentID = ""
			t.Dangling++
		}
	}
	// Corrupt parent cycles (a message that is its own ancestor) are cut at
	// the message that closes them, so every walk down the tree ends.
	parent := make(map[string]string, len(t.Messages))
	for _, m := range t.Messages {
		parent[m.ID] = m.ParentID
	}
	for i := range t.Messages {
		id := t.Messages[i].ID
		for p, n := parent[id], 0; p != "" && n <= len(t.Messages); p, n = parent[p], n+1 {
			if p == id {
				t.Messages[i].ParentID, parent[id] = "", ""
				t.Dangling++
				break
			}
		}
	}
	t.CurrentID = activeLeaf(t.Messages, doc.History.CurrentID)
	return t, nil
}

// activeLeaf returns want when it is a leaf; otherwise it walks down from want
// (or, when want is unknown, from the newest message) to the newest leaf.
func activeLeaf(msgs []Message, want string) string {
	if len(msgs) == 0 {
		return ""
	}
	children := map[string][]string{} // in creation order, since msgs is sorted
	known := map[string]bool{}
	for _, m := range msgs {
		known[m.ID] = true
		if m.ParentID != "" {
			children[m.ParentID] = append(children[m.ParentID], m.ID)
		}
	}
	cur := want
	if !known[cur] {
		cur = msgs[len(msgs)-1].ID
	}
	for len(children[cur]) > 0 {
		kids := children[cur]
		cur = kids[len(kids)-1]
	}
	return cur
}

func convertMessage(sm srcMessage, output []map[string]any, fallback time.Time) Message {
	m := Message{
		ID:        sm.ID,
		Role:      sm.Role,
		Model:     sm.Model,
		Files:     stripFileObjects(sm.Files),
		Sources:   orEmpty(sm.Sources),
		FollowUps: orEmpty(sm.FollowUps),
		Usage:     sm.Usage,
		Status:    "done",
		Parts:     []Part{},
	}
	if sm.ParentID != nil {
		m.ParentID = *sm.ParentID
	}
	if m.Usage == nil {
		m.Usage = map[string]any{}
	}
	if s, ok := sm.ContextSummary.(string); ok {
		m.ContextSummary = s
	}
	m.CreatedAt = fallback
	if sm.Timestamp > 0 {
		m.CreatedAt = epoch(int64(sm.Timestamp))
	}
	m.Timestamp = m.CreatedAt.Unix()
	if sm.Done != nil && !*sm.Done {
		m.Status = "stopped"
	}
	if msg, failed := errorText(sm.Error); failed {
		m.Status, m.Error = "error", msg
	}

	text, blockParts := SplitDetails(contentText(sm.Content))
	m.Content = text
	if op := OutputParts(output); len(op) > 0 {
		blockParts = op // the output array is the structured original of the details blocks
	}
	var parts []Part
	for _, s := range sm.StatusHistory {
		parts = append(parts, statusPart(s))
	}
	if len(parts)+len(blockParts) == 0 {
		return m
	}
	if !hasText(blockParts) && text != "" {
		blockParts = append(blockParts, Part{"type": "text", "text": text})
	}
	m.Parts = append(parts, blockParts...)
	return m
}

// contentText flattens content that is either a string or a list of
// {type:"text", text} items (multimodal user input).
func contentText(c any) string {
	switch v := c.(type) {
	case string:
		return v
	case []any:
		var sb []string
		for _, it := range v {
			if o, ok := it.(map[string]any); ok {
				if s, ok := o["text"].(string); ok {
					sb = append(sb, s)
				}
			}
		}
		return strings.Join(sb, "\n")
	}
	return ""
}

func errorText(e any) (string, bool) {
	switch v := e.(type) {
	case nil:
		return "", false
	case bool:
		return "", v
	case string:
		return v, v != ""
	case map[string]any:
		if s, ok := v["content"].(string); ok {
			return s, true
		}
		b, _ := json.Marshal(v)
		return string(b), true
	}
	return "", false
}

// stripFileObjects drops the embedded copy of the whole file row that some
// file references carry (it duplicates the file table, extracted text included).
func stripFileObjects(files []any) []any {
	out := make([]any, 0, len(files))
	for _, f := range files {
		if o, ok := f.(map[string]any); ok {
			if _, has := o["file"]; has {
				c := make(map[string]any, len(o))
				for k, v := range o {
					if k != "file" {
						c[k] = v
					}
				}
				f = c
			}
		}
		out = append(out, f)
	}
	return out
}

func orEmpty(v []any) []any {
	if v == nil {
		return []any{}
	}
	return v
}

func statusPart(s map[string]any) Part {
	p := Part{}
	for k, v := range s {
		p[k] = v
	}
	p["type"] = "status"
	return p
}

func hasText(parts []Part) bool {
	for _, p := range parts {
		if p["type"] == "text" {
			return true
		}
	}
	return false
}

// epoch converts source timestamps (seconds; milliseconds or nanoseconds are
// recognized by magnitude) to time.
func epoch(v int64) time.Time {
	switch {
	case v > 1e17:
		return time.Unix(0, v).UTC()
	case v > 1e14:
		return time.UnixMicro(v).UTC()
	case v > 1e11:
		return time.UnixMilli(v).UTC()
	}
	return time.Unix(v, 0).UTC()
}

var (
	detailsOpen = regexp.MustCompile(`<details\s+type="(reasoning|tool_calls)"([^>]*)>`)
	detailsTag  = regexp.MustCompile(`<details[\s>]|</details>`)
	attrRe      = regexp.MustCompile(`([A-Za-z_][\w-]*)="([^"]*)"`)
	summaryRe   = regexp.MustCompile(`(?s)^\s*<summary>.*?</summary>`)
)

// SplitDetails removes reasoning and tool-call <details> blocks from
// assistant markdown. It returns the remaining text and, when at least one
// block was found, the ordered parts (text segments, reasoning, tool_call and
// tool_result). Other <details> blocks and unterminated ones stay in the text.
func SplitDetails(content string) (string, []Part) {
	var parts []Part
	var texts []string
	found := false
	addText := func(s string) {
		if s = strings.TrimSpace(s); s != "" {
			texts = append(texts, s)
			parts = append(parts, Part{"type": "text", "text": s})
		}
	}
	rest := content
	for {
		loc := detailsOpen.FindStringSubmatchIndex(rest)
		if loc == nil {
			break
		}
		bodyStart := loc[1]
		closeAt, closeEnd := matchingClose(rest[bodyStart:])
		if closeAt < 0 {
			break
		}
		found = true
		addText(rest[:loc[0]])
		kind := rest[loc[2]:loc[3]]
		attrs := parseAttrs(rest[loc[4]:loc[5]])
		body := rest[bodyStart : bodyStart+closeAt]
		if kind == "reasoning" {
			parts = append(parts, reasoningPart(attrs, body))
		} else {
			parts = append(parts, toolParts(attrs)...)
		}
		rest = rest[bodyStart+closeEnd:]
	}
	if !found {
		return content, nil
	}
	addText(rest)
	return strings.Join(texts, "\n\n"), parts
}

// matchingClose finds the </details> that closes a block whose body starts at
// s[0], allowing nested <details>. It returns the start and end offsets of
// that closing tag, or -1.
func matchingClose(s string) (int, int) {
	depth := 1
	for _, m := range detailsTag.FindAllStringIndex(s, -1) {
		if s[m[0]+1] == '/' {
			depth--
			if depth == 0 {
				return m[0], m[1]
			}
		} else {
			depth++
		}
	}
	return -1, -1
}

func parseAttrs(s string) map[string]string {
	out := map[string]string{}
	for _, m := range attrRe.FindAllStringSubmatch(s, -1) {
		out[m[1]] = html.UnescapeString(m[2])
	}
	return out
}

func reasoningPart(attrs map[string]string, body string) Part {
	body = summaryRe.ReplaceAllString(body, "")
	lines := strings.Split(strings.Trim(body, "\n"), "\n")
	for i, l := range lines {
		if s, ok := strings.CutPrefix(l, "> "); ok {
			lines[i] = s
		} else {
			lines[i] = strings.TrimPrefix(l, ">")
		}
	}
	p := Part{"type": "reasoning", "text": strings.TrimSpace(strings.Join(lines, "\n")), "done": attrs["done"] != "false"}
	if d, err := strconv.ParseFloat(attrs["duration"], 64); err == nil {
		p["durationMs"] = int64(d * 1000)
	}
	return p
}

func toolParts(attrs map[string]string) []Part {
	call := Part{"type": "tool_call", "callId": attrs["id"], "name": attrs["name"], "arguments": jsonText(attrs["arguments"])}
	_, hasResult := attrs["result"]
	if !hasResult && attrs["done"] != "true" {
		return []Part{call}
	}
	res := Part{"type": "tool_result", "callId": attrs["id"], "name": attrs["name"], "result": jsonText(attrs["result"])}
	for _, k := range []string{"files", "embeds"} {
		var v []any
		if json.Unmarshal([]byte(jsonText(attrs[k])), &v) == nil && len(v) > 0 {
			res[k] = v
		}
	}
	return []Part{call, res}
}

// jsonText unwraps an attribute that holds a JSON-encoded string (the source
// double-encodes arguments and results); other values are returned as is.
func jsonText(s string) string {
	var inner string
	if json.Unmarshal([]byte(s), &inner) == nil {
		return inner
	}
	return s
}

// OutputParts converts an output item array (the structured message format
// of newer source versions) into parts.
func OutputParts(items []map[string]any) []Part {
	var parts []Part
	names := map[string]string{}
	for _, it := range items {
		typ, _ := it["type"].(string)
		switch {
		case typ == "message":
			if t := itemText(it["content"]); t != "" {
				parts = append(parts, Part{"type": "text", "text": t})
			}
		case typ == "reasoning":
			t := itemText(it["content"])
			if t == "" {
				t = itemText(it["summary"])
			}
			p := Part{"type": "reasoning", "text": t, "done": true}
			if d, ok := it["duration"].(float64); ok {
				p["durationMs"] = int64(d * 1000)
			}
			parts = append(parts, p)
		case typ == "function_call":
			id, _ := it["call_id"].(string)
			name, _ := it["name"].(string)
			names[id] = name
			p := Part{"type": "tool_call", "callId": id, "name": name, "arguments": stringify(it["arguments"])}
			if s, ok := it["status"].(string); ok {
				p["status"] = s
			}
			if d, ok := it["duration"].(float64); ok {
				p["durationMs"] = int64(d * 1000)
			}
			parts = append(parts, p)
		case typ == "function_call_output":
			id, _ := it["call_id"].(string)
			result := itemText(it["output"])
			if result == "" {
				result = stringify(it["output"])
			}
			p := Part{"type": "tool_result", "callId": id, "name": names[id], "result": result}
			if s, ok := it["status"].(string); ok {
				p["status"] = s
			}
			parts = append(parts, p)
		case typ == "web_search_call":
			p := Part{"type": "status", "action": "web_search", "done": true}
			if a, ok := it["action"].(map[string]any); ok {
				for _, k := range []string{"query", "queries", "url"} {
					if v, ok := a[k]; ok {
						p[k] = v
					}
				}
			}
			parts = append(parts, p)
		case strings.HasSuffix(typ, "context_compaction"):
			s, _ := it["summary"].(string)
			parts = append(parts, Part{"type": "compaction", "summary": s})
		}
		// ponytail: unknown item types are dropped; add a mapping when one shows up.
	}
	return parts
}

// itemText joins the text of [{type, text}] arrays; a plain string is returned as is.
func itemText(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case []any:
		var out []string
		for _, e := range x {
			if o, ok := e.(map[string]any); ok {
				if s, ok := o["text"].(string); ok && s != "" {
					out = append(out, s)
				}
			}
		}
		return strings.Join(out, "\n")
	}
	return ""
}

func stringify(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	}
	b, _ := json.Marshal(v)
	return string(b)
}
