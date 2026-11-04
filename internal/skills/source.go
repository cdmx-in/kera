package skills

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

// Limits on what an import reads.
const (
	maxFileBytes  = 1 << 20  // one SKILL.md or resource file
	maxSkillBytes = 16 << 20 // one skill with its resources
	maxNameLen    = 128
)

// Skill is one skill as read from a source.
type Skill struct {
	Name        string
	Description string
	Content     string            // the whole SKILL.md
	Resources   map[string]string // path relative to the skill folder -> text
}

// Digest identifies the skill's text, so an import can tell "unchanged".
func (s Skill) Digest() string {
	h := sha256.New()
	fmt.Fprintf(h, "%s\x00%s\x00", s.Description, s.Content)
	keys := make([]string, 0, len(s.Resources))
	for k := range s.Resources {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		fmt.Fprintf(h, "%s\x00%d\x00%s", k, len(s.Resources[k]), s.Resources[k])
	}
	return hex.EncodeToString(h.Sum(nil))
}

// Load reads every skill in a .zip file or a directory. Warnings name files
// that were skipped (binary, too large, duplicate skill names).
func Load(p string) ([]Skill, []string, error) {
	st, err := os.Stat(p)
	if err != nil {
		return nil, nil, err
	}
	if st.IsDir() {
		return Parse(os.DirFS(p), filepath.Base(filepath.Clean(p)))
	}
	z, err := zip.OpenReader(p)
	if err != nil {
		return nil, nil, fmt.Errorf("open %s: %w", p, err)
	}
	defer z.Close()
	return Parse(z, strings.TrimSuffix(filepath.Base(p), filepath.Ext(p)))
}

// Parse finds every SKILL.md in fsys. A skill is the folder holding a
// SKILL.md; its other files (outside nested skill folders) are resources.
// rootName names a SKILL.md at the top of fsys that has no name in its
// frontmatter.
func Parse(fsys fs.FS, rootName string) ([]Skill, []string, error) {
	var files, roots []string
	err := fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		base := d.Name()
		if p != "." && (strings.HasPrefix(base, ".") || base == "__MACOSX") {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil // directories, symlinks, devices
		}
		files = append(files, p)
		if base == "SKILL.md" {
			roots = append(roots, path.Dir(p))
		}
		return nil
	})
	if err != nil {
		return nil, nil, fmt.Errorf("read skills: %w", err)
	}
	// deepest root first, so a file goes to its nearest skill folder
	slices.SortFunc(roots, func(a, b string) int { return strings.Count(b, "/") - strings.Count(a, "/") })
	var warns []string
	byRoot := map[string]*Skill{}
	sizes := map[string]int{}
	for _, root := range roots {
		raw, err := readText(fsys, path.Join(root, "SKILL.md"))
		if err != nil {
			warns = append(warns, err.Error())
			continue
		}
		name, desc := frontmatter(raw)
		if name == "" {
			name = path.Base(root)
			if root == "." {
				name = rootName
			}
		}
		if name == "" || len(name) > maxNameLen || strings.ContainsAny(name, "\r\n") {
			warns = append(warns, fmt.Sprintf("%s/SKILL.md: bad skill name %q", root, name))
			continue
		}
		byRoot[root] = &Skill{Name: name, Description: desc, Content: raw, Resources: map[string]string{}}
		sizes[root] = len(raw)
	}
	for _, f := range files {
		root := owner(roots, f)
		s := byRoot[root]
		if s == nil || path.Base(f) == "SKILL.md" && path.Dir(f) == root {
			continue
		}
		txt, err := readText(fsys, f)
		if err != nil {
			warns = append(warns, err.Error())
			continue
		}
		if sizes[root]+len(txt) > maxSkillBytes {
			warns = append(warns, fmt.Sprintf("%s: skill %s is over %d MB, file skipped", f, s.Name, maxSkillBytes>>20))
			continue
		}
		sizes[root] += len(txt)
		rel := f
		if root != "." {
			rel = strings.TrimPrefix(f, root+"/")
		}
		s.Resources[rel] = txt
	}
	var out []Skill
	seen := map[string]string{}
	slices.Sort(roots)
	for _, root := range roots {
		s := byRoot[root]
		if s == nil {
			continue
		}
		if prev, dup := seen[s.Name]; dup {
			warns = append(warns, fmt.Sprintf("%s: skill name %q already used by %s, skipped", root, s.Name, prev))
			continue
		}
		seen[s.Name] = root
		out = append(out, *s)
	}
	return out, warns, nil
}

// owner returns the skill folder f belongs to ("" when none); roots are deepest first.
func owner(roots []string, f string) string {
	for _, r := range roots {
		if r == "." || strings.HasPrefix(f, r+"/") {
			return r
		}
	}
	return ""
}

// readText reads a UTF-8 text file of at most maxFileBytes.
func readText(fsys fs.FS, p string) (string, error) {
	fh, err := fsys.Open(p)
	if err != nil {
		return "", err
	}
	defer fh.Close()
	b, err := io.ReadAll(io.LimitReader(fh, maxFileBytes+1))
	if err != nil {
		return "", fmt.Errorf("%s: %w", p, err)
	}
	if len(b) > maxFileBytes {
		return "", fmt.Errorf("%s: larger than %d MB, skipped", p, maxFileBytes>>20)
	}
	if !utf8.Valid(b) || slices.Contains(b, 0) {
		return "", fmt.Errorf("%s: not a text file, skipped", p)
	}
	return string(b), nil
}

// frontmatter returns name and description from a leading "---" YAML block;
// the description is folded to one line.
func frontmatter(md string) (name, desc string) {
	md = strings.TrimPrefix(md, "\uFEFF")
	rest, ok := strings.CutPrefix(strings.ReplaceAll(md, "\r\n", "\n"), "---\n")
	if !ok {
		return "", ""
	}
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return "", ""
	}
	var fm struct {
		Name        string `yaml:"name"`
		Description string `yaml:"description"`
	}
	if yaml.Unmarshal([]byte(rest[:end]), &fm) != nil {
		return "", ""
	}
	return strings.TrimSpace(fm.Name), strings.Join(strings.Fields(fm.Description), " ")
}
