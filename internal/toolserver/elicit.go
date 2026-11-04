package toolserver

import (
	"bytes"
	"context"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/cdmx-in/kera/internal/tools"
)

// MCP elicitation (form mode) -> Call.Ask. A schema without properties is a
// yes/no confirmation; otherwise every property becomes one question
// (enum -> options, boolean -> Yes/No, free text -> "Other" only).

type elicitParams struct {
	Mode            string `json:"mode"`
	Message         string `json:"message"`
	RequestedSchema struct {
		Properties json.RawMessage `json:"properties"`
		Required   []string        `json:"required"`
	} `json:"requestedSchema"`
}

type choice struct {
	Const string `json:"const"`
	Title string `json:"title"`
}

type propSchema struct {
	Type        string   `json:"type"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Enum        []string `json:"enum"`
	EnumNames   []string `json:"enumNames"`
	OneOf       []choice `json:"oneOf"`
	Items       *struct {
		Enum  []string `json:"enum"`
		AnyOf []choice `json:"anyOf"`
	} `json:"items"`
}

// choices returns (value, label) pairs of an enum-like property.
func (p propSchema) choices() []choice {
	var out []choice
	add := func(vals []string, names []string) {
		for i, v := range vals {
			c := choice{Const: v, Title: v}
			if i < len(names) && names[i] != "" {
				c.Title = names[i]
			}
			out = append(out, c)
		}
	}
	switch {
	case len(p.OneOf) > 0:
		out = p.OneOf
	case len(p.Enum) > 0:
		add(p.Enum, p.EnumNames)
	case p.Items != nil && len(p.Items.AnyOf) > 0:
		out = p.Items.AnyOf
	case p.Items != nil:
		add(p.Items.Enum, nil)
	case p.Type == "boolean":
		out = []choice{{Const: "true", Title: "Yes"}, {Const: "false", Title: "No"}}
	}
	for i := range out {
		if out[i].Title == "" {
			out[i].Title = out[i].Const
		}
	}
	return out
}

type askQuestion struct {
	Question    string      `json:"question"`
	Header      string      `json:"header,omitempty"`
	MultiSelect bool        `json:"multiSelect,omitempty"`
	AllowOther  bool        `json:"allowOther,omitempty"`
	Options     []askOption `json:"options"`
}

type askOption struct {
	Label string `json:"label"`
}

func (s *Server) elicit(ctx context.Context, c tools.Call, raw json.RawMessage) map[string]any {
	var p elicitParams
	if json.Unmarshal(raw, &p) != nil || (p.Mode != "" && p.Mode != "form") {
		return map[string]any{"action": "decline"} // only form mode is declared
	}
	names := orderedKeys(p.RequestedSchema.Properties)
	if len(names) == 0 {
		ans, err := c.Ask(ctx, tools.AskRequest{Kind: "confirm", Title: s.label(), Message: p.Message})
		if err != nil || isNull(ans) {
			return map[string]any{"action": "cancel"}
		}
		var yes bool
		if json.Unmarshal(ans, &yes) == nil && !yes {
			return map[string]any{"action": "decline"}
		}
		return map[string]any{"action": "accept", "content": map[string]any{}}
	}
	var props map[string]propSchema
	_ = json.Unmarshal(p.RequestedSchema.Properties, &props)
	qs := make([]askQuestion, len(names))
	kind := "choice"
	for i, n := range names {
		ps := props[n]
		q := askQuestion{Question: firstNonEmpty(ps.Description, ps.Title, n), Header: firstNonEmpty(ps.Title, n),
			MultiSelect: ps.Type == "array", Options: []askOption{}}
		for _, ch := range ps.choices() {
			q.Options = append(q.Options, askOption{Label: ch.Title})
		}
		if len(q.Options) == 0 {
			q.AllowOther, kind = true, "input"
		}
		qs[i] = q
	}
	ans, err := c.Ask(ctx, tools.AskRequest{Kind: kind, Title: s.label(), Message: p.Message, Questions: mustJSON(qs)})
	if err != nil || isNull(ans) {
		return map[string]any{"action": "cancel"}
	}
	content := map[string]any{}
	for i, n := range names {
		vals := answerFor(ans, i, n, qs[i].Question)
		if v, ok := convert(props[n], vals); ok {
			content[n] = v
		}
	}
	for _, r := range p.RequestedSchema.Required {
		if _, ok := content[r]; !ok {
			return map[string]any{"action": "decline"}
		}
	}
	return map[string]any{"action": "accept", "content": content}
}

// answerFor reads the answers to question i from the ask_user reply
// ([{question, answers: [...]}, ...]) or from a plain {name: value} object.
func answerFor(ans json.RawMessage, i int, name, question string) []string {
	var list []struct {
		Question string   `json:"question"`
		Answers  []string `json:"answers"`
	}
	if json.Unmarshal(ans, &list) == nil {
		for _, a := range list {
			if a.Question == question {
				return a.Answers
			}
		}
		if i < len(list) {
			return list[i].Answers
		}
		return nil
	}
	var obj map[string]any
	if json.Unmarshal(ans, &obj) == nil {
		switch v := obj[name].(type) {
		case nil:
			return nil
		case []any:
			out := make([]string, 0, len(v))
			for _, x := range v {
				out = append(out, toString(x))
			}
			return out
		default:
			return []string{toString(v)}
		}
	}
	return nil
}

func toString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	b, _ := json.Marshal(v)
	return string(b)
}

// convert maps answer labels back to values of the property's type.
func convert(p propSchema, vals []string) (any, bool) {
	chs := p.choices()
	value := func(label string) string {
		for _, c := range chs {
			if c.Title == label || c.Const == label {
				return c.Const
			}
		}
		return label
	}
	if len(vals) == 0 {
		return nil, false
	}
	if p.Type == "array" {
		out := make([]string, len(vals))
		for i, v := range vals {
			out[i] = value(v)
		}
		return out, true
	}
	v := strings.TrimSpace(value(vals[0]))
	switch p.Type {
	case "integer":
		n, err := strconv.ParseInt(v, 10, 64)
		return n, err == nil
	case "number":
		f, err := strconv.ParseFloat(v, 64)
		return f, err == nil
	case "boolean":
		switch strings.ToLower(v) {
		case "true", "yes", "1":
			return true, true
		case "false", "no", "0":
			return false, true
		}
		return nil, false
	}
	return v, v != ""
}

// orderedKeys returns an object's keys in document order (forms keep the
// server's field order).
func orderedKeys(raw json.RawMessage) []string {
	dec := json.NewDecoder(bytes.NewReader(raw))
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return nil
	}
	var keys []string
	for dec.More() {
		t, err := dec.Token()
		if err != nil {
			return keys
		}
		k, _ := t.(string)
		keys = append(keys, k)
		var skip json.RawMessage
		if dec.Decode(&skip) != nil {
			return keys
		}
	}
	return keys
}

func isNull(b json.RawMessage) bool {
	t := bytes.TrimSpace(b)
	return len(t) == 0 || string(t) == "null"
}

func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if strings.TrimSpace(s) != "" {
			return s
		}
	}
	return ""
}
