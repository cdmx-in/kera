package skills

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/cdmx-in/kera/internal/auth"
	"github.com/cdmx-in/kera/internal/tools"
)

// ID is the tool set id. A model turns it off with tools.builtins.skills: false.
const ID = "builtin:skills"

// Provider offers list_skills, load_skill and read_skill_file to users who
// can read at least one enabled skill. Only names and one-line descriptions
// go into the prompt (in the tool descriptions); the text loads on demand.
type Provider struct{ DB *pgxpool.Pool }

func (p *Provider) ID() string { return ID }

const descMax = 240

func (p *Provider) Tools(ctx context.Context, u *auth.User) ([]tools.Spec, error) {
	rows, err := Visible(ctx, p.DB, u)
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	var b strings.Builder
	b.WriteString("Load a skill: expert instructions for one kind of task. Before a task that one of these skills covers, load it and follow it. Skills:")
	var always []string
	for _, r := range rows {
		fmt.Fprintf(&b, "\n- %s: %s", r.Name, clip(r.Description, descMax))
		if !r.Lazy {
			always = append(always, r.Name)
		}
	}
	// ponytail: non-lazy skills ride in this description (the prompt) in full;
	// a per-run system-prompt hook replaces this if tool descriptions get too long.
	for _, name := range always {
		if s, err := Open(ctx, p.DB, u, name); err == nil {
			fmt.Fprintf(&b, "\n\n<skill name=%q loaded=\"always\">\n%s\n</skill>", s.Name, s.Content)
		}
	}
	return []tools.Spec{
		{Name: "list_skills", Description: "List the skills you can load, with what each one is for.",
			Parameters: json.RawMessage(`{"type":"object","properties":{}}`)},
		{Name: "load_skill", Description: b.String(),
			Parameters: json.RawMessage(`{"type":"object","properties":{"name":{"type":"string","description":"skill name"}},"required":["name"]}`)},
		{Name: "read_skill_file", Description: "Read one file of a loaded skill (load_skill lists its files).",
			Parameters: json.RawMessage(`{"type":"object","properties":{"name":{"type":"string","description":"skill name"},"path":{"type":"string","description":"file path as load_skill lists it"}},"required":["name","path"]}`)},
	}, nil
}

func (p *Provider) Execute(ctx context.Context, c tools.Call) (tools.Result, error) {
	var a struct {
		Name string `json:"name"`
		Path string `json:"path"`
	}
	_ = json.Unmarshal(c.Args, &a)
	a.Name, a.Path = strings.TrimSpace(a.Name), strings.TrimPrefix(strings.TrimSpace(a.Path), "./")
	switch c.Name {
	case "list_skills":
		rows, err := Visible(ctx, p.DB, c.User)
		if err != nil {
			return tools.Result{}, err
		}
		var b strings.Builder
		for _, r := range rows {
			fmt.Fprintf(&b, "- %s: %s\n", r.Name, clip(r.Description, descMax))
		}
		if b.Len() == 0 {
			return fail("No skills are available."), nil
		}
		return tools.Result{Content: b.String()}, nil
	case "load_skill", "read_skill_file":
		s, err := Open(ctx, p.DB, c.User, a.Name)
		if errors.Is(err, ErrNotFound) {
			return fail("No skill named %q. Call list_skills for the names.", a.Name), nil
		}
		if err != nil {
			return tools.Result{}, err
		}
		names := make([]string, 0, len(s.Resources))
		for k := range s.Resources {
			names = append(names, k)
		}
		slices.Sort(names)
		if c.Name == "load_skill" {
			out := s.Content
			if len(names) > 0 {
				out += "\n\n---\nFiles in this skill (read one with read_skill_file):\n- " + strings.Join(names, "\n- ")
			}
			return tools.Result{Content: out}, nil
		}
		txt, ok := s.Resources[a.Path]
		if !ok {
			return fail("Skill %s has no file %q. Its files: %s", s.Name, a.Path, strings.Join(names, ", ")), nil
		}
		return tools.Result{Content: txt}, nil
	}
	return fail("unknown tool %s", c.Name), nil
}

func fail(format string, args ...any) tools.Result {
	return tools.Result{Content: fmt.Sprintf(format, args...), IsError: true}
}

// clip shortens s to n runes.
func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}
