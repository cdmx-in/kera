// Package sandbox is the built-in code interpreter (tool set "builtin:code"):
// execute_code runs Python in a Jupyter kernel (other languages from
// code_interpreter.languages as a subprocess started by the kernel), with the chat's attachments
// staged into a per-call folder on a directory shared with the kernel, and
// every new file the code writes there returned as a file of the user.
package sandbox

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/cdmx-in/kera/internal/app"
	"github.com/cdmx-in/kera/internal/apply"
	"github.com/cdmx-in/kera/internal/auth"
	"github.com/cdmx-in/kera/internal/files"
	"github.com/cdmx-in/kera/internal/ids"
	"github.com/cdmx-in/kera/internal/secret"
	"github.com/cdmx-in/kera/internal/settings"
	"github.com/cdmx-in/kera/internal/tools"
)

// ID is the tool set id.
const ID = "builtin:code"

const (
	maxOutputChars = 20000 // per stream, sent to the model
	maxHarvest     = 20    // files returned per call
	setupTimeout   = 30 * time.Second
)

func init() { app.Register("sandbox", Mount) }

// Mount registers the execute_code provider.
func Mount(a *app.App) error {
	tools.Register(&Provider{Settings: a.Settings, Keys: a.Keys, Files: files.For(a),
		HTTP: &http.Client{Timeout: 30 * time.Second}})
	return nil
}

// DefaultPrompt is used when code_interpreter.prompt is empty.
const DefaultPrompt = "You can run Python with the execute_code tool. Use it for calculations, data work, charts and " +
	"for creating files. Files the user attached to this chat are in the working directory under their own names " +
	"(the ATTACHMENTS list names them): open them by name. To give the user a file, save it in the working " +
	"directory with a descriptive name (for example summary.xlsx); every new file is attached to your reply " +
	"automatically, so do not write links or paths to it."

// Prompt returns the system-prompt text describing the sandbox: the
// code_interpreter.prompt setting (`kera apply` loads it from prompt_file),
// or DefaultPrompt, plus a line naming the other installed languages (known
// once Tools has probed the sandbox). The chat engine appends it to the system prompt when the
// code_interpreter feature is on for the turn.
func Prompt(ctx context.Context, st *settings.Store) string {
	var p string
	_ = st.Get(ctx, "code_interpreter.prompt", &p)
	if strings.TrimSpace(p) == "" {
		p = DefaultPrompt
	}
	if n := cachedNote(); n != "" {
		p = strings.TrimRight(p, "\n") + "\n\n" + n
	}
	return p
}

// Provider implements tools.Provider for execute_code.
type Provider struct {
	Settings *settings.Store
	Keys     *secret.Keys
	Files    *files.Service
	HTTP     *http.Client // Jupyter REST and websocket
}

// ID implements tools.Provider.
func (p *Provider) ID() string { return ID }

var spec = tools.Spec{
	Name: "execute_code",
	Description: "Run Python code in a sandboxed Jupyter kernel and return stdout, stderr, the last expression's value " +
		"and any error. Every call starts a fresh kernel in an empty working directory (nothing persists between " +
		"calls), with the chat's attached files copied in under their own names (listed in ATTACHMENTS). New files " +
		"saved in the working directory are given to the user as downloads. Matplotlib figures shown with " +
		"plt.show() are returned as images.",
	Parameters: json.RawMessage(`{"type":"object","properties":{"code":{"type":"string","description":"The Python code to run"}},"required":["code"]}`),
}

// Tools implements tools.Provider: execute_code when the interpreter is
// configured and the user has the code_interpreter permission.
func (p *Provider) Tools(ctx context.Context, u *auth.User) ([]tools.Spec, error) {
	if u == nil || !u.Can("features.code_interpreter") {
		return nil, nil
	}
	cfg, err := p.load(ctx)
	if err != nil || !cfg.enabled() {
		return nil, err
	}
	return []tools.Spec{toolSpec(cfg.languages, p.available(ctx, cfg))}, nil
}

type config struct {
	engine, url, token   string
	timeout              time.Duration
	sharedDir, kernelDir string
	prelude              string
	maxFile              int64
	languages            map[string]Language
}

func (c config) enabled() bool { return c.engine == "jupyter" && c.url != "" }

func (p *Provider) load(ctx context.Context) (config, error) {
	var c config
	var timeout, maxMB int
	st := p.Settings
	for key, dst := range map[string]any{
		"code_interpreter.engine": &c.engine, "code_interpreter.url": &c.url, "code_interpreter.timeout": &timeout,
		"code_interpreter.shared_dir": &c.sharedDir, "code_interpreter.kernel_dir": &c.kernelDir,
		"code_interpreter.prelude": &c.prelude, "code_interpreter.max_file_mb": &maxMB,
		"code_interpreter.languages": &c.languages,
	} {
		if err := st.Get(ctx, key, dst); err != nil {
			return c, err
		}
	}
	tok, err := apply.ReadSecret(ctx, st, p.Keys, "code_interpreter.token")
	if err != nil {
		return c, err
	}
	c.token = tok
	c.timeout = time.Duration(max(timeout, 1)) * time.Second
	c.maxFile = int64(max(maxMB, 1)) << 20
	c.languages = validLanguages(c.languages)
	if c.kernelDir == "" {
		c.kernelDir = "/mnt/data"
	}
	return c, nil
}

// Execute implements tools.Provider.
func (p *Provider) Execute(ctx context.Context, c tools.Call) (tools.Result, error) {
	var args struct {
		Code     string `json:"code"`
		Language string `json:"language"`
	}
	if err := json.Unmarshal(c.Args, &args); err != nil || strings.TrimSpace(args.Code) == "" {
		return tools.Result{Content: `{"error":"the code argument is required"}`, IsError: true}, nil
	}
	cfg, err := p.load(ctx)
	if err != nil {
		return tools.Result{}, err
	}
	if !cfg.enabled() {
		return tools.Result{Content: `{"error":"the code interpreter is not configured"}`, IsError: true}, nil
	}
	lang, ok := resolve(cfg.languages, args.Language)
	var command []string
	if ok && lang != defaultLanguage {
		avail := p.available(ctx, cfg)
		command, ok = avail[lang]
		if !ok {
			return report{Error: fmt.Sprintf("%s is not installed in the sandbox; available: %s", lang,
				strings.Join(sortedNames(avail), ", "))}.result(nil), nil
		}
	}
	if !ok {
		return report{Error: fmt.Sprintf("unknown language %q; available: %s", args.Language,
			strings.Join(sortedNames(p.available(ctx, cfg)), ", "))}.result(nil), nil
	}
	c.Emit(status(false, "Running code"))
	res := p.run(ctx, c, cfg, job{lang: lang, command: command, ext: cfg.languages[lang].Ext, code: args.Code})
	desc := "Ran code"
	if res.IsError {
		desc = "Code failed"
	}
	c.Emit(status(true, desc))
	return res, nil
}

func status(done bool, desc string) tools.Event {
	return tools.Event{Type: "status", Data: map[string]any{"action": "code_run", "description": desc, "done": done}}
}

// job is one execute_code call; command is nil for Python (the kernel).
type job struct {
	lang, ext, code string
	command         []string
}

// report is the JSON the model receives.
type report struct {
	Stdout   string   `json:"stdout,omitempty"`
	Stderr   string   `json:"stderr,omitempty"`
	Result   string   `json:"result,omitempty"`
	ExitCode *int     `json:"exit_code,omitempty"` // non-Python languages
	Error    string   `json:"error,omitempty"`
	Files    []string `json:"files,omitempty"`
	Skipped  []string `json:"skipped_files,omitempty"`
}

func (r report) result(fileIDs []string) tools.Result {
	b, _ := json.Marshal(r)
	return tools.Result{Content: string(b), Files: fileIDs, IsError: r.Error != ""}
}

// run stages inputs, executes, harvests outputs and removes the run folder.
func (p *Provider) run(ctx context.Context, c tools.Call, cfg config, jb job) tools.Result {
	var rep report
	runID := ids.New()
	hostDir, kernelDir := "", cfg.kernelDir
	var inputs map[string]stamp
	if cfg.sharedDir != "" {
		hostDir = filepath.Join(cfg.sharedDir, "runs", runID)
		kernelDir = path.Join(cfg.kernelDir, "runs", runID)
		var err error
		if inputs, err = p.stage(ctx, c.User, c.FileIDs, hostDir); err != nil {
			slog.Error("sandbox staging", "run", c.RunID, "err", err)
			rep.Error = "could not prepare the sandbox folder"
			return rep.result(nil)
		}
		defer func() {
			if err := os.RemoveAll(hostDir); err != nil {
				slog.Warn("sandbox cleanup", "dir", hostDir, "err", err)
			}
		}()
	}
	code, timeout := rewritePaths(jb.code, cfg.kernelDir, kernelDir), cfg.timeout
	if jb.command != nil {
		// the driver enforces cfg.timeout on the script; the kernel gets slack to report it
		code, timeout = driver(jb.command, jb.ext, code, cfg.timeout), cfg.timeout+setupTimeout
	}
	out, err := p.execute(ctx, cfg, prelude(kernelDir, inputs, cfg.prelude), code, timeout)
	if err != nil {
		slog.Warn("sandbox execute", "run", c.RunID, "err", err)
		rep.Error = "sandbox unavailable: " + err.Error()
		return rep.result(nil)
	}
	rep.Stdout, rep.Stderr, rep.Result = clip(out.Stdout), clip(out.Stderr), clip(out.Result)
	if out.Error != "" {
		rep.Error = tail(out.Error, 40, maxOutputChars)
	}
	if jb.command != nil && out.Error == "" && !out.TimedOut {
		v := strings.TrimSpace(out.Result) // the driver's value: the exit code or 'timeout'
		rep.Result = ""
		if rc, err := strconv.Atoi(v); err == nil {
			rep.ExitCode = &rc
			if rc != 0 {
				rep.Error = fmt.Sprintf("%s exited with code %d", jb.lang, rc)
			}
		} else if v == "'timeout'" {
			rep.Error = fmt.Sprintf("timed out after %s; the %s process was stopped", cfg.timeout, jb.lang)
		}
	}
	if out.TimedOut {
		rep.Error = strings.TrimSpace(rep.Error + fmt.Sprintf("\ntimed out after %s; the kernel was stopped", cfg.timeout))
	}
	var fileIDs []string
	for i, img := range out.Images {
		name := fmt.Sprintf("output-%d.%s", i+1, strings.TrimPrefix(out.ImageTypes[i], "image/"))
		f, err := p.Files.Create(ctx, c.User.ID, name, out.ImageTypes[i], bytes.NewReader(img), meta(c))
		if err != nil {
			slog.Warn("sandbox image", "err", err)
			continue
		}
		fileIDs = append(fileIDs, f.ID)
		rep.Files = append(rep.Files, f.Filename)
	}
	if hostDir != "" {
		got, skipped, err := p.harvest(ctx, c, hostDir, inputs, cfg.maxFile)
		if err != nil {
			slog.Warn("sandbox harvest", "run", c.RunID, "err", err)
		}
		for _, f := range got {
			fileIDs = append(fileIDs, f.ID)
			rep.Files = append(rep.Files, f.Filename)
		}
		rep.Skipped = skipped
	}
	return rep.result(fileIDs)
}

// execute runs the prelude and the code in one fresh kernel, then deletes it.
func (p *Provider) execute(ctx context.Context, cfg config, pre, code string, timeout time.Duration) (Output, error) {
	j := &Jupyter{URL: cfg.url, Token: cfg.token, HTTP: p.HTTP}
	k, err := j.Start(ctx)
	if err != nil {
		return Output{}, err
	}
	defer k.Close()
	setup, err := k.Exec(ctx, pre, setupTimeout)
	if err != nil {
		return Output{}, err
	}
	if setup.Error != "" || setup.TimedOut {
		return Output{}, fmt.Errorf("sandbox setup failed: %s", tail(setup.Error, 5, 2000))
	}
	return k.Exec(ctx, code, timeout)
}

func meta(c tools.Call) map[string]any {
	return map[string]any{"source": "code_interpreter", "chat_id": c.ChatID, "message_id": c.MessageID}
}

// prelude moves the kernel into the run folder and names the attachments.
// umask 0 lets Kera (another uid) clean up whatever the code creates.
func prelude(dir string, inputs map[string]stamp, extra string) string {
	names := make([]string, 0, len(inputs))
	for n := range inputs {
		names = append(names, n)
	}
	slices.Sort(names)
	d, _ := json.Marshal(dir)
	l, _ := json.Marshal(names) // a JSON string list is a valid Python literal
	return fmt.Sprintf("import os as _kera_os\n_kera_os.makedirs(%s, exist_ok=True)\n_kera_os.chdir(%s)\n_kera_os.umask(0)\n"+
		"ATTACHMENTS = %s\ndel _kera_os\n%s", d, d, l, extra)
}

// rewritePaths points absolute paths under the shared kernel folder at the run
// folder. ponytail: textual; models trained on other sandboxes write
// '/mnt/data/x.xlsx' whatever the prompt says, and only the run folder is
// harvested. A path built at runtime (os.path.join('/mnt', 'data')) escapes it.
func rewritePaths(code, shared, run string) string {
	if shared == run || shared == "" {
		return code
	}
	shared = strings.TrimRight(shared, "/")
	r := strings.NewReplacer(shared+"/", run+"/", `"`+shared+`"`, `"`+run+`"`, "'"+shared+"'", "'"+run+"'")
	return r.Replace(code)
}

// clip keeps the head and tail of long output.
func clip(s string) string {
	if len(s) <= maxOutputChars {
		return s
	}
	head, tl := maxOutputChars*2/3, maxOutputChars/3
	return validUTF8(s[:head]) + "\n[... output cut ...]\n" + validUTF8(s[len(s)-tl:])
}

// tail keeps the last lines of a traceback (and at most maxChars).
func tail(s string, lines, maxChars int) string {
	parts := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(parts) > lines {
		// the first line is "Ename: evalue"; keep it above the tail
		parts = append([]string{parts[0], "..."}, parts[len(parts)-lines:]...)
	}
	out := strings.Join(parts, "\n")
	if len(out) > maxChars {
		out = validUTF8(out[len(out)-maxChars:])
	}
	return out
}

func validUTF8(s string) string { return strings.ToValidUTF8(s, "") }

// stamp identifies a staged input so an overwritten input counts as output.
type stamp struct {
	size int64
	mod  time.Time
}

// stage creates the run folder and copies the readable attachments into it.
func (p *Provider) stage(ctx context.Context, u *auth.User, fileIDs []string, dir string) (map[string]stamp, error) {
	runs := filepath.Dir(dir)
	if err := os.MkdirAll(runs, 0o711); err != nil {
		return nil, fmt.Errorf("create %s: %w", runs, err)
	}
	if err := os.Mkdir(dir, 0o777); err != nil {
		return nil, fmt.Errorf("create run folder: %w", err)
	}
	// The kernel runs as another uid: the folder is world-writable, its parent
	// is not listable, and the name is unguessable.
	if err := os.Chmod(dir, 0o777); err != nil {
		return nil, err
	}
	inputs := map[string]stamp{}
	for _, id := range fileIDs {
		if ok, err := p.Files.CanRead(ctx, u, id); err != nil || !ok {
			continue
		}
		name, err := p.copyIn(ctx, id, dir, inputs)
		if err != nil {
			slog.Warn("sandbox stage file", "file", id, "err", err)
			continue
		}
		fi, err := os.Stat(filepath.Join(dir, name))
		if err != nil {
			return nil, err
		}
		inputs[name] = stamp{fi.Size(), fi.ModTime()}
	}
	return inputs, nil
}

func (p *Provider) copyIn(ctx context.Context, id, dir string, taken map[string]stamp) (string, error) {
	rc, f, err := p.Files.Open(ctx, id)
	if err != nil {
		return "", err
	}
	defer rc.Close()
	name := uniqueName(files.SanitizeName(f.Filename), taken)
	dst, err := os.OpenFile(filepath.Join(dir, name), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o666)
	if err != nil {
		return "", err
	}
	if _, err := dst.ReadFrom(rc); err != nil {
		dst.Close()
		return "", err
	}
	if err := dst.Close(); err != nil {
		return "", err
	}
	return name, os.Chmod(filepath.Join(dir, name), 0o666)
}

func uniqueName(name string, taken map[string]stamp) string {
	if name == "" || strings.HasPrefix(name, ".") {
		name = "file" + name
	}
	if _, dup := taken[name]; !dup {
		return name
	}
	ext := filepath.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	for i := 2; ; i++ {
		n := fmt.Sprintf("%s (%d)%s", stem, i, ext)
		if _, dup := taken[n]; !dup {
			return n
		}
	}
}

// harvest uploads every new (or overwritten) regular file in dir as a file of
// the user. os.Root keeps symlinks the code may have planted from reaching
// anything outside the run folder.
func (p *Provider) harvest(ctx context.Context, c tools.Call, dir string, inputs map[string]stamp, maxSize int64) ([]files.File, []string, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, nil, err
	}
	defer root.Close()
	var got []files.File
	var skipped []string
	err = fs.WalkDir(root.FS(), ".", func(rel string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // unreadable entry: skip it, keep walking
		}
		base := d.Name()
		if rel != "." && (strings.HasPrefix(base, ".") || base == "__pycache__") {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() || !d.Type().IsRegular() {
			return nil
		}
		f, why := p.upload(ctx, c, root, rel, inputs, maxSize, len(got))
		switch {
		case why != "":
			skipped = append(skipped, rel+" ("+why+")")
		case f != nil:
			got = append(got, *f)
		}
		return nil
	})
	return got, skipped, err
}

// upload stores one harvested file; it returns nil, "" for unchanged inputs
// and a reason when the file is skipped.
func (p *Provider) upload(ctx context.Context, c tools.Call, root *os.Root, rel string, inputs map[string]stamp, maxSize int64, n int) (*files.File, string) {
	// O_NONBLOCK: a FIFO swapped in by a leftover process must not hang the open.
	fh, err := root.OpenFile(rel, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, "unreadable"
	}
	defer fh.Close()
	fi, err := fh.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		return nil, "not a regular file"
	}
	if in, ok := inputs[rel]; ok && in.size == fi.Size() && in.mod.Equal(fi.ModTime()) {
		return nil, ""
	}
	if fi.Size() > maxSize {
		return nil, fmt.Sprintf("larger than %d MB", maxSize>>20)
	}
	if n >= maxHarvest {
		return nil, fmt.Sprintf("only %d files are returned per run", maxHarvest)
	}
	f, err := p.Files.Create(ctx, c.User.ID, path.Base(rel), "", fh, meta(c))
	if err != nil {
		if errors.Is(err, files.ErrTooLarge) {
			return nil, "too large"
		}
		slog.Warn("sandbox upload", "file", rel, "err", err)
		return nil, "upload failed"
	}
	return &f, ""
}
