package knowledge

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/cdmx-in/kera/internal/auth"
	"github.com/cdmx-in/kera/internal/retrieval"
	"github.com/cdmx-in/kera/internal/tools"
)

// ToolSetID is the provider id of the builtin knowledge tools.
const ToolSetID = "builtin:knowledge"

// toolSet offers list_knowledge, search_knowledge_files,
// query_knowledge_files and view_knowledge_file. Calls see the collections
// attached to the calling model, or every collection the user can read when
// the model has none.
type toolSet struct{ s *Service }

func (t *toolSet) ID() string { return ToolSetID }

var toolSpecs = []tools.Spec{
	{Name: "list_knowledge", Description: "List the knowledge bases available in this conversation with their descriptions and file counts.",
		Parameters: json.RawMessage(`{"type":"object","properties":{}}`)},
	{Name: "search_knowledge_files", Description: "Find files in the knowledge bases by file name. Returns file ids for view_knowledge_file.",
		Parameters: json.RawMessage(`{"type":"object","properties":{"query":{"type":"string","description":"words to look for in file names; empty lists every file"},"knowledge_id":{"type":"string","description":"limit to one knowledge base"},"count":{"type":"integer","description":"maximum files to return (default 50)"}}}`)},
	{Name: "query_knowledge_files", Description: "Search the content of the knowledge bases and return the most relevant passages with their sources.",
		Parameters: json.RawMessage(`{"type":"object","properties":{"query":{"type":"string","description":"what to look for, in natural language"},"knowledge_ids":{"type":"array","items":{"type":"string"},"description":"limit to these knowledge bases"},"count":{"type":"integer","description":"passages to return (default from settings)"}},"required":["query"]}`)},
	{Name: "view_knowledge_file", Description: "Read the text of one knowledge file, optionally from a character offset.",
		Parameters: json.RawMessage(`{"type":"object","properties":{"file_id":{"type":"string"},"offset":{"type":"integer","description":"character offset to start from (default 0)"},"max_chars":{"type":"integer","description":"characters to return (default 20000)"}},"required":["file_id"]}`)},
}

func (t *toolSet) Tools(ctx context.Context, u *auth.User) ([]tools.Spec, error) {
	if u == nil {
		return nil, nil
	}
	return slices.Clone(toolSpecs), nil
}

// scope returns the collections a call may use, and ctx marked as a local
// run when the calling model is local (the engine marks its runs too; this
// covers callers that only pass the model id).
func (t *toolSet) scope(ctx context.Context, c tools.Call) ([]*Collection, context.Context, error) {
	if t.s.models != nil && c.ModelID != "" {
		if m, err := t.s.models.Resolve(ctx, c.User, c.ModelID); err == nil {
			if m.Local {
				ctx = retrieval.WithLocalRun(ctx)
			}
			if len(m.Knowledge) > 0 {
				refs := make([]string, 0, len(m.Knowledge))
				for _, k := range m.Knowledge {
					for _, v := range []string{k.ID, k.Name} {
						if v != "" {
							refs = append(refs, v)
						}
					}
				}
				cs, err := t.s.readable(ctx, c.User, refs)
				return cs, ctx, err
			}
		}
	}
	cs, err := t.s.readable(ctx, c.User, nil)
	return cs, ctx, err
}

func (t *toolSet) Execute(ctx context.Context, c tools.Call) (tools.Result, error) {
	var args struct {
		Query        string   `json:"query"`
		KnowledgeID  string   `json:"knowledge_id"`
		KnowledgeIDs []string `json:"knowledge_ids"`
		Count        int      `json:"count"`
		FileID       string   `json:"file_id"`
		Offset       int      `json:"offset"`
		MaxChars     int      `json:"max_chars"`
	}
	if len(c.Args) > 0 {
		if err := json.Unmarshal(c.Args, &args); err != nil {
			return errResult("invalid arguments: " + err.Error()), nil
		}
	}
	cs, ctx, err := t.scope(ctx, c)
	if err != nil {
		return tools.Result{}, err
	}
	switch c.Name {
	case "list_knowledge":
		return t.list(ctx, cs)
	case "search_knowledge_files":
		if args.KnowledgeID != "" {
			cs = only(cs, []string{args.KnowledgeID})
		}
		return t.searchFiles(ctx, cs, args.Query, args.Count)
	case "query_knowledge_files":
		if len(args.KnowledgeIDs) > 0 {
			cs = only(cs, args.KnowledgeIDs)
		}
		return t.query(ctx, c, cs, args.Query, args.Count)
	case "view_knowledge_file":
		return t.view(ctx, cs, args.FileID, args.Offset, args.MaxChars)
	}
	return errResult("unknown tool " + c.Name), nil
}

func errResult(msg string) tools.Result {
	b, _ := json.Marshal(map[string]string{"error": msg})
	return tools.Result{Content: string(b), IsError: true}
}

func jsonResult(v any) tools.Result {
	b, _ := json.Marshal(v)
	return tools.Result{Content: string(b)}
}

// only keeps the collections whose id or name is listed.
func only(cs []*Collection, idsOrNames []string) []*Collection {
	return slices.DeleteFunc(slices.Clone(cs), func(c *Collection) bool {
		return !slices.Contains(idsOrNames, c.ID) && !slices.Contains(idsOrNames, c.Name)
	})
}

func collIDs(cs []*Collection) []string {
	out := make([]string, len(cs))
	for i, c := range cs {
		out[i] = c.ID
	}
	return out
}

func (t *toolSet) list(ctx context.Context, cs []*Collection) (tools.Result, error) {
	st, err := t.s.stats(ctx, collIDs(cs))
	if err != nil {
		return tools.Result{}, err
	}
	out := make([]map[string]any, 0, len(cs))
	for _, c := range cs {
		n := 0
		if x := st[c.ID]; x != nil {
			n = x.Files
		}
		out = append(out, map[string]any{"id": c.ID, "name": c.Name, "description": c.Description, "files": n})
	}
	return jsonResult(out), nil
}

func (t *toolSet) searchFiles(ctx context.Context, cs []*Collection, query string, count int) (tools.Result, error) {
	if count <= 0 || count > 200 {
		count = 50
	}
	words := strings.Fields(strings.ToLower(query))
	rows, err := t.s.db.Query(ctx, `SELECT f.id, f.filename, k.id, k.name FROM knowledge_file kf
		JOIN file f ON f.id = kf.file_id JOIN knowledge k ON k.id = kf.knowledge_id
		WHERE kf.knowledge_id = ANY($1) AND kf.replaces IS NULL AND (SELECT bool_and(lower(f.filename) LIKE '%' || w || '%') FROM unnest($2::text[]) w) IS NOT FALSE
		ORDER BY k.name, f.filename LIMIT $3`, collIDs(cs), nonNil(words), count)
	if err != nil {
		return tools.Result{}, fmt.Errorf("search knowledge files: %w", err)
	}
	out, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (map[string]any, error) {
		var fid, name, kid, kname string
		if err := r.Scan(&fid, &name, &kid, &kname); err != nil {
			return nil, err
		}
		return map[string]any{"file_id": fid, "name": name, "knowledge_id": kid, "knowledge": kname}, nil
	})
	if err != nil {
		return tools.Result{}, err
	}
	return jsonResult(nonNil(out)), nil
}

func (t *toolSet) query(ctx context.Context, c tools.Call, cs []*Collection, query string, count int) (tools.Result, error) {
	if strings.TrimSpace(query) == "" {
		return errResult("query is required"), nil
	}
	c.Emit(tools.Event{Type: "status", Data: map[string]any{"action": "knowledge_search", "description": "Searching knowledge", "query": query, "done": false}})
	cfg := t.s.searchConfig(ctx)
	if count > 0 {
		cfg.TopKRerank = min(count, 50)
	}
	hits, err := t.s.search(ctx, cs, []string{query}, cfg)
	if err != nil {
		return tools.Result{}, err
	}
	c.Emit(tools.Event{Type: "status", Data: map[string]any{"action": "knowledge_search", "description": "Searched knowledge", "query": query, "count": len(hits), "done": true}})
	res := tools.Result{}
	out := make([]map[string]any, 0, len(hits))
	for i, h := range hits {
		item := map[string]any{"source": i + 1, "file": h.FileName, "file_id": h.FileID, "text": h.Text}
		if h.Page > 0 {
			item["page"] = h.Page
		}
		if len(h.Headers) > 0 {
			item["section"] = strings.Join(h.Headers, " > ")
		}
		out = append(out, item)
		res.Sources = append(res.Sources, tools.Source{Name: h.FileName, FileID: h.FileID, Page: h.Page, Snippet: truncate(h.Text, 300)})
	}
	b, _ := json.Marshal(out)
	res.Content = string(b)
	return res, nil
}

func (t *toolSet) view(ctx context.Context, cs []*Collection, fileID string, offset, maxChars int) (tools.Result, error) {
	if fileID == "" {
		return errResult("file_id is required"), nil
	}
	var name string
	err := t.s.db.QueryRow(ctx, `SELECT f.filename FROM knowledge_file kf JOIN file f ON f.id = kf.file_id
		WHERE kf.file_id = $1 AND kf.knowledge_id = ANY($2) AND kf.replaces IS NULL LIMIT 1`, fileID, collIDs(cs)).Scan(&name)
	if err == pgx.ErrNoRows {
		return errResult("no such file in the available knowledge"), nil
	}
	if err != nil {
		return tools.Result{}, fmt.Errorf("view knowledge file: %w", err)
	}
	text, err := t.s.files.Text(ctx, fileID)
	if err != nil {
		return tools.Result{}, err
	}
	if maxChars <= 0 || maxChars > 100000 {
		maxChars = 20000
	}
	total := utf8.RuneCountInString(text)
	rs := []rune(text)
	start := min(max(offset, 0), total)
	end := min(start+maxChars, total)
	return jsonResult(map[string]any{"file_id": fileID, "name": name, "offset": start, "end": end, "total_chars": total,
		"truncated": end < total, "text": string(rs[start:end])}), nil
}
