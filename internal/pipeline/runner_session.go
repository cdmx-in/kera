package pipeline

// The interactive half of `kera runner` (server side: session.go): script notebook sessions, cell output
// streaming and syntax checks. It runs next to the loop that executes scheduled runs, so a notebook session
// never waits for a long run.

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/cdmx-in/kera/internal/version"
)

// post sends a JSON body to Kera with the runner token. out may be nil.
func (rn *runner) post(ctx context.Context, path string, in, out any) (int, error) {
	b, _ := json.Marshal(in)
	req, err := http.NewRequestWithContext(ctx, "POST", rn.url+path, bytes.NewReader(b))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+rn.bearer())
	req.Header.Set("Content-Type", "application/json")
	resp, err := rn.hc.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		rn.reloadToken()
	}
	if resp.StatusCode == http.StatusOK && out != nil {
		return resp.StatusCode, json.NewDecoder(resp.Body).Decode(out)
	}
	io.Copy(io.Discard, resp.Body)
	return resp.StatusCode, nil
}

// interactive asks Kera for notebook and check work until ctx ends.
func (rn *runner) interactive(ctx context.Context) {
	wait := time.Second
	for ctx.Err() == nil {
		var wk work
		code, err := rn.post(ctx, "/api/v1/runner/work", workIn{Version: version.Version, Wait: int(rn.wait / time.Second)}, &wk)
		if err != nil || (code != http.StatusOK && code != http.StatusNoContent) {
			select {
			case <-ctx.Done():
			case <-time.After(wait):
			}
			wait = min(wait*2, 30*time.Second)
			continue
		}
		wait = time.Second
		if code == http.StatusOK {
			rn.dispatch(ctx, &wk)
		}
	}
	rn.closeAllSessions()
}

func (rn *runner) dispatch(ctx context.Context, wk *work) {
	switch wk.Kind {
	case "check":
		go rn.runCheck(ctx, wk.Check)
	case "session":
		go rn.startSession(ctx, wk.Session)
	case "cell":
		if s := rn.session(wk.Cell.Session); s != nil {
			s.queue <- *wk.Cell
		}
	case "stop":
		if s := rn.session(wk.Cell.Session); s != nil {
			s.interrupt(wk.Cell.Cell)
		}
	case "close":
		if s := rn.session(wk.Session.ID); s != nil {
			s.close()
		}
	}
}

func (rn *runner) session(id string) *sess {
	rn.smu.Lock()
	defer rn.smu.Unlock()
	return rn.sessions[id]
}

func (rn *runner) closeAllSessions() {
	rn.smu.Lock()
	list := make([]*sess, 0, len(rn.sessions))
	for _, s := range rn.sessions {
		list = append(list, s)
	}
	rn.smu.Unlock()
	for _, s := range list {
		s.close()
	}
}

// Sessions ----------------------------------------------------------------------------------------------------

type sess struct {
	rn      *runner
	sw      *sessionWork
	dir     string // the session folder (scripts, cell files)
	cwd     string // where cells run
	mark    string
	sent    string // end-of-cell sentinel, unguessable
	vals    []string
	queue   chan cellWork
	mu      sync.Mutex
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	lines   chan string
	curCell int64
	stopReq int64 // the cell an admin asked to stop
	closed  bool
	done    chan struct{}
	py      string
	venvBin string
}

func (rn *runner) startSession(ctx context.Context, sw *sessionWork) {
	fail := func(msg string) {
		_, _ = rn.post(ctx, "/api/v1/runner/sessions/"+sw.ID+"/ready", readyIn{Error: msg}, nil)
	}
	if !slugRe.MatchString(sw.Pipeline) {
		fail("bad pipeline name")
		return
	}
	dir := filepath.Join(rn.work, ".sessions", sw.ID)
	sdir := filepath.Join(dir, "script")
	cwd := filepath.Join(dir, "ws")
	if sw.Real {
		cwd = filepath.Join(rn.work, sw.Pipeline)
	}
	for _, d := range []string{sdir, cwd, filepath.Join(dir, "cells")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			fail("cannot create the session folder: " + err.Error())
			return
		}
	}
	for name, body := range sw.Files {
		p, err := safeJoin(sdir, name)
		if err == nil {
			err = os.MkdirAll(filepath.Dir(p), 0o755)
		}
		if err == nil {
			err = os.WriteFile(p, []byte(body), 0o644)
		}
		if err != nil {
			fail("cannot write the script: " + err.Error())
			return
		}
	}
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	s := &sess{rn: rn, sw: sw, dir: dir, cwd: cwd, sent: "::cell-" + hex.EncodeToString(b) + "::", queue: make(chan cellWork, 64), done: make(chan struct{})}
	mb := make([]byte, 12)
	_, _ = rand.Read(mb)
	s.mark = "::kera-" + hex.EncodeToString(mb) + "::"
	for _, n := range sw.Secrets {
		if v := sw.Env[n]; len(v) >= minMaskLen {
			s.vals = append(s.vals, v)
		}
	}
	s.vals = append(s.vals, sw.Token)
	s.py = "python3"
	if req, ok := sw.Files["requirements.txt"]; ok { // same as a run: the environment is on PATH for shell cells too
		py, err := rn.venv(ctx, sdir, req, newReporter(&client{}, 0, rn.tm, io.Discard), func(x string) string { return x })
		if err != nil {
			fail("python packages: " + err.Error())
			return
		}
		s.py, s.venvBin = py, filepath.Dir(py)
	}
	if err := s.spawn(); err != nil {
		fail(err.Error())
		return
	}
	rn.smu.Lock()
	if rn.sessions == nil {
		rn.sessions = map[string]*sess{}
	}
	rn.sessions[sw.ID] = s
	rn.smu.Unlock()
	if _, err := rn.post(ctx, "/api/v1/runner/sessions/"+sw.ID+"/ready", readyIn{}, nil); err != nil {
		s.close()
		return
	}
	s.loop(ctx)
}

const pyDriver = `import sys, os, json, traceback
g = {"__name__": "__main__"}
sent = sys.argv[1]
sys.path.insert(0, os.environ.get("KERA_SCRIPT_DIR", "."))
for line in sys.stdin:
    req = json.loads(line)
    rc = 0
    try:
        exec(compile(open(req["file"]).read(), req["name"], "exec"), g)
    except SystemExit as e:
        rc = e.code if isinstance(e.code, int) else (0 if e.code is None else 1)
    except KeyboardInterrupt:
        print("KeyboardInterrupt")
        rc = 130
    except BaseException:
        traceback.print_exc()
        rc = 1
    sys.stdout.flush()
    sys.stderr.flush()
    print("%s%d" % (sent, rc), flush=True)
`

// spawn starts the shell or Python process of the session.
func (s *sess) spawn() error {
	sdir := filepath.Join(s.dir, "script")
	env := []string{"PATH=" + pathWith(s.venvBin), "HOME=" + s.cwd, "KERA_SCRIPT_DIR=" + sdir, "LANG=C.UTF-8", "TZ=" + os.Getenv("TZ"), "KERA_URL=" + s.rn.url,
		"KERA_PIPELINE=" + s.sw.Pipeline, "KERA_PIPELINE_TOKEN=" + s.sw.Token, fmt.Sprintf("KERA_RUN=%d", s.sw.Run), "KERA_MARK=" + s.mark, "KERA_TEST=1"}
	for n, v := range s.sw.Env {
		env = append(env, n+"="+v)
	}
	var cmd *exec.Cmd
	if s.sw.Lang == "python" {
		drv := filepath.Join(s.dir, "cells", "driver.py")
		if err := os.WriteFile(drv, []byte(pyDriver), 0o644); err != nil {
			return err
		}
		cmd = exec.Command(s.py, "-u", drv, s.sent)
	} else {
		cmd = exec.Command("bash", "--noprofile", "--norc")
	}
	cmd.Dir, cmd.Env = s.cwd, env
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	in, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	pr, pw, err := os.Pipe()
	if err != nil {
		return err
	}
	cmd.Stdout, cmd.Stderr = pw, pw
	if err := cmd.Start(); err != nil {
		pr.Close()
		pw.Close()
		return fmt.Errorf("could not start %s: %w", cmd.Path, err)
	}
	pw.Close()
	lines := make(chan string, 256)
	go func() {
		defer close(lines)
		defer pr.Close()
		readLines(pr, func(l string) { lines <- l })
	}()
	go func() { _ = cmd.Wait() }()
	s.mu.Lock()
	s.cmd, s.stdin, s.lines = cmd, in, lines
	s.mu.Unlock()
	return nil
}

func (s *sess) killProcess(sig syscall.Signal) {
	s.mu.Lock()
	cmd := s.cmd
	s.mu.Unlock()
	if cmd != nil && cmd.Process != nil {
		_ = syscall.Kill(-cmd.Process.Pid, sig)
	}
}

func (s *sess) close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	close(s.done)
	s.mu.Unlock()
	s.killProcess(syscall.SIGKILL)
	s.rn.smu.Lock()
	delete(s.rn.sessions, s.sw.ID)
	s.rn.smu.Unlock()
	if !s.sw.Real {
		_ = os.RemoveAll(s.dir)
	} else {
		_ = os.RemoveAll(filepath.Join(s.dir, "cells"))
	}
}

// interrupt stops the running cell. Python keeps its state (a KeyboardInterrupt in the cell); a shell cannot be
// interrupted without ending the cell's remaining commands too, so it is restarted and its variables are lost.
func (s *sess) interrupt(cell int64) {
	s.mu.Lock()
	cur := s.curCell
	if cur == cell {
		s.stopReq = cell
	}
	s.mu.Unlock()
	if cur != cell {
		return
	}
	if s.sw.Lang == "python" {
		s.killProcess(syscall.SIGINT)
		return
	}
	s.killProcess(syscall.SIGKILL) // loop() sees the output end, reports the cell stopped and starts a new shell
}

func (s *sess) mask(x string) string { return maskValues(x, s.vals) }

// show turns a step marker line into something readable; other lines are passed on masked.
func (s *sess) show(l string) string {
	if rest, ok := strings.CutPrefix(l, s.mark); ok {
		verb, text, _ := strings.Cut(rest, " ")
		switch verb {
		case "step", "warn", "fail", "skip":
			return "▸ " + verb + ": " + s.mask(text)
		}
		return "▸ " + s.mask(rest)
	}
	return s.mask(l)
}

func (s *sess) loop(ctx context.Context) {
	for {
		select {
		case <-s.done:
			return
		case <-ctx.Done():
			s.close()
			return
		case cw := <-s.queue:
			s.runCell(ctx, cw)
		}
	}
}

var noNul = strings.NewReplacer("\x00", "")

func (s *sess) runCell(ctx context.Context, cw cellWork) {
	base := "/api/v1/runner/sessions/" + s.sw.ID + "/cells/" + strconv.FormatInt(cw.Cell, 10)
	report := func(c cellReport) { _, _ = s.rn.post(context.WithoutCancel(ctx), base, c, nil) }
	s.mu.Lock()
	s.curCell = cw.Cell
	lines, stdin := s.lines, s.stdin
	s.mu.Unlock()
	defer func() { s.mu.Lock(); s.curCell = 0; s.mu.Unlock() }()
	file := filepath.Join(s.dir, "cells", fmt.Sprintf("cell-%d", cw.Cell))
	var req string
	if s.sw.Lang == "python" {
		file += ".py"
		j, _ := json.Marshal(map[string]string{"file": file, "name": firstNonEmpty(cw.Name, fmt.Sprintf("cell-%d", cw.Cell))})
		req = string(j) + "\n"
	} else {
		file += ".sh"
		req = fmt.Sprintf(". %q\n__rc=$?\nprintf '%%s%%d\\n' %q \"$__rc\"\n", file, s.sent)
	}
	if err := os.WriteFile(file, []byte(cw.Code), 0o644); err != nil {
		report(cellReport{Append: "cannot write the cell: " + err.Error() + "\n", Status: "failed"})
		return
	}
	if _, err := io.WriteString(stdin, req); err != nil {
		report(cellReport{Append: "the session process has ended\n", Status: "failed"})
		s.restart(ctx)
		return
	}
	var buf strings.Builder
	flush := time.NewTicker(300 * time.Millisecond)
	defer flush.Stop()
	push := func() {
		if buf.Len() > 0 {
			report(cellReport{Append: buf.String()})
			buf.Reset()
		}
	}
	deadline := time.After(scriptLimit)
	for {
		select {
		case <-flush.C:
			push()
		case <-deadline:
			s.killProcess(syscall.SIGKILL)
		case l, ok := <-lines:
			if !ok { // the process ended: stopped by the admin, or the cell ran `exit`
				push()
				report(cellReport{Status: s.endStatus(cw.Cell)})
				s.restart(ctx)
				return
			}
			if i := strings.Index(l, s.sent); i >= 0 { // the end of the cell; text before it on the line is output without a newline
				if i > 0 {
					buf.WriteString(s.show(noNul.Replace(l[:i])))
				}
				push()
				rc, _ := strconv.Atoi(l[i+len(s.sent):])
				st := "ok"
				if rc != 0 {
					st = "failed"
				}
				if s.endStatus(cw.Cell) == "stopped" { // an interrupted Python cell prints its sentinel too
					st = "stopped"
				}
				report(cellReport{Status: st, ExitCode: &rc})
				return
			}
			buf.WriteString(s.show(noNul.Replace(l)) + "\n")
			if buf.Len() > 8192 {
				push()
			}
		}
	}
}

// endStatus: a cell whose process vanished was stopped if an admin asked, otherwise it ended the session's process itself (exit, a crash).
func (s *sess) endStatus(cell int64) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopReq == cell {
		return "stopped"
	}
	return "failed"
}

// restart starts a fresh process for the next cell (state is lost) unless the session is closing.
func (s *sess) restart(ctx context.Context) {
	s.mu.Lock()
	closed := s.closed
	s.mu.Unlock()
	if closed {
		return
	}
	if err := s.spawn(); err != nil {
		s.close()
		_, _ = s.rn.post(context.WithoutCancel(ctx), "/api/v1/runner/sessions/"+s.sw.ID+"/ready", readyIn{Error: err.Error()}, nil)
	}
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// Checks ------------------------------------------------------------------------------------------------------

var bashErrRe = regexp.MustCompile(`^(?:.*?): line (\d+): (.*)$`)

const pyCheck = `import ast, sys, json
try:
    ast.parse(open(sys.argv[1]).read(), sys.argv[1])
except SyntaxError as e:
    print(json.dumps({"line": e.lineno or 1, "col": e.offset or 0, "message": e.msg}))
`

func (rn *runner) runCheck(ctx context.Context, cw *checkWork) {
	res := checkResult{Problems: []problem{}, Tools: []string{}}
	dir, err := os.MkdirTemp("", "kera-check-")
	if err != nil {
		res.Error = err.Error()
		_, _ = rn.post(ctx, "/api/v1/runner/checks/"+cw.ID, res, nil)
		return
	}
	defer os.RemoveAll(dir)
	tools := map[string]bool{}
	for name, body := range cw.Files {
		p, err := safeJoin(dir, name)
		if err == nil {
			err = os.MkdirAll(filepath.Dir(p), 0o755)
		}
		if err == nil {
			err = os.WriteFile(p, []byte(body), 0o644)
		}
		if err != nil {
			continue
		}
		cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		switch langOf(name) {
		case "bash":
			tools["bash -n"] = true
			out, _ := exec.CommandContext(cctx, "bash", "-n", p).CombinedOutput()
			for _, l := range strings.Split(string(out), "\n") {
				if m := bashErrRe.FindStringSubmatch(l); m != nil {
					n, _ := strconv.Atoi(m[1])
					res.Problems = append(res.Problems, problem{File: name, Line: n, Severity: "error", Message: m[2], Tool: "bash -n"})
				}
			}
			if sc, err := exec.LookPath("shellcheck"); err == nil {
				tools["shellcheck"] = true
				out, _ := exec.CommandContext(cctx, sc, "-f", "json", "-s", "bash", "-e", "SC1091", p).Output()
				var items []struct {
					Line, Column   int
					Level, Message string
					Code           int
				}
				if json.Unmarshal(out, &items) == nil {
					for _, it := range items {
						sev := map[string]string{"error": "error", "warning": "warning"}[it.Level]
						if sev == "" {
							sev = "info"
						}
						res.Problems = append(res.Problems, problem{File: name, Line: it.Line, Col: it.Column, Severity: sev, Message: it.Message, Tool: "shellcheck", Code: fmt.Sprintf("SC%d", it.Code)})
					}
				}
			}
		case "python":
			tools["python syntax"] = true
			out, _ := exec.CommandContext(cctx, "python3", "-c", pyCheck, p).Output()
			var e struct {
				Line, Col int
				Message   string
			}
			if json.Unmarshal(bytes.TrimSpace(out), &e) == nil && e.Message != "" {
				res.Problems = append(res.Problems, problem{File: name, Line: e.Line, Col: e.Col, Severity: "error", Message: e.Message, Tool: "python"})
			}
		}
		cancel()
	}
	for t := range tools {
		res.Tools = append(res.Tools, t)
	}
	if _, err := rn.post(ctx, "/api/v1/runner/checks/"+cw.ID, res, nil); err != nil && !errors.Is(err, context.Canceled) {
		fmt.Fprintf(rn.errw, "kera runner: check result: %v\n", err)
	}
}
