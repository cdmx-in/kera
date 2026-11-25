package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/cdmx-in/kera/internal/auth"
	"github.com/cdmx-in/kera/internal/chats"
	"github.com/cdmx-in/kera/internal/files"
	"github.com/cdmx-in/kera/internal/tools"
)

// Built-in tool sets: builtin:files (read the conversation's attachments)
// and builtin:core (time, questions to the user, chat search).

func spec(name, desc, params string) tools.Spec {
	return tools.Spec{Name: name, Description: desc, Parameters: json.RawMessage(params)}
}

func textResult(format string, a ...any) tools.Result {
	return tools.Result{Content: fmt.Sprintf(format, a...)}
}

func errResult(format string, a ...any) tools.Result {
	return tools.Result{Content: "Error: " + fmt.Sprintf(format, a...), IsError: true}
}

// ---- builtin:files ------------------------------------------------------------------

// maxViewChars is where view_file stops adding lines to one page (the page then says where to continue).
const maxViewChars = 30000

type filesTools struct{ e *Engine }

func (t *filesTools) ID() string { return "builtin:files" }

func (t *filesTools) Tools(context.Context, *auth.User) ([]tools.Spec, error) {
	return []tools.Spec{
		spec("list_chat_files", "List the files attached to this conversation with their id, type and size.",
			`{"type":"object","properties":{}}`),
		spec("view_file", "Read an attached file's extracted text, a page of lines at a time. Start at line 1 and continue with start_line until you have what you need.",
			`{"type":"object","properties":{"file":{"type":"string","description":"file id or file name"},"start_line":{"type":"integer","description":"first line to show (1-based)","default":1},"max_lines":{"type":"integer","description":"lines to show, at most 500","default":200}},"required":["file"]}`),
		spec("grep_chat_files", "Search the attached files for lines matching a pattern (case-insensitive regular expression or plain text).",
			`{"type":"object","properties":{"pattern":{"type":"string"},"file":{"type":"string","description":"limit to one file (id or name)"},"context":{"type":"integer","description":"lines of context around each match, 0-3","default":0},"max_results":{"type":"integer","default":50}},"required":["pattern"]}`),
	}, nil
}

// chatFiles are the conversation's files the caller may read.
func (t *filesTools) chatFiles(ctx context.Context, c tools.Call) []files.File {
	ids := append([]string{}, c.FileIDs...)
	if c.ChatID != "" && !strings.HasPrefix(c.ChatID, "temp:") {
		rows, err := t.e.DB.Query(ctx, `SELECT file_id FROM chat_file WHERE chat_id = $1`, c.ChatID)
		if err == nil {
			for rows.Next() {
				var id string
				if rows.Scan(&id) == nil && !containsStr(ids, id) {
					ids = append(ids, id)
				}
			}
			rows.Close()
		}
	}
	var out []files.File
	for _, id := range ids {
		if ok, err := t.e.Files.CanRead(ctx, c.User, id); err != nil || !ok {
			continue
		}
		if f, err := t.e.Files.Get(ctx, id); err == nil {
			out = append(out, f)
		}
	}
	return out
}

func (t *filesTools) find(ctx context.Context, c tools.Call, key string) (files.File, bool) {
	key = strings.TrimSpace(key)
	for _, f := range t.chatFiles(ctx, c) {
		if f.ID == key || strings.EqualFold(f.Filename, key) {
			return f, true
		}
	}
	return files.File{}, false
}

func (t *filesTools) lines(ctx context.Context, f files.File) ([]string, error) {
	if !f.Done() {
		var err error
		if f, err = t.e.Files.Wait(ctx, f.ID, fileWait); err != nil {
			return nil, fmt.Errorf("%s is still being processed", f.Filename)
		}
	}
	if f.Status == files.Failed {
		return nil, fmt.Errorf("the text of %s could not be extracted: %s", f.Filename, f.Error)
	}
	text, err := t.e.Files.Text(ctx, f.ID)
	if err != nil {
		return nil, err
	}
	return strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n"), nil
}

func (t *filesTools) Execute(ctx context.Context, c tools.Call) (tools.Result, error) {
	var a struct {
		File       string `json:"file"`
		FileID     string `json:"file_id"`
		StartLine  int    `json:"start_line"`
		MaxLines   int    `json:"max_lines"`
		Pattern    string `json:"pattern"`
		Context    int    `json:"context"`
		MaxResults int    `json:"max_results"`
	}
	if len(c.Args) > 0 && json.Unmarshal(c.Args, &a) != nil {
		return errResult("arguments are not valid JSON"), nil
	}
	a.File = coalesce(a.File, a.FileID)
	switch c.Name {
	case "list_chat_files":
		fs := t.chatFiles(ctx, c)
		if len(fs) == 0 {
			return textResult("No files are attached to this conversation."), nil
		}
		var b strings.Builder
		for _, f := range fs {
			fmt.Fprintf(&b, "%s\n", fileLine(f))
		}
		return textResult("%s", b.String()), nil
	case "view_file":
		f, ok := t.find(ctx, c, a.File)
		if !ok {
			return errResult("no attached file %q; call list_chat_files", a.File), nil
		}
		lines, err := t.lines(ctx, f)
		if err != nil {
			return errResult("%v", err), nil
		}
		start := max(a.StartLine, 1)
		n := a.MaxLines
		if n <= 0 {
			n = 200
		}
		n = min(n, 500)
		if start > len(lines) {
			return textResult("%s has %d lines; start_line %d is past the end.", f.Filename, len(lines), start), nil
		}
		end := min(start+n-1, len(lines))
		// a page of very long lines must not fill the prompt: it stops at maxViewChars, or at the share of the window
		// the engine leaves this call (then never more than that, but at least one line)
		limit, strict := maxViewChars, false
		if c.MaxChars > 0 && c.MaxChars < limit {
			limit, strict = c.MaxChars, true
		}
		var body strings.Builder
		for i := start; i <= end; i++ {
			line := fmt.Sprintf("%d: %s\n", i, clip(lines[i-1], min(2000, limit)))
			if body.Len() >= limit || strict && i > start && body.Len()+len(line) > limit {
				end = i - 1
				break
			}
			body.WriteString(line)
		}
		var b strings.Builder
		fmt.Fprintf(&b, "%s, lines %d-%d of %d\n", f.Filename, start, end, len(lines))
		b.WriteString(body.String())
		if end < len(lines) {
			fmt.Fprintf(&b, "[more: continue with start_line %d]\n", end+1)
		}
		return textResult("%s", b.String()), nil
	case "grep_chat_files":
		return t.grep(ctx, c, a.Pattern, a.File, a.Context, a.MaxResults, c.MaxChars), nil
	}
	return errResult("unknown tool %s", c.Name), nil
}

// grep lists matching lines; maxChars (0 = no limit) is where it stops adding matches, with a line saying so.
func (t *filesTools) grep(ctx context.Context, c tools.Call, pattern, file string, around, limit, maxChars int) tools.Result {
	if strings.TrimSpace(pattern) == "" {
		return errResult("pattern is required")
	}
	re, err := regexp.Compile("(?i)" + pattern)
	if err != nil {
		re = regexp.MustCompile("(?i)" + regexp.QuoteMeta(pattern))
	}
	around = min(max(around, 0), 3)
	if limit <= 0 {
		limit = 50
	}
	limit = min(limit, 200)
	var b strings.Builder
	found := 0
	for _, f := range t.chatFiles(ctx, c) {
		if file != "" && f.ID != file && !strings.EqualFold(f.Filename, file) {
			continue
		}
		lines, err := t.lines(ctx, f)
		if err != nil {
			continue
		}
		for i, l := range lines {
			if !re.MatchString(l) {
				continue
			}
			if found++; found > limit {
				fmt.Fprintf(&b, "[stopped after %d matches; narrow the pattern]\n", limit)
				return textResult("%s", b.String())
			}
			var hit strings.Builder
			for j := max(i-around, 0); j <= min(i+around, len(lines)-1); j++ {
				fmt.Fprintf(&hit, "%s:%d: %s\n", f.Filename, j+1, clip(lines[j], 500))
			}
			if around > 0 {
				hit.WriteString("--\n")
			}
			if maxChars > 0 && b.Len() > 0 && b.Len()+hit.Len() > maxChars {
				fmt.Fprintf(&b, "[stopped after %d matches, the most that fits the context window now; narrow the pattern or search one file]\n", found-1)
				return textResult("%s", b.String())
			}
			b.WriteString(hit.String())
		}
	}
	if found == 0 {
		return textResult("No lines match %q.", pattern)
	}
	return textResult("%s", b.String())
}

// ---- builtin:core -------------------------------------------------------------------

type coreTools struct{ e *Engine }

func (t *coreTools) ID() string { return "builtin:core" }

func (t *coreTools) Tools(context.Context, *auth.User) ([]tools.Spec, error) {
	return []tools.Spec{
		spec("get_current_timestamp", "Get the current date and time in the user's time zone.", `{"type":"object","properties":{}}`),
		spec("ask_user", "Ask the user 1 to 3 multiple-choice questions when you need a decision you cannot make yourself. Give 2 or 3 short options per question; the user can also type their own answer. Wait for the answers before continuing.",
			`{"type":"object","properties":{"questions":{"type":"array","minItems":1,"maxItems":3,"items":{"type":"object","properties":{"question":{"type":"string"},"header":{"type":"string","description":"very short label, 1-3 words"},"multi_select":{"type":"boolean","default":false},"allow_other":{"type":"boolean","default":true},"options":{"type":"array","minItems":2,"maxItems":3,"items":{"type":"object","properties":{"label":{"type":"string"},"description":{"type":"string"},"recommended":{"type":"boolean"}},"required":["label"]}}},"required":["question","options"]}}},"required":["questions"]}`),
		spec("search_chats", "Search the user's earlier conversations for a word or phrase.",
			`{"type":"object","properties":{"query":{"type":"string"},"limit":{"type":"integer","default":5}},"required":["query"]}`),
	}, nil
}

func (t *coreTools) Execute(ctx context.Context, c tools.Call) (tools.Result, error) {
	switch c.Name {
	case "get_current_timestamp":
		p := t.e.profile(ctx, c.User)
		loc, err := time.LoadLocation(p.Timezone)
		if err != nil || p.Timezone == "" {
			loc = time.UTC
		}
		now := time.Now().In(loc)
		b, _ := json.Marshal(map[string]any{"iso": now.Format(time.RFC3339), "date": now.Format("2006-01-02"),
			"time": now.Format("15:04:05"), "weekday": now.Weekday().String(), "timezone": loc.String(), "unix": now.Unix()})
		return tools.Result{Content: string(b)}, nil
	case "ask_user":
		return t.askUser(ctx, c)
	case "search_chats":
		return t.searchChats(ctx, c)
	}
	return errResult("unknown tool %s", c.Name), nil
}

type askQuestion struct {
	Question    string `json:"question"`
	Header      string `json:"header,omitempty"`
	MultiSelect bool   `json:"multiSelect,omitempty"`
	AllowOther  bool   `json:"allowOther"`
	Options     []struct {
		Label       string `json:"label"`
		Description string `json:"description,omitempty"`
		Recommended bool   `json:"recommended,omitempty"`
	} `json:"options"`
}

// normalizeQuestions accepts snake_case or camelCase keys and returns the web app's shape.
func normalizeQuestions(args json.RawMessage) ([]askQuestion, error) {
	var a struct {
		Questions []map[string]json.RawMessage `json:"questions"`
	}
	if err := json.Unmarshal(args, &a); err != nil || len(a.Questions) == 0 {
		return nil, fmt.Errorf("questions are required")
	}
	var out []askQuestion
	for _, q := range a.Questions[:min(len(a.Questions), 3)] {
		var aq askQuestion
		aq.AllowOther = true
		_ = json.Unmarshal(q["question"], &aq.Question)
		_ = json.Unmarshal(q["header"], &aq.Header)
		_ = json.Unmarshal(q["options"], &aq.Options)
		for _, k := range []string{"multi_select", "multiSelect"} {
			if v, ok := q[k]; ok {
				_ = json.Unmarshal(v, &aq.MultiSelect)
			}
		}
		for _, k := range []string{"allow_other", "allowOther"} {
			if v, ok := q[k]; ok {
				_ = json.Unmarshal(v, &aq.AllowOther)
			}
		}
		if strings.TrimSpace(aq.Question) == "" {
			return nil, fmt.Errorf("every question needs text")
		}
		if aq.Options == nil {
			aq.Options = aq.Options[:0]
		}
		out = append(out, aq)
	}
	return out, nil
}

func (t *coreTools) askUser(ctx context.Context, c tools.Call) (tools.Result, error) {
	qs, err := normalizeQuestions(c.Args)
	if err != nil {
		return errResult("%v", err), nil
	}
	b, _ := json.Marshal(qs)
	v, err := c.Ask(ctx, tools.AskRequest{Kind: "choice", Questions: b})
	if err != nil {
		return textResult("The user did not answer the questions."), nil
	}
	var answers []struct {
		Question string   `json:"question"`
		Answers  []string `json:"answers"`
	}
	if json.Unmarshal(v, &answers) != nil {
		return textResult("The user answered: %s", string(v)), nil
	}
	var sb strings.Builder
	for _, a := range answers {
		fmt.Fprintf(&sb, "Q: %s\nA: %s\n", a.Question, strings.Join(a.Answers, "; "))
	}
	return textResult("%s", sb.String()), nil
}

func (t *coreTools) searchChats(ctx context.Context, c tools.Call) (tools.Result, error) {
	var a struct {
		Query string `json:"query"`
		Limit int    `json:"limit"`
	}
	_ = json.Unmarshal(c.Args, &a)
	q := chats.PrefixQuery(a.Query)
	if q == "" {
		return errResult("query is required"), nil
	}
	if a.Limit <= 0 || a.Limit > 20 {
		a.Limit = 5
	}
	rows, err := t.e.DB.Query(ctx, `SELECT c.id, c.title, m.created_at,
			ts_headline('simple', m.content, to_tsquery('simple', $2), 'MaxWords=40, MinWords=15, StartSel=*, StopSel=*')
		FROM message m JOIN chat c ON c.id = m.chat_id
		WHERE c.user_id = $1 AND m.tsv @@ to_tsquery('simple', $2) AND m.chat_id <> $3
		ORDER BY m.created_at DESC LIMIT $4`, c.User.ID, q, c.ChatID, a.Limit)
	if err != nil {
		return tools.Result{}, fmt.Errorf("search chats: %w", err)
	}
	defer rows.Close()
	var b strings.Builder
	n := 0
	for rows.Next() {
		var id, title, snip string
		var at time.Time
		if err := rows.Scan(&id, &title, &at, &snip); err != nil {
			return tools.Result{}, err
		}
		n++
		fmt.Fprintf(&b, "- %q (chat %s, %s): %s\n", title, id, at.Format("2006-01-02"), strings.ReplaceAll(snip, "\n", " "))
	}
	if n == 0 {
		return textResult("No earlier chats mention %q.", a.Query), nil
	}
	return textResult("%s", b.String()), rows.Err()
}
