package pipeline

// `kera runner`: pulls queued runs from Kera, writes the stored script into the pipeline's workspace,
// runs it and reports through the ordinary run API with a job token. It is the only place that
// executes scripts; it is started as its own container (see deploy/runner) with the mounts the
// scripts need and nothing else.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/cdmx-in/kera/internal/version"
)

const (
	runnerUsage = `  kera runner             run scripts that Kera queues (Admin > Pipelines): reads KERA_URL, KERA_RUNNER_TOKEN, KERA_RUNNER_WORK (default /work)
`
	scriptLimit = 3 * time.Hour // the server closes a run after this too
	minMaskLen  = 6             // shorter values are not masked in logs: they would hide ordinary words
)

// RunnerUsage is the help text of `kera runner`.
const RunnerUsage = runnerUsage

type runner struct {
	url, token, work, host string
	tm                     timing
	wait                   time.Duration // how long one claim waits for work
	cancelEvery            time.Duration
	out, errw              io.Writer
	hc                     *http.Client
}

// RunnerCommand is `kera runner`: it claims and runs jobs until ctx ends.
func RunnerCommand(ctx context.Context, stdout, stderr io.Writer) error {
	host, _ := os.Hostname()
	rn := &runner{url: strings.TrimRight(os.Getenv("KERA_URL"), "/"), token: os.Getenv("KERA_RUNNER_TOKEN"), work: os.Getenv("KERA_RUNNER_WORK"),
		host: host, tm: defaultTiming, wait: claimWaitMax, cancelEvery: 5 * time.Second, out: stdout, errw: stderr, hc: &http.Client{Timeout: claimWaitMax + 20*time.Second}}
	if rn.work == "" {
		rn.work = "/work"
	}
	if rn.url == "" || rn.token == "" {
		return errors.New("kera runner: KERA_URL and KERA_RUNNER_TOKEN must be set (create the token under Admin > Pipelines > Runners)")
	}
	fmt.Fprintf(stdout, "kera runner %s: waiting for work from %s (workspace %s)\n", version.Version, rn.url, rn.work)
	wait := time.Second
	for ctx.Err() == nil {
		_, err := rn.once(ctx)
		if err == nil {
			wait = time.Second
			continue
		}
		fmt.Fprintf(stderr, "kera runner: %v (retrying in %s)\n", err, wait)
		select {
		case <-ctx.Done():
		case <-time.After(wait):
		}
		wait = min(wait*2, 30*time.Second)
	}
	return nil
}

// once asks for a job and, when there is one, runs it to the end. It reports whether a job ran.
func (rn *runner) once(ctx context.Context) (bool, error) {
	j, err := rn.claim(ctx)
	if err != nil || j == nil {
		return false, err
	}
	rn.exec(ctx, j)
	return true, nil
}

func (rn *runner) claim(ctx context.Context) (*job, error) {
	body, _ := json.Marshal(claimIn{Version: version.Version, Host: rn.host, Wait: int(rn.wait / time.Second)})
	req, err := http.NewRequestWithContext(ctx, "POST", rn.url+"/api/v1/runner/claim", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+rn.token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := rn.hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("cannot reach Kera: %w", err)
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusNoContent:
		return nil, nil
	case http.StatusOK:
		j := &job{}
		return j, json.NewDecoder(resp.Body).Decode(j)
	case http.StatusUnauthorized:
		return nil, errors.New("Kera refused the runner token (rotated or deleted?)")
	}
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
	return nil, fmt.Errorf("claim: HTTP %d %s", resp.StatusCode, strings.TrimSpace(string(b)))
}

// maskValues replaces every long-enough environment value in s: a script that prints its own secret must not
// put it in the run log. (The server masks key shapes as well; this one knows the actual values.)
func maskValues(s string, vals []string) string {
	for _, v := range vals {
		if strings.Contains(s, v) {
			s = strings.ReplaceAll(s, v, "***")
		}
	}
	return s
}

// safeJoin joins a script file name under dir and refuses anything that leaves it.
func safeJoin(dir, name string) (string, error) {
	p := filepath.Join(dir, filepath.FromSlash(name))
	if rel, err := filepath.Rel(dir, p); err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(name) {
		return "", fmt.Errorf("file name %q leaves the script folder", name)
	}
	return p, nil
}

func (rn *runner) exec(ctx context.Context, j *job) {
	c, err := newClient(cliEnv{URL: rn.url, Pipeline: j.Pipeline, Token: j.Token}, "")
	if err != nil {
		fmt.Fprintf(rn.errw, "kera runner: run %d: %v\n", j.Run, err)
		return
	}
	rep := newReporter(c, j.Run, rn.tm, rn.errw)
	started := startOut{Run: j.Run}
	var vals []string
	for _, n := range j.Secrets { // only what an admin marked secret: a plain value such as a region name stays readable
		if v := j.Env[n]; len(v) >= minMaskLen {
			vals = append(vals, v)
		}
	}
	vals = append(vals, j.Token)
	mask := func(s string) string { return maskValues(s, vals) }
	fmt.Fprintf(rn.out, "kera runner: run %d of %s, script version %d\n", j.Run, j.Pipeline, j.Version)

	stop, loopDone := make(chan struct{}), make(chan struct{})
	go rep.loop(context.WithoutCancel(ctx), stop, loopDone)
	end := func(runErr string) {
		close(stop)
		<-loopDone
		finishRun(ctx, c, rep, started, runErr, rn.out)
	}
	failStart := func(msg string) {
		fmt.Fprintf(rn.errw, "kera runner: run %d: %s\n", j.Run, msg)
		rep.add(stepIn{Name: "start", Status: "failed", Error: msg})
		end("Failed at step start: " + msg)
	}

	if !slugRe.MatchString(j.Pipeline) {
		failStart("bad pipeline name")
		return
	}
	dir := filepath.Join(rn.work, j.Pipeline)
	sdir := filepath.Join(dir, ".kera-script")
	if err := os.RemoveAll(sdir); err != nil {
		failStart("cannot clear the script folder: " + err.Error())
		return
	}
	for name, body := range j.Files {
		p, err := safeJoin(sdir, name)
		if err == nil {
			err = os.MkdirAll(filepath.Dir(p), 0o755)
		}
		if err == nil {
			err = os.WriteFile(p, []byte(body), 0o644)
		}
		if err != nil {
			failStart("cannot write the script: " + err.Error())
			return
		}
	}
	entry, err := safeJoin(sdir, j.Entry)
	if err != nil {
		failStart(err.Error())
		return
	}
	_ = os.Chmod(entry, 0o755)

	var argv []string
	switch ext := strings.ToLower(filepath.Ext(j.Entry)); ext {
	case ".sh", ".bash":
		argv = []string{"bash", entry}
	case ".py":
		py := "python3"
		if _, ok := j.Files["requirements.txt"]; ok {
			if py, err = rn.venv(ctx, sdir, j.Files["requirements.txt"], rep, mask); err != nil {
				failStart("python packages: " + err.Error())
				return
			}
		}
		argv = []string{py, entry}
	default:
		failStart(fmt.Sprintf("do not know how to start %q: use a .sh or .py file", j.Entry))
		return
	}
	argv = append(argv, strings.Fields(j.Args)...)

	mark, err := newMark()
	if err != nil {
		failStart(err.Error())
		return
	}
	// the environment is built from nothing: the runner's own token and variables are not the script's business
	env := []string{"PATH=" + os.Getenv("PATH"), "HOME=" + dir, "KERA_SCRIPT_DIR=" + sdir, "LANG=C.UTF-8", "TZ=" + os.Getenv("TZ"),
		"KERA_URL=" + rn.url, "KERA_PIPELINE=" + j.Pipeline, "KERA_PIPELINE_TOKEN=" + j.Token, fmt.Sprintf("KERA_RUN=%d", j.Run), "KERA_MARK=" + mark}
	names := make([]string, 0, len(j.Env))
	for n := range j.Env {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		env = append(env, n+"="+j.Env[n])
	}

	var outMu sync.Mutex
	p := newParser(mark, func(s stepIn) { s.Log, s.Error = mask(s.Log), mask(s.Error); rep.add(s) },
		func(line string) { outMu.Lock(); fmt.Fprintln(rn.out, mask(line)); outMu.Unlock() })
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir, cmd.Env = dir, env
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} // so a cancel stops the script's children too
	pr, pw, err := os.Pipe()
	if err != nil {
		failStart("pipe: " + err.Error())
		return
	}
	cmd.Stdout, cmd.Stderr = pw, pw
	startErr := cmd.Start()
	pw.Close()
	if startErr != nil {
		pr.Close()
		failStart(fmt.Sprintf("could not start %q: %v", argv[0], startErr))
		return
	}
	readDone := make(chan struct{})
	go func() { defer close(readDone); readLines(pr, p.line) }()

	var cancelled, timedOut bool
	var mu sync.Mutex
	killGroup := func(sig syscall.Signal) { _ = syscall.Kill(-cmd.Process.Pid, sig) }
	cmdDone := make(chan struct{})
	go func() { // an admin's cancel, the 3 hour cap and a runner shutdown all end the script the same way
		tick := time.NewTicker(rn.cancelEvery)
		defer tick.Stop()
		deadline := time.After(scriptLimit)
		for {
			select {
			case <-cmdDone:
				return
			case <-ctx.Done():
				killGroup(syscall.SIGTERM)
				return
			case <-deadline:
				mu.Lock()
				timedOut = true
				mu.Unlock()
				killGroup(syscall.SIGTERM)
				time.AfterFunc(10*time.Second, func() { killGroup(syscall.SIGKILL) })
				return
			case <-tick.C:
				gctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
				r, err := c.getRun(gctx, j.Run)
				cancel()
				if err == nil && r.Cancelling {
					mu.Lock()
					cancelled = true
					mu.Unlock()
					killGroup(syscall.SIGTERM)
					time.AfterFunc(10*time.Second, func() { killGroup(syscall.SIGKILL) })
					return
				}
			}
		}
	}()

	_ = cmd.Wait()
	close(cmdDone)
	select { // a background process that keeps the pipe open is cut off 2 s after the script ends
	case <-readDone:
	case <-time.After(2 * time.Second):
		pr.Close()
		<-readDone
	}
	pr.Close()
	code := exitCode(cmd.ProcessState)
	runErr := p.end(code)
	mu.Lock()
	switch {
	case cancelled:
		runErr = "Cancelled by an admin."
	case timedOut:
		runErr = "The script was still running after 3 hours and was stopped."
	}
	mu.Unlock()
	end(runErr)
}

// venv makes (or reuses) a virtual environment for a requirements.txt, keyed by its content, so the packages are
// installed once and then reused by every later run of the same script.
func (rn *runner) venv(ctx context.Context, sdir, reqs string, rep *reporter, mask func(string) string) (string, error) {
	sum := sha256.Sum256([]byte(reqs))
	dir := filepath.Join(rn.work, ".venvs", hex.EncodeToString(sum[:])[:16])
	py := filepath.Join(dir, "bin", "python")
	if _, err := os.Stat(filepath.Join(dir, ".ok")); err == nil {
		return py, nil
	}
	rep.add(stepIn{Name: "setup", Status: "running", Log: "installing packages from requirements.txt\n"})
	_ = os.RemoveAll(dir)
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return "", err
	}
	for _, args := range [][]string{{"python3", "-m", "venv", dir}, {py, "-m", "pip", "install", "--disable-pip-version-check", "-q", "-r", filepath.Join(sdir, "requirements.txt")}} {
		cmd := exec.CommandContext(ctx, args[0], args[1:]...)
		out, err := cmd.CombinedOutput()
		if len(out) > 0 {
			rep.add(stepIn{Name: "setup", Log: mask(tail(string(out), 4000)) + "\n"})
		}
		if err != nil {
			rep.add(stepIn{Name: "setup", Status: "failed", Error: "pip install failed"})
			_ = os.RemoveAll(dir)
			return "", err
		}
	}
	_ = os.WriteFile(filepath.Join(dir, ".ok"), nil, 0o644)
	rep.add(stepIn{Name: "setup", Status: "ok"})
	return py, nil
}

func tail(s string, n int) string {
	if len(s) > n {
		return "…" + s[len(s)-n:]
	}
	return strings.TrimRight(s, "\n")
}
