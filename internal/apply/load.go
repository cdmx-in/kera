package apply

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// Errors is every problem found in one pass, reported together.
type Errors []string

func (e Errors) Error() string { return strings.Join(e, "\n") }

func (e *Errors) add(line int, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	if line > 0 {
		msg = fmt.Sprintf("line %d: %s", line, msg)
	}
	*e = append(*e, msg)
}

// File is a parsed config plus what apply needs to report problems.
type File struct {
	Config
	lines map[string][]int // section -> line of each list item
}

// Load reads path, substitutes ${VAR} from the environment and validates it.
// Prompt and template files are read relative to the file's directory.
func Load(path string) (*File, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	return Parse(data, filepath.Dir(path), os.LookupEnv)
}

// Parse is Load for bytes, with an explicit environment lookup.
func Parse(data []byte, dir string, lookup func(string) (string, bool)) (*File, error) {
	var errs Errors
	if err := checkKnownFields(data, &errs); err != nil {
		return nil, err
	}
	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	substitute(&root, lookup, &errs)
	f := &File{lines: itemLines(&root)}
	if err := root.Decode(&f.Config); err != nil {
		var te *yaml.TypeError
		if !errors.As(err, &te) {
			return nil, fmt.Errorf("decode config: %w", err)
		}
		errs = append(errs, te.Errors...)
	}
	readFiles(f, dir, &errs)
	validate(f, &errs)
	if len(errs) > 0 {
		return nil, errs
	}
	return f, nil
}

var notFound = regexp.MustCompile(`^(line \d+): field (\S+) not found in type \S+$`)

// checkKnownFields decodes once with KnownFields so unknown keys are reported
// with their line. Other type errors are left to the second pass, after
// ${VAR} substitution.
func checkKnownFields(data []byte, errs *Errors) error {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	var tmp Config
	err := dec.Decode(&tmp)
	if errors.Is(err, io.EOF) {
		return errors.New("config file is empty")
	}
	var te *yaml.TypeError
	if errors.As(err, &te) {
		for _, m := range te.Errors {
			if s := notFound.FindStringSubmatch(m); s != nil {
				*errs = append(*errs, fmt.Sprintf("%s: unknown key %q", s[1], s[2]))
			}
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("parse config: %w", err)
	}
	return nil
}

var envRef = regexp.MustCompile(`\$\$|\$\{[^}]*\}`)
var envName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// substitute expands ${VAR} and ${VAR:-default} in scalar values ($$ is a
// literal $). An unset variable without a default is an error.
func substitute(n *yaml.Node, lookup func(string) (string, bool), errs *Errors) {
	if n.Kind == yaml.ScalarNode && strings.Contains(n.Value, "$") {
		out := envRef.ReplaceAllStringFunc(n.Value, func(m string) string {
			if m == "$$" {
				return "$"
			}
			name, def, hasDef := strings.Cut(m[2:len(m)-1], ":-")
			if !envName.MatchString(name) {
				errs.add(n.Line, "bad variable reference %s", m)
				return ""
			}
			v, ok := lookup(name)
			if hasDef && v == "" {
				return def
			}
			if !ok {
				errs.add(n.Line, "environment variable %s is not set (use ${%s:-default} for an optional value)", name, name)
			}
			return v
		})
		if out != n.Value && n.Style == 0 {
			n.Tag = "" // plain scalar: let the decoder re-resolve "60" as an int, "true" as a bool
		}
		n.Value = out
	}
	for _, c := range n.Content {
		substitute(c, lookup, errs)
	}
}

// itemLines records the line of every list item per top-level section.
func itemLines(root *yaml.Node) map[string][]int {
	out := map[string][]int{}
	if len(root.Content) == 0 || root.Content[0].Kind != yaml.MappingNode {
		return out
	}
	m := root.Content[0]
	for i := 0; i+1 < len(m.Content); i += 2 {
		if seq := m.Content[i+1]; seq.Kind == yaml.SequenceNode {
			for _, it := range seq.Content {
				out[m.Content[i].Value] = append(out[m.Content[i].Value], it.Line)
			}
		}
	}
	return out
}

func (f *File) line(section string, i int) int {
	if l := f.lines[section]; i < len(l) {
		return l[i]
	}
	return 0
}

// readFiles replaces *_file references with the file contents (trimmed).
func readFiles(f *File, dir string, errs *Errors) {
	read := func(line int, what, name string, inline bool) *string {
		if name == "" {
			return nil
		}
		if inline {
			errs.add(line, "%s: set either the inline value or the _file, not both", what)
			return nil
		}
		p := name
		if !filepath.IsAbs(p) {
			p = filepath.Join(dir, p)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			errs.add(line, "%s: %v", what, err)
			return nil
		}
		s := strings.TrimSpace(string(b))
		return &s
	}
	for i := range f.Models {
		m := &f.Models[i]
		if s := read(f.line("models", i), "models["+m.ID+"].prompt_file", m.PromptFile, m.Prompt != ""); s != nil {
			m.Prompt = *s
		}
		m.PromptFile = ""
	}
	if r := f.RAG; r != nil {
		if s := read(0, "rag.template_file", r.TemplateFile, r.Template != nil); s != nil {
			r.Template = s
		}
		r.TemplateFile = ""
	}
	if s := f.Skills; s != nil {
		for _, p := range []*string{&s.Dir, &s.ImportZip} {
			if *p != "" && !filepath.IsAbs(*p) {
				*p = filepath.Join(dir, *p)
			}
		}
	}
	if c := f.CodeInterpreter; c != nil {
		if s := read(0, "code_interpreter.prompt_file", c.PromptFile, c.Prompt != nil); s != nil {
			c.Prompt = s
		}
		if s := read(0, "code_interpreter.prelude_file", c.PreludeFile, c.Prelude != nil); s != nil {
			c.Prelude = s
		}
		c.PromptFile, c.PreludeFile = "", ""
	}
	if c := f.Cowork; c != nil {
		if s := read(0, "cowork.prompt_file", c.PromptFile, c.Prompt != nil); s != nil {
			c.Prompt = s
		}
		c.PromptFile = ""
	}
}
