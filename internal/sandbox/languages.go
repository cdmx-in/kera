package sandbox

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/cdmx-in/kera/internal/apply"
	"github.com/cdmx-in/kera/internal/tools"
)

// Language is one entry of code_interpreter.languages.
type Language = apply.CodeLanguage

const (
	defaultLanguage = "python" // the kernel itself
	scriptDir       = ".kera"  // hidden, so the harvest skips the script
	probeTimeout    = 20 * time.Second
	probeTTL        = time.Hour // a recreated sandbox image is noticed within this
	probeRetry      = time.Minute
)

// resolve maps a requested language (name or alias, any case; "" = python)
// to its configured name.
func resolve(langs map[string]Language, req string) (string, bool) {
	n := strings.ToLower(strings.TrimSpace(req))
	if n == "" {
		n = defaultLanguage
	}
	if _, ok := langs[n]; ok {
		return n, true
	}
	names := sortedNames(langs) // deterministic when two entries share an alias
	for _, k := range names {
		if slices.Contains(langs[k].Aliases, n) {
			return k, true
		}
	}
	return "", false
}

// sortedNames lists python first, then the rest alphabetically.
func sortedNames[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.SortFunc(out, func(a, b string) int {
		switch {
		case a == defaultLanguage:
			return -1
		case b == defaultLanguage:
			return 1
		}
		return strings.Compare(a, b)
	})
	return out
}

// argv fills the {file} placeholder; the result goes to subprocess as a list,
// never through a shell.
func argv(command []string, file string) []string {
	out := make([]string, len(command))
	for i, a := range command {
		out[i] = strings.ReplaceAll(a, "{file}", file)
	}
	return out
}

// validLanguages drops malformed entries and always keeps python (the kernel).
func validLanguages(in map[string]Language) map[string]Language {
	out := map[string]Language{}
	for name, l := range in {
		n := strings.ToLower(strings.TrimSpace(name))
		if n == "" || (len(l.Command) > 0 && (l.Ext == "" || strings.ContainsAny(l.Ext, `/\`) || l.Command[0] == "")) {
			slog.Warn("code_interpreter.languages: entry ignored (needs command and ext)", "language", name)
			continue
		}
		out[n] = l
	}
	if _, ok := out[defaultLanguage]; !ok {
		out[defaultLanguage] = Language{Label: "Python"}
	}
	return out
}

// toolSpec builds execute_code for the installed languages (name -> argv,
// nil for the kernel). With Python alone the schema stays code-only.
func toolSpec(langs map[string]Language, avail map[string][]string) tools.Spec {
	s := spec
	names := sortedNames(avail)
	if len(names) < 2 {
		return s
	}
	var list []string
	for _, n := range names {
		list = append(list, fmt.Sprintf("%s (%s)", n, cmpOr(langs[n].Label, n)))
	}
	s.Description = "Run code in a sandbox and return stdout, stderr and any error. Languages: " + strings.Join(list, ", ") +
		". Python (the default) runs in a Jupyter kernel and also returns the last expression's value; matplotlib figures " +
		"shown with plt.show() are returned as images. Other languages run as a script with their interpreter or compiler " +
		"and also return exit_code. Every call starts fresh in an empty working directory (nothing persists between calls), " +
		"with the chat's attached files copied in under their own names (listed in ATTACHMENTS for Python). New files saved " +
		"in the working directory are given to the user as downloads."
	enum, _ := json.Marshal(names)
	s.Parameters = json.RawMessage(`{"type":"object","properties":{` +
		`"code":{"type":"string","description":"The source code to run (a complete program for compiled languages)"},` +
		`"language":{"type":"string","enum":` + string(enum) + `,"description":"Language of the code; default python"}},` +
		`"required":["code"]}`)
	return s
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// languageNote is the system-prompt line naming the installed languages
// ("" when only Python is there).
func languageNote(langs map[string]Language, avail map[string][]string) string {
	names := sortedNames(avail)
	if len(names) < 2 {
		return ""
	}
	var list []string
	for _, n := range names[1:] {
		list = append(list, fmt.Sprintf("%q (%s)", n, cmpOr(langs[n].Label, n)))
	}
	return "Besides Python (the default), execute_code runs these languages when you pass its language argument: " +
		strings.Join(list, ", ") + ". The script runs in the same working directory: attachments are there under their " +
		"own names and new files are returned to the user the same way."
}

// probeCode prints, as JSON on one line, name -> argv for every configured
// language whose program is installed in the kernel's container ([] for the
// kernel itself). check commands run without a shell.
func probeCode(langs map[string]Language) string {
	b, _ := json.Marshal(langs)
	lit, _ := json.Marshal(string(b)) // a JSON string is a valid Python string literal
	return `import json as _j, shutil as _sh, subprocess as _sp
def _kera_ok(argv, check):
    if not argv or not _sh.which(argv[0]):
        return False
    if not check:
        return True
    try:
        return _sp.run(check, stdin=_sp.DEVNULL, stdout=_sp.DEVNULL, stderr=_sp.DEVNULL, timeout=20).returncode == 0
    except Exception:
        return False
_kera_out = {}
for _n, _l in _j.loads(` + string(lit) + `).items():
    if not _l.get("command"):
        _kera_out[_n] = []
    elif _kera_ok(_l["command"], _l.get("check")):
        _kera_out[_n] = _l["command"]
    elif _kera_ok(_l.get("fallback"), None):
        _kera_out[_n] = _l["fallback"]
print("KERA_LANGS " + _j.dumps(_kera_out))
`
}

func parseProbe(stdout string) (map[string][]string, error) {
	for line := range strings.SplitSeq(stdout, "\n") {
		if rest, ok := strings.CutPrefix(line, "KERA_LANGS "); ok {
			var m map[string][]string
			if err := json.Unmarshal([]byte(rest), &m); err != nil {
				return nil, err
			}
			for k, v := range m {
				if len(v) == 0 {
					m[k] = nil
				}
			}
			return m, nil
		}
	}
	return nil, fmt.Errorf("no probe output")
}

// probed caches the last probe. ponytail: one entry for the process (there is
// one sandbox); keyed by URL and the languages so a settings change re-probes.
var probed struct {
	sync.Mutex
	key   string
	until time.Time
	avail map[string][]string
	langs map[string]Language
}

// available returns name -> argv of the installed languages, probing the
// sandbox at most once per probeTTL (probeRetry after a failure, when only
// Python is offered).
func (p *Provider) available(ctx context.Context, cfg config) map[string][]string {
	b, _ := json.Marshal(cfg.languages)
	key := cfg.url + "\x00" + string(b)
	probed.Lock()
	defer probed.Unlock()
	if probed.key == key && time.Now().Before(probed.until) {
		return probed.avail
	}
	avail, err := p.probe(ctx, cfg)
	until := time.Now().Add(probeTTL)
	if err != nil {
		slog.Warn("sandbox language probe", "err", err)
		until = time.Now().Add(probeRetry)
		avail = map[string][]string{}
		if probed.key == key { // a sandbox restarting keeps what it had
			maps.Copy(avail, probed.avail)
		}
	}
	probed.key, probed.langs, probed.until = key, cfg.languages, until
	avail[defaultLanguage] = nil
	for n := range avail {
		if _, ok := cfg.languages[n]; !ok {
			delete(avail, n)
		}
	}
	probed.avail = avail
	return avail
}

func (p *Provider) probe(ctx context.Context, cfg config) (map[string][]string, error) {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	k, err := (&Jupyter{URL: cfg.url, Token: cfg.token, HTTP: p.HTTP}).Start(ctx)
	if err != nil {
		return nil, err
	}
	defer k.Close()
	out, err := k.Exec(ctx, probeCode(cfg.languages), probeTimeout)
	if err != nil {
		return nil, err
	}
	if out.Error != "" || out.TimedOut {
		return nil, fmt.Errorf("probe failed: %s", tail(out.Error, 3, 500))
	}
	return parseProbe(out.Stdout)
}

// cachedNote is languageNote for the last probe (Prompt has only settings).
func cachedNote() string {
	probed.Lock()
	defer probed.Unlock()
	return languageNote(probed.langs, probed.avail)
}

// driver is the Python that runs a non-Python script inside the kernel:
// write the source under the hidden script folder of the working directory,
// run argv (cwd = the working directory, stdin closed, its own process group
// killed at the end), keep the head and tail of each stream, print them and
// evaluate to the exit code ('timeout' when it was stopped).
func driver(command []string, ext, code string, timeout time.Duration) string {
	file := scriptDir + "/main" + ext
	a, _ := json.Marshal(argv(command, file))
	f, _ := json.Marshal(file)
	src, _ := json.Marshal(code)
	return `def _kera_run(argv, name, src, timeout, cap):
    import os, signal, subprocess, sys, threading
    os.makedirs(os.path.dirname(name), exist_ok=True)
    with open(name, "w", encoding="utf-8") as fh:
        fh.write(src)
    p = subprocess.Popen(argv, stdin=subprocess.DEVNULL, stdout=subprocess.PIPE, stderr=subprocess.PIPE, start_new_session=True)
    got = {}
    def pump(stream, key):
        head, tl, n = bytearray(), bytearray(), 0
        for chunk in iter(lambda: stream.read1(65536), b""):
            n += len(chunk)
            if len(head) < cap:
                take = cap - len(head)
                head += chunk[:take]
                chunk = chunk[take:]
            if chunk:
                tl += chunk
                del tl[:-cap]
        got[key] = (head, tl, n)
    ts = [threading.Thread(target=pump, args=(p.stdout, 1), daemon=True), threading.Thread(target=pump, args=(p.stderr, 2), daemon=True)]
    for t in ts:
        t.start()
    try:
        rc = p.wait(timeout=timeout)
    except subprocess.TimeoutExpired:
        rc = None
    try:
        os.killpg(p.pid, signal.SIGKILL)
    except OSError:
        pass
    p.wait()
    for t in ts:
        t.join(5)
    for key, out in ((1, sys.stdout), (2, sys.stderr)):
        head, tl, n = got.get(key, (b"", b"", 0))
        text = head.decode("utf-8", "replace")
        if n > len(head):
            cut = n - len(head) - len(tl)
            if cut > 0:
                text += "\n[... %d bytes cut ...]\n" % cut
            text += tl.decode("utf-8", "replace")
        out.write(text)
        out.flush()
    return "timeout" if rc is None else rc
_kera_run(` + string(a) + `, ` + string(f) + `, ` + string(src) + `, ` +
		strconv.Itoa(int(max(timeout.Seconds(), 1))) + `, ` + strconv.Itoa(maxOutputChars) + `)`
}
