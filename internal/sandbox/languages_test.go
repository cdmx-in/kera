package sandbox

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/cdmx-in/kera/internal/settings"
)

func defaultLanguages(t *testing.T) map[string]Language {
	t.Helper()
	var m map[string]Language
	if err := json.Unmarshal(settings.Defaults["code_interpreter.languages"], &m); err != nil {
		t.Fatal(err)
	}
	return validLanguages(m)
}

func TestResolveLanguage(t *testing.T) {
	langs := defaultLanguages(t)
	for req, want := range map[string]string{"": "python", "Python": "python", "py": "python", " PHP ": "php",
		"node": "javascript", "js": "javascript", "ts": "typescript", "shell": "bash", "golang": "go", "Rscript": "r"} {
		if got, ok := resolve(langs, req); !ok || got != want {
			t.Errorf("resolve(%q) = %q %v, want %q", req, got, ok, want)
		}
	}
	for _, bad := range []string{"cobol", "php; rm -rf /", "../php"} {
		if got, ok := resolve(langs, bad); ok {
			t.Errorf("resolve(%q) = %q", bad, got)
		}
	}
	// malformed entries are dropped, python is always there
	v := validLanguages(map[string]Language{"Perl": {Command: []string{"perl", "{file}"}}, "lua": {Command: []string{"lua"}, Ext: "../x"}})
	if len(v) != 1 || v["python"].Label != "Python" {
		t.Fatalf("validLanguages = %+v", v)
	}
}

func TestArgvNoShell(t *testing.T) {
	got := argv([]string{"node", "--flag", "{file}", "x{file}y"}, ".kera/main.js")
	if !reflect.DeepEqual(got, []string{"node", "--flag", ".kera/main.js", "x.kera/main.jsy"}) {
		t.Fatal(got)
	}
	// model input never reaches argv: it is the file's content, embedded as a string literal
	d := driver([]string{"php", "{file}"}, ".php", "<?php echo 1; ?>\"); import os; os.system('x') #", time.Minute)
	bs := `\` // the code travels as a JSON string literal: <, > and " are escaped
	want := `_kera_run(["php",".kera/main.php"], ".kera/main.php", "` + bs + "u003c?php echo 1; ?" + bs + "u003e" + bs + `"); import os; os.system('x') #", 60, 20000)`
	if !strings.Contains(d, want) {
		t.Fatal(d)
	}
}

func TestToolSpecFiltered(t *testing.T) {
	langs := defaultLanguages(t)
	if s := toolSpec(langs, map[string][]string{"python": nil}); s.Description != spec.Description || string(s.Parameters) != string(spec.Parameters) {
		t.Fatal("python only must keep the plain schema")
	}
	s := toolSpec(langs, map[string][]string{"python": nil, "php": {"php", "{file}"}, "javascript": {"node", "{file}"}})
	var schema struct {
		Properties struct {
			Language struct {
				Enum []string `json:"enum"`
			} `json:"language"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(s.Parameters, &schema); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(schema.Properties.Language.Enum, []string{"python", "javascript", "php"}) {
		t.Fatalf("enum = %v", schema.Properties.Language.Enum)
	}
	if !strings.Contains(s.Description, "php (PHP)") || strings.Contains(s.Description, "ruby") {
		t.Fatal(s.Description)
	}
	n := languageNote(langs, map[string][]string{"python": nil, "php": nil})
	if !strings.Contains(n, `"php" (PHP)`) || strings.Contains(n, `"python"`) {
		t.Fatal(n)
	}
	if languageNote(langs, map[string][]string{"python": nil}) != "" {
		t.Fatal("note without other languages")
	}
}

// python3 runs the generated Python locally (the kernel runs the same code).
func python3(t *testing.T, dir, code string) string {
	t.Helper()
	py, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not installed")
	}
	cmd := exec.Command(py, "-I", "-c", code)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("python: %v\n%s", err, out)
	}
	return string(out)
}

func TestProbeCode(t *testing.T) {
	langs := map[string]Language{
		"python":  {},
		"bash":    {Command: []string{"bash", "{file}"}, Ext: ".sh"},
		"nope":    {Command: []string{"kera-no-such-binary", "{file}"}, Ext: ".x"},
		"checked": {Command: []string{"bash", "{file}"}, Ext: ".sh", Check: []string{"false"}, Fallback: []string{"sh", "{file}"}},
		"failed":  {Command: []string{"bash", "{file}"}, Ext: ".sh", Check: []string{"false"}},
	}
	got, err := parseProbe(python3(t, t.TempDir(), probeCode(langs)))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string][]string{"python": nil, "bash": {"bash", "{file}"}, "checked": {"sh", "{file}"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("probe = %v", got)
	}
}

func TestDriver(t *testing.T) {
	run := func(code string, timeout time.Duration) (string, string) {
		dir := t.TempDir()
		d := driver([]string{"bash", "{file}"}, ".sh", code, timeout)
		d = strings.Replace(d, "\n_kera_run(", "\nprint('RC=%r' % (_kera_run(", 1) + "))"
		return python3(t, dir, d), dir
	}
	out, dir := run("echo hello; echo oops >&2; read x; echo \"stdin=[$x]\"; printf 'a,b\\n' > out.csv; exit 3", time.Minute)
	if !strings.Contains(out, "hello\nstdin=[]\n") || !strings.Contains(out, "oops") || !strings.HasSuffix(out, "RC=3\n") {
		t.Fatalf("output %q", out)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "out.csv")); string(b) != "a,b\n" {
		t.Fatalf("out.csv = %q", b)
	}
	if _, err := os.Stat(filepath.Join(dir, scriptDir, "main.sh")); err != nil {
		t.Fatal("script not under the hidden folder")
	}
	out, _ = run("for i in $(seq 1 200000); do echo line$i; done", time.Minute)
	if len(out) > 3*maxOutputChars || !strings.Contains(out, "line1\n") || !strings.Contains(out, "line200000\n") || !strings.Contains(out, "bytes cut") {
		t.Fatalf("cap: %d bytes", len(out))
	}
	start := time.Now()
	out, _ = run("echo before; sleep 30 & sleep 30", time.Second)
	if !strings.Contains(out, "before") || !strings.HasSuffix(out, "RC='timeout'\n") || time.Since(start) > 15*time.Second {
		t.Fatalf("timeout: %q after %s", out, time.Since(start))
	}
}

// {dir} lets a toolchain build inside the run folder: sandboxes mount /tmp noexec,
// so "go run" fails with "permission denied" unless GOTMPDIR points elsewhere.
func TestArgvDir(t *testing.T) {
	got := argv([]string{"env", "GOTMPDIR={dir}", "go", "run", "{file}"}, ".kera/main.go")
	want := []string{"env", "GOTMPDIR=.kera", "go", "run", ".kera/main.go"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("argv = %v, want %v", got, want)
	}
}
