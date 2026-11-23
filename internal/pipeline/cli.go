package pipeline

// The command line half of pipelines: `kera pipeline run` wraps a script and
// reports its steps and logs; `kera pipeline push` makes a collection match a
// folder. Both talk to the token routes over HTTP and read only these
// environment variables (no config.Load, no database), like `kera health`:
//
//	KERA_URL             the server, e.g. https://kera.example.com
//	KERA_PIPELINE        the pipeline's name
//	KERA_PIPELINE_TOKEN  the pipeline's token
//	KERA_RUN             set by `run` for the child: the open run
//	KERA_MARK            set by `run` for the child: the marker prefix

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/cdmx-in/kera/internal/files"
	"github.com/cdmx-in/kera/internal/knowledge"
	"github.com/cdmx-in/kera/internal/version"
)

// CLIUsage is the help text of `kera pipeline`.
const CLIUsage = `  kera pipeline run [--name NAME] -- COMMAND [ARGS...]
                          run COMMAND as a pipeline run: its step markers and output are reported to Kera
  kera pipeline push --collection NAME --dir DIR [--wait 15m] [--dry-run]
                          make a collection match a folder (new and changed files are uploaded, missing ones removed)
`

// ExitError ends the process with Code. Msg, when set, is printed first.
type ExitError struct {
	Code int
	Msg  string
}

func (e *ExitError) Error() string {
	if e.Msg != "" {
		return e.Msg
	}
	return "exit status " + strconv.Itoa(e.Code)
}

// exitBusy is the exit code of a run that was not started because another one is open (EX_TEMPFAIL).
const exitBusy = 75

// cliEnv is what the command reads from its environment.
type cliEnv struct {
	URL, Pipeline, Token, Run, Mark string
	Environ                         []string // the environment the child process gets
}

func osEnv() cliEnv {
	return cliEnv{URL: os.Getenv("KERA_URL"), Pipeline: os.Getenv("KERA_PIPELINE"), Token: os.Getenv("KERA_PIPELINE_TOKEN"),
		Run: os.Getenv("KERA_RUN"), Mark: os.Getenv("KERA_MARK"), Environ: os.Environ()}
}

// timing holds the intervals tests shorten. poll is the first wait between two
// looks at a run; it doubles up to 10 s. hang is the longest the script waits
// for a Kera that accepts the connection and never answers (a proxy or firewall
// that swallows packets): the run start and each report get that long, the
// closing calls twice that. Refused connections fail at once and need no limit.
type timing struct {
	flush, beat, poll, hang time.Duration
}

var defaultTiming = timing{flush: 5 * time.Second, beat: 60 * time.Second, poll: 500 * time.Millisecond, hang: 20 * time.Second}

// hangFor is n times the hang limit (the default for a timing that sets none).
func (tm timing) hangFor(n int) time.Duration {
	if tm.hang <= 0 {
		tm.hang = defaultTiming.hang
	}
	return time.Duration(n) * tm.hang
}

// retryDelay is the first backoff; it doubles. A variable so tests do not wait.
var retryDelay = 500 * time.Millisecond

// Command runs `kera pipeline ...` and returns an *ExitError for an exit code.
func Command(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	return command(ctx, args, osEnv(), defaultTiming, stdout, stderr)
}

func command(ctx context.Context, args []string, e cliEnv, tm timing, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: kera pipeline run|push")
	}
	switch args[0] {
	case "run":
		return runCmd(ctx, args[1:], e, tm, stdout, stderr)
	case "push":
		return pushCmd(ctx, args[1:], e, tm, stdout, stderr)
	}
	return fmt.Errorf("unknown pipeline command %q (run or push)", args[0])
}

// HTTP client ---------------------------------------------------------------------

type client struct {
	base, name, token string
	hc                *http.Client
}

// apiErr is an HTTP error answer.
type apiErr struct {
	Status int
	Code   string
	Detail string
	Body   []byte
}

func (e *apiErr) Error() string { return fmt.Sprintf("HTTP %d: %s", e.Status, e.Detail) }

func newClient(e cliEnv, name string) (*client, error) {
	if name == "" {
		name = e.Pipeline
	}
	var missing []string
	for _, v := range []struct{ name, val string }{{"KERA_URL", e.URL}, {"KERA_PIPELINE (or --name)", name}, {"KERA_PIPELINE_TOKEN", e.Token}} {
		if v.val == "" {
			missing = append(missing, v.name)
		}
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("not set: %s", strings.Join(missing, ", "))
	}
	return &client{base: strings.TrimRight(e.URL, "/"), name: name, token: e.Token, hc: &http.Client{}}, nil
}

// unreachable is true for a transport error or a server error: Kera cannot be asked right now.
func unreachable(err error) bool {
	var ae *apiErr
	return err != nil && (!errors.As(err, &ae) || ae.Status >= 500)
}

func jsonBody(v any) func() (io.Reader, error) {
	return func() (io.Reader, error) {
		b, err := json.Marshal(v)
		return bytes.NewReader(b), err
	}
}

// do sends one request and retries a transport error or a 5xx answer twice more
// with a doubling backoff. out, when set, receives the JSON answer.
func (c *client) do(ctx context.Context, method, path string, body func() (io.Reader, error), size int64, out any, timeout time.Duration) error {
	var last error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(retryDelay << (attempt - 1)):
			}
		}
		last = c.once(ctx, method, path, body, size, out, timeout)
		if last == nil || !unreachable(last) || ctx.Err() != nil {
			return last
		}
	}
	return last
}

func (c *client) once(ctx context.Context, method, path string, body func() (io.Reader, error), size int64, out any, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var rd io.Reader
	if body != nil {
		var err error
		if rd, err = body(); err != nil {
			return fmt.Errorf("prepare %s %s: %w", method, path, err)
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+"/api/v1/pipelines/"+url.PathEscape(c.name)+path, rd)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	if size >= 0 && body != nil {
		req.ContentLength = size
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("User-Agent", "kera-pipeline/"+version.Version)
	if method != "PUT" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return fmt.Errorf("%s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return fmt.Errorf("%s %s: read answer: %w", method, path, err)
	}
	if resp.StatusCode >= 400 {
		e := &apiErr{Status: resp.StatusCode, Body: b}
		var j struct{ Detail, Code string }
		_ = json.Unmarshal(b, &j)
		e.Code, e.Detail = j.Code, j.Detail
		if e.Detail == "" {
			e.Detail = strings.TrimSpace(string(b[:min(len(b), 200)]))
		}
		return e
	}
	if out != nil {
		if err := json.Unmarshal(b, out); err != nil {
			return fmt.Errorf("%s %s: bad answer: %w", method, path, err)
		}
	}
	return nil
}

func (c *client) post(ctx context.Context, path string, in, out any) error {
	return c.do(ctx, "POST", path, jsonBody(in), -1, out, time.Minute)
}

type startOut struct {
	Run     int64  `json:"run"`
	URL     string `json:"url"`
	Version string `json:"version"`
}

func (c *client) start(ctx context.Context, in startIn) (startOut, error) {
	var out startOut
	return out, c.post(ctx, "/runs", in, &out)
}

func (c *client) steps(ctx context.Context, run int64, items []stepIn) error {
	if items == nil {
		items = []stepIn{}
	}
	return c.post(ctx, fmt.Sprintf("/runs/%d/steps", run), map[string]any{"steps": items}, nil)
}

func (c *client) finish(ctx context.Context, run int64, errText string) (runOut, error) {
	var out runOut
	return out, c.post(ctx, fmt.Sprintf("/runs/%d/finish", run), map[string]any{"error": errText}, &out)
}

func (c *client) getRun(ctx context.Context, run int64) (runOut, error) {
	var out runOut
	return out, c.do(ctx, "GET", fmt.Sprintf("/runs/%d", run), nil, -1, &out, time.Minute)
}

// busy prints who holds the pipeline and returns the exit-75 error, when err is a 409 run_active.
func busy(err error, w io.Writer) error {
	var ae *apiErr
	if !errors.As(err, &ae) || ae.Status != http.StatusConflict || ae.Code != "run_active" {
		return nil
	}
	fmt.Fprintln(w, "kera pipeline: not started: "+ae.Detail)
	return &ExitError{Code: exitBusy}
}

// warnVersion says when the server is newer than this binary.
func warnVersion(server string, stderr io.Writer) {
	if newer(server, version.Version) {
		fmt.Fprintf(stderr, "kera pipeline: the server is version %s, this command is %s; update it when you can\n", server, version.Version)
	}
}

// newer reports whether version a is higher than b ("v1.2.3" or "1.2.3-rc1"; anything else is not comparable).
func newer(a, b string) bool {
	pa, ok1 := semver(a)
	pb, ok2 := semver(b)
	return ok1 && ok2 && slices.Compare(pa[:], pb[:]) > 0
}

func semver(v string) (out [3]int, ok bool) {
	v = strings.TrimPrefix(v, "v")
	v, _, _ = strings.Cut(v, "-")
	v, _, _ = strings.Cut(v, "+")
	parts := strings.Split(v, ".")
	if len(parts) != 3 {
		return out, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			return out, false
		}
		out[i] = n
	}
	return out, true
}

// Marker parser -------------------------------------------------------------------

// parser turns the child's output lines into step reports. A marker is a line
// that starts with the run's random mark: the nonce keeps text the script
// fetched (a web page, a ticket) from forging one.
//
//	<mark>steps a b c   declare steps, shown pending
//	<mark>step NAME     start NAME; the step before it ends ok unless it was marked
//	<mark>warn TEXT     the current step gets a warning; the script goes on
//	<mark>fail TEXT     the current step fails; the script goes on
//	<mark>skip TEXT     the current step is skipped
//	<mark>count k=N     set a count on the current step
//	<mark>end           the script is done and only keeps its verdict for the exit
//	                    (`exit 1` after fail markers); the current step ends ok
//
// Every other line is a log line of the current step; lines before the first
// marker belong to an implicit step "start". A step that is closed (ended or
// marked) is not re-entered: `step NAME` for it is ignored.
type parser struct {
	mark  string
	cur   string            // the current step, "" before the first line
	state map[string]string // step -> pending | running | ok | warning | failed | skipped
	order []string          // steps in order of first appearance
	errs  map[string]string // failed step -> its error text
	ended bool              // the script printed "end"
	sink  func(stepIn)      // receives every report
	echo  func(string)      // receives what the wrapper prints
}

func newParser(mark string, sink func(stepIn), echo func(string)) *parser {
	return &parser{mark: mark, state: map[string]string{}, errs: map[string]string{}, sink: sink, echo: echo}
}

func (p *parser) line(text string) {
	if p.mark != "" {
		if rest, ok := strings.CutPrefix(text, p.mark); ok {
			verb, arg, _ := strings.Cut(rest, " ")
			if p.marker(verb, strings.TrimSpace(arg)) {
				return
			}
		}
	}
	p.echo(text)
	p.ensure()
	p.sink(stepIn{Name: p.cur, Log: text + "\n"})
}

// marker handles one marker; false means the line is not one (an unknown verb
// or a malformed count) and is logged as plain text.
func (p *parser) marker(verb, arg string) bool {
	switch verb {
	case "steps":
		for _, n := range strings.Fields(arg) {
			if p.seen(n) {
				continue
			}
			p.state[n] = "pending"
			p.order = append(p.order, n)
			p.sink(stepIn{Name: n, Status: "pending"})
		}
		return true
	case "step":
		name, _, _ := strings.Cut(arg, " ")
		if name == "" {
			return false
		}
		if closed(p.state[name]) || name == p.cur {
			return true // re-entering a closed step is ignored
		}
		p.echo("[step] " + name)
		p.closeCur()
		p.open(name)
		return true
	case "warn", "fail", "skip":
		status := map[string]string{"warn": "warning", "fail": "failed", "skip": "skipped"}[verb]
		p.echo("[" + verb + "] " + arg)
		p.ensure()
		p.verdict(p.cur, status, arg)
		return true
	case "count":
		k, v, ok := strings.Cut(arg, "=")
		n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
		if !ok || err != nil || !countKeyRe.MatchString(strings.TrimSpace(k)) {
			return false
		}
		p.ensure()
		p.sink(stepIn{Name: p.cur, Counts: map[string]int64{strings.TrimSpace(k): n}})
		return true
	case "end":
		p.closeCur()
		p.ended = true
		return true
	}
	return false
}

func (p *parser) seen(n string) bool { _, ok := p.state[n]; return ok }

func (p *parser) open(name string) {
	if !slices.Contains(p.order, name) {
		p.order = append(p.order, name)
	}
	p.cur, p.state[name] = name, "running"
	p.sink(stepIn{Name: name, Status: "running"})
}

// ensure makes sure there is a current step (the implicit "start").
func (p *parser) ensure() {
	if p.cur == "" {
		p.open("start")
	}
}

// closeCur ends the current step ok, unless a marker already closed it.
func (p *parser) closeCur() {
	if p.cur != "" && !closed(p.state[p.cur]) {
		p.state[p.cur] = "ok"
		p.sink(stepIn{Name: p.cur, Status: "ok"})
	}
}

// verdict gives a step a verdict; a worse verdict replaces a milder one.
func (p *parser) verdict(name, status, text string) {
	if rank(status) > rank(p.state[name]) {
		p.state[name] = status
	}
	if status == "failed" {
		p.errs[name] = strings.TrimSpace(p.errs[name] + " " + text)
	}
	p.sink(stepIn{Name: name, Status: status, Error: text})
}

// end settles the steps after the command exited with code and returns the
// run's error ("" when it succeeded). A non-zero exit fails the step the script
// died in. The one exception is a script that already failed a step and then
// printed "end": it exits non-zero to keep its verdict, the work is done, and
// only the failed steps are blamed. Without "end" the script may just have
// crashed after the soft failure, so the current step is blamed too.
func (p *parser) end(code int) string {
	if code == 0 {
		p.closeCur()
		return ""
	}
	var failed []string
	for _, n := range p.order {
		if p.state[n] == "failed" {
			failed = append(failed, n)
		}
	}
	if len(failed) == 0 || (!p.ended && p.cur != "" && p.state[p.cur] != "failed") {
		p.ensure()
		p.verdict(p.cur, "failed", fmt.Sprintf("exit code %d", code))
		failed = append(failed, p.cur)
	}
	p.closeCur()
	var parts []string
	for _, n := range failed {
		parts = append(parts, strings.TrimRight(fmt.Sprintf("Failed at step %s: %s", n, p.errs[n]), ": .")+".")
	}
	return strings.Join(parts, " ")
}

// Reporter ------------------------------------------------------------------------

// reporter batches step reports and sends them: every flush interval, as soon as
// 16 KiB are waiting, and as an empty heartbeat when nothing else went out for the beat interval.
type reporter struct {
	c      *client
	run    int64
	tm     timing
	stderr io.Writer

	mu      sync.Mutex // guards items, size, dropped, dead
	items   []stepIn
	size    int
	dropped int
	dead    bool // the server closed the run; stop reporting
	kick    chan struct{}

	fmu      sync.Mutex // serialises flushes, so batches keep their order
	lastSent time.Time
	warned   bool
}

const (
	flushBytes  = 16 << 10
	bufferLimit = 512 << 10 // what is kept while Kera is unreachable
)

func newReporter(c *client, run int64, tm timing, stderr io.Writer) *reporter {
	return &reporter{c: c, run: run, tm: tm, stderr: stderr, kick: make(chan struct{}, 1), lastSent: time.Now()}
}

func (r *reporter) add(in stepIn) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.dead {
		return
	}
	if in.Log != "" && r.size+len(in.Log) > bufferLimit {
		r.dropped++
		return
	}
	if r.dropped > 0 && in.Log != "" {
		r.items = append(r.items, stepIn{Name: in.Name, Log: fmt.Sprintf("... %d log lines were dropped while Kera was unreachable\n", r.dropped)})
		r.dropped = 0
	}
	if n := len(r.items); n > 0 && in.Log != "" && in.Status == "" && in.Error == "" && in.Counts == nil && r.items[n-1].Name == in.Name {
		r.items[n-1].Log += in.Log // consecutive lines of one step travel together
	} else {
		r.items = append(r.items, in)
	}
	r.size += len(in.Log) + 64
	if r.size >= flushBytes {
		select {
		case r.kick <- struct{}{}:
		default:
		}
	}
}

// flush sends what is waiting. A report that cannot be delivered because Kera
// is unreachable stays for the next flush; one the server refuses is dropped.
func (r *reporter) flush(ctx context.Context, heartbeat bool) {
	r.fmu.Lock()
	defer r.fmu.Unlock()
	r.mu.Lock()
	items, dead := r.items, r.dead
	r.items, r.size = nil, 0
	r.mu.Unlock()
	if dead || (len(items) == 0 && !(heartbeat && time.Since(r.lastSent) >= r.tm.beat)) {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, r.tm.hangFor(1))
	defer cancel()
	err := r.c.steps(ctx, r.run, items)
	var ae *apiErr
	switch {
	case err == nil:
		r.lastSent = time.Now()
		return
	case errors.As(err, &ae) && ae.Status == http.StatusConflict && ae.Code == "run_closed":
		r.mu.Lock()
		r.dead = true
		r.mu.Unlock()
		r.warn("Kera closed this run (" + ae.Detail + "); the rest is not reported")
		return
	case !unreachable(err):
		r.warn("a report was refused: " + err.Error())
		return
	}
	r.mu.Lock()
	r.items = append(items, r.items...) // the next flush sends them first
	for _, it := range r.items {
		r.size += len(it.Log) + 64
	}
	r.mu.Unlock()
	r.warn("Kera is unreachable; reports are kept and sent when it is back (" + err.Error() + ")")
}

func (r *reporter) warn(msg string) {
	if !r.warned {
		r.warned = true
		fmt.Fprintln(r.stderr, "kera pipeline: "+msg)
	}
}

// loop flushes until stop is closed.
func (r *reporter) loop(ctx context.Context, stop <-chan struct{}, done chan<- struct{}) {
	defer close(done)
	t := time.NewTicker(r.tm.flush)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			r.flush(ctx, true)
		case <-r.kick:
			r.flush(ctx, false)
		}
	}
}

// run -----------------------------------------------------------------------------

// newMark returns a random marker prefix: "::kera-<32 hex>::".
func newMark() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("random marker: %w", err)
	}
	return "::kera-" + hex.EncodeToString(b) + "::", nil
}

// exitCode is the exit code of a finished process: 128 + the signal when it was killed.
func exitCode(ps *os.ProcessState) int {
	if ws, ok := ps.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		return 128 + int(ws.Signal())
	}
	return ps.ExitCode()
}

func runCmd(ctx context.Context, args []string, e cliEnv, tm timing, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("pipeline run", flag.ContinueOnError)
	fs.SetOutput(stderr)
	name := fs.String("name", "", "pipeline name (default $KERA_PIPELINE)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	argv := fs.Args()
	if len(argv) == 0 {
		return errors.New("usage: kera pipeline run [--name NAME] -- COMMAND [ARGS...]")
	}
	if e.Run != "" {
		return errors.New("kera pipeline run cannot run inside another pipeline run (KERA_RUN is set)")
	}
	mark, err := newMark()
	if err != nil {
		return err
	}
	childEnv := append(slices.Clone(e.Environ), "KERA_MARK="+mark)

	// Kera down, or a wrong token, must not stop the real work: the command runs
	// unreported. Only a run that is already open (exit 75) is a reason not to start.
	var rep *reporter
	var c *client
	var started startOut
	c, err = newClient(e, *name)
	if err == nil {
		sctx, cancel := context.WithTimeout(ctx, tm.hangFor(1))
		started, err = c.start(sctx, startIn{Trigger: "script"})
		cancel()
	}
	if berr := busy(err, stderr); berr != nil {
		return berr
	}
	if err != nil {
		fmt.Fprintf(stderr, "kera pipeline: not reporting this run: %v\n", err)
	} else {
		fmt.Fprintf(stdout, "kera pipeline: run %d %s\n", started.Run, started.URL)
		warnVersion(started.Version, stderr)
		childEnv = append(childEnv, "KERA_RUN="+strconv.FormatInt(started.Run, 10))
		rep = newReporter(c, started.Run, tm, stderr)
	}

	sink := func(stepIn) {}
	if rep != nil {
		sink = rep.add
	}
	var outMu sync.Mutex
	p := newParser(mark, sink, func(s string) { outMu.Lock(); fmt.Fprintln(stdout, s); outMu.Unlock() })

	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Env, cmd.Stdin = childEnv, os.Stdin
	pr, pw, err := os.Pipe()
	if err != nil {
		return fmt.Errorf("pipe: %w", err)
	}
	cmd.Stdout, cmd.Stderr = pw, pw // one pipe: the lines keep the order the command wrote them
	startErr := cmd.Start()
	pw.Close()
	if startErr != nil {
		pr.Close()
		msg := fmt.Sprintf("could not start %q: %v", argv[0], startErr)
		fmt.Fprintln(stderr, "kera pipeline: "+msg)
		if rep != nil { // the run names a step that exists, so the diagram has a red node to open
			rep.add(stepIn{Name: "start", Status: "failed", Error: msg})
		}
		finishRun(ctx, c, rep, started, "Failed at step start: "+msg, stdout)
		return &ExitError{Code: 127}
	}
	cmdDone := make(chan struct{})
	// ponytail: the signal goes to the command only; a process it started keeps
	// running (as under cron before) and its uploads end at run_closed. Upgrade:
	// Setpgid on the command and a signal to its group (a command that reads the
	// terminal is then stopped as a background job, so it is not the default).
	go func() { // a signal for this process is a signal for the command
		select {
		case <-ctx.Done():
			_ = cmd.Process.Signal(syscall.SIGTERM)
		case <-cmdDone:
		}
	}()
	stop, loopDone := make(chan struct{}), make(chan struct{})
	if rep != nil {
		go rep.loop(context.WithoutCancel(ctx), stop, loopDone)
	} else {
		close(loopDone)
	}
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		readLines(pr, p.line)
	}()

	_ = cmd.Wait() // the exit code comes from ProcessState
	close(cmdDone)
	select { // a background process that keeps the pipe open is cut off 2 s after the command exits
	case <-readDone:
	case <-time.After(2 * time.Second):
		pr.Close()
		<-readDone
	}
	pr.Close()
	close(stop)
	<-loopDone
	code := exitCode(cmd.ProcessState)
	runErr := p.end(code)
	finishRun(ctx, c, rep, started, runErr, stdout)
	if code != 0 {
		return &ExitError{Code: code}
	}
	return nil
}

// finishRun sends what is left and closes the run.
func finishRun(ctx context.Context, c *client, rep *reporter, started startOut, runErr string, stdout io.Writer) {
	if rep == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), rep.tm.hangFor(2))
	defer cancel()
	rep.flush(ctx, false)
	out, err := c.finish(ctx, started.Run, runErr)
	switch {
	case err != nil:
		fmt.Fprintf(stdout, "kera pipeline: could not finish run %d: %v\n", started.Run, err)
	case out.FinishedAt == nil:
		fmt.Fprintf(stdout, "kera pipeline: run %d is finishing in Kera (indexing): %s\n", started.Run, started.URL)
	default:
		fmt.Fprintf(stdout, "kera pipeline: run %d %s: %s\n", started.Run, out.Status, started.URL)
	}
}

// readLines calls f with every line of r (without the line ending). A line
// longer than 64 KiB is cut into pieces.
func readLines(r io.Reader, f func(string)) {
	br := bufio.NewReaderSize(r, 64<<10)
	for {
		chunk, err := br.ReadSlice('\n')
		if len(chunk) > 0 || err == nil {
			f(strings.TrimRight(string(chunk), "\r\n"))
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if err != nil {
			return
		}
	}
}

// push ----------------------------------------------------------------------------

// localFile is one file of the folder as the server will see it.
type localFile struct {
	Name, Path, SHA string
	Size            int64
}

// scanDir lists the files to sync. It skips dotfiles and dot folders, symlinks,
// and files Kera cannot extract text from, and says what it skipped. A path with
// folders becomes one flat name ("a/b.md" -> "a--b.md"); two files that end up
// with the same name are an error.
func scanDir(dir string) (list []localFile, skipped []string, err error) {
	st, err := os.Stat(dir)
	if err != nil {
		return nil, nil, fmt.Errorf("--dir: %w", err)
	}
	if !st.IsDir() {
		return nil, nil, fmt.Errorf("--dir %s is not a folder", dir)
	}
	// WalkDir does not follow a link given as its root: it would list nothing.
	if dir, err = filepath.EvalSymlinks(dir); err != nil {
		return nil, nil, fmt.Errorf("--dir: %w", err)
	}
	byName := map[string]string{}
	err = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, path)
		if rel == "." {
			return nil
		}
		rel = filepath.ToSlash(rel)
		switch {
		case strings.HasPrefix(d.Name(), "."):
			skipped = append(skipped, rel+" (hidden)")
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		case d.Type()&fs.ModeSymlink != 0:
			skipped = append(skipped, rel+" (symbolic link)")
			return nil
		case d.IsDir():
			return nil
		case !d.Type().IsRegular():
			skipped = append(skipped, rel+" (not a regular file)")
			return nil
		case !files.Extractable(d.Name()):
			skipped = append(skipped, rel+" (type not supported)")
			return nil
		}
		name := strings.ReplaceAll(rel, "/", "--")
		if files.SanitizeName(name) != name {
			skipped = append(skipped, rel+" (name not allowed: too long or has control characters)")
			return nil
		}
		if prev, dup := byName[name]; dup {
			return fmt.Errorf("%s and %s both become %q; rename one", prev, rel, name)
		}
		byName[name] = rel
		sha, size, err := hashFile(path)
		if err != nil {
			return err
		}
		list = append(list, localFile{Name: name, Path: path, SHA: sha, Size: size})
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	slices.SortFunc(list, func(a, b localFile) int { return strings.Compare(a.Name, b.Name) })
	return list, skipped, nil
}

func hashFile(path string) (sum string, size int64, err error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	size, err = io.Copy(h, f)
	if err != nil {
		return "", 0, fmt.Errorf("read %s: %w", path, err)
	}
	return hex.EncodeToString(h.Sum(nil)), size, nil
}

func pushCmd(ctx context.Context, args []string, e cliEnv, tm timing, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("pipeline push", flag.ContinueOnError)
	fs.SetOutput(stderr)
	name := fs.String("name", "", "pipeline name (default $KERA_PIPELINE)")
	coll := fs.String("collection", "", "collection to sync (a name or id the pipeline writes to)")
	dir := fs.String("dir", "", "folder to sync")
	wait := fs.Duration("wait", 15*time.Minute, "how long to wait for indexing")
	dry := fs.Bool("dry-run", false, "show what would change and change nothing")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *coll == "" || *dir == "" || fs.NArg() > 0 {
		return errors.New("usage: kera pipeline push --collection NAME --dir DIR [--wait 15m] [--dry-run]")
	}
	wrapped := e.Run != ""
	if wrapped && *dry {
		return errors.New("--dry-run cannot be used inside a pipeline run; run it on its own")
	}
	list, skipped, err := scanDir(*dir)
	if err != nil {
		return err
	}
	c, err := newClient(e, *name)
	if err != nil {
		return err
	}
	pu := &pusher{c: c, e: e, tm: tm, coll: *coll, list: list, wait: *wait, dry: *dry, out: stdout, err: stderr}
	if len(skipped) > 0 {
		fmt.Fprintf(stdout, "%s: skipped %d: %s\n", *coll, len(skipped), summarize(skipped, 10))
	}
	if wrapped {
		if pu.run, err = strconv.ParseInt(e.Run, 10, 64); err != nil {
			return fmt.Errorf("KERA_RUN %q is not a run number", e.Run)
		}
		return pu.push(ctx)
	}
	started, err := c.start(ctx, startIn{Trigger: "push", DryRun: *dry})
	if berr := busy(err, stderr); berr != nil {
		return berr
	}
	if err != nil {
		return fmt.Errorf("start run: %w", err)
	}
	fmt.Fprintf(stdout, "kera pipeline: run %d %s\n", started.Run, started.URL)
	warnVersion(started.Version, stderr)
	pu.run, pu.own = started.Run, true
	err = pu.push(ctx)
	if err != nil {
		// the run ends with the reason; an unreachable server cannot be told, and times the run out
		_, _ = c.finish(context.WithoutCancel(ctx), pu.run, "kera pipeline push: "+err.Error())
		return err
	}
	return pu.close(ctx)
}

func summarize(list []string, n int) string {
	if len(list) <= n {
		return strings.Join(list, ", ")
	}
	return strings.Join(list[:n], ", ") + fmt.Sprintf(", and %d more", len(list)-n)
}

type pusher struct {
	c        *client
	e        cliEnv
	tm       timing
	coll     string
	list     []localFile
	wait     time.Duration
	dry, own bool // own: this command opened the run and closes it
	run      int64
	out, err io.Writer
}

type planOut struct {
	Upload    []string `json:"upload"`
	Unchanged []string `json:"unchanged"`
	Received  []string `json:"received"`
	Adopted   []string `json:"adopted"`
	Ambiguous []string `json:"ambiguous"`
	Stale     []string `json:"stale"`
	Refused   string   `json:"refused"`
}

func (p *pusher) sync(ctx context.Context) (planOut, error) {
	files := make([]knowledge.SyncFile, len(p.list))
	for i, f := range p.list {
		files[i] = knowledge.SyncFile{Name: f.Name, SHA256: f.SHA, Size: f.Size}
	}
	var plan planOut
	err := p.c.post(ctx, fmt.Sprintf("/runs/%d/sync", p.run), map[string]any{"collection": p.coll, "files": files, "complete": true}, &plan)
	return plan, err
}

func (p *pusher) put(ctx context.Context, f localFile) error {
	q := url.Values{"collection": {p.coll}, "name": {f.Name}, "sha256": {f.SHA}}
	body := func() (io.Reader, error) { return os.Open(f.Path) }
	return p.c.do(ctx, "PUT", fmt.Sprintf("/runs/%d/files?%s", p.run, q.Encode()), body, f.Size, nil, 10*time.Minute)
}

// push does the sync: list, upload what the server asks for, list again, wait for indexing.
// It returns an error only for a transport or authentication failure, or, outside a
// wrapped run, a refused listing; file-level failures are on the collection's step.
func (p *pusher) push(ctx context.Context) error {
	plan, err := p.sync(ctx)
	if err != nil {
		return p.syncError(err)
	}
	fmt.Fprintf(p.out, "%s: %d files, %d to upload, %d unchanged, adopt %d, remove %d\n", p.coll, len(p.list), len(plan.Upload), len(plan.Unchanged)+len(plan.Received),
		len(plan.Adopted), len(plan.Stale))
	if len(plan.Ambiguous) > 0 {
		fmt.Fprintf(p.out, "%s: not adopted, two files have the name: %s\n", p.coll, summarize(plan.Ambiguous, 10))
	}
	if plan.Refused != "" {
		fmt.Fprintf(p.out, "%s: the removal would be refused: %s (an admin can allow it once)\n", p.coll, plan.Refused)
	}
	if p.dry {
		for _, g := range []struct {
			what  string
			names []string
		}{{"upload", plan.Upload}, {"adopt", plan.Adopted}, {"remove", plan.Stale}} {
			if len(g.names) > 0 {
				fmt.Fprintf(p.out, "  would %s: %s\n", g.what, summarize(g.names, 20))
			}
		}
		return nil
	}
	byName := map[string]localFile{}
	for _, f := range p.list {
		byName[f.Name] = f
	}
	for _, n := range plan.Upload {
		err := p.put(ctx, byName[n])
		var ae *apiErr
		switch {
		case err == nil:
		case errors.As(err, &ae) && ae.Status != http.StatusUnauthorized && ae.Status != http.StatusForbidden && ae.Status < 500 && ae.Code != "run_closed":
			fmt.Fprintf(p.out, "%s: %s was not uploaded: %s\n", p.coll, n, ae.Detail)
		default:
			return fmt.Errorf("upload %s: %w", n, err)
		}
	}
	if _, err := p.sync(ctx); err != nil {
		return p.syncError(err)
	}
	return p.waitIndexed(ctx)
}

// listingRefusals are the answers that say the server will not take this listing, as opposed to a broken connection or token.
var listingRefusals = []string{"not_a_target", "ambiguous_collection", "reindexing", "bad_name", "bad_sha256", "duplicate_name", "too_many_files"}

// syncError decides what a refused listing means: inside a wrapped run it is reported
// as a failure of the script's current step and the script goes on; otherwise it is an error.
func (p *pusher) syncError(err error) error {
	var ae *apiErr
	if !errors.As(err, &ae) || !slices.Contains(listingRefusals, ae.Code) {
		return err // transport, server and authentication errors
	}
	msg := fmt.Sprintf("%s: Kera refused the listing: %s", p.coll, ae.Detail)
	if p.e.Run != "" && p.e.Mark != "" {
		fmt.Fprintf(p.out, "%sfail %s\n", p.e.Mark, msg)
		return nil
	}
	return errors.New(msg)
}

// waitIndexed polls the run until the collection's files are indexed or --wait is over.
func (p *pusher) waitIndexed(ctx context.Context) error {
	deadline := time.Now().Add(p.wait)
	delay := p.tm.poll
	for {
		rv, err := p.c.getRun(ctx, p.run)
		if err != nil {
			return err
		}
		n := p.node(rv)
		if n == nil || (n.Status != "pending" && n.Counts["waiting"] == 0) {
			return nil
		}
		if time.Now().After(deadline) {
			fmt.Fprintf(p.out, "%s: %d files are still being indexed after %s; Kera goes on with them\n", p.coll, n.Counts["waiting"], p.wait)
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay):
		}
		delay = min(delay*2, 10*time.Second)
	}
}

// node finds the run's step for the collection.
func (p *pusher) node(rv runOut) *stepOut {
	if rv.Steps == nil {
		return nil
	}
	for i, st := range *rv.Steps {
		if st.Kera && (st.Name == p.coll || st.Key == p.coll) {
			return &(*rv.Steps)[i]
		}
	}
	return nil
}

// close ends a run that push opened and turns the run's verdict into the exit code.
func (p *pusher) close(ctx context.Context) error {
	ctx = context.WithoutCancel(ctx)
	rv, err := p.c.finish(ctx, p.run, "")
	if err != nil {
		return fmt.Errorf("finish run: %w", err)
	}
	deadline := time.Now().Add(p.wait)
	for rv.FinishedAt == nil && time.Now().Before(deadline) {
		time.Sleep(p.tm.poll)
		if rv, err = p.c.getRun(ctx, p.run); err != nil {
			return err
		}
	}
	if n := p.node(rv); n != nil {
		c := n.Counts
		fmt.Fprintf(p.out, "%s: %d uploaded, %d indexed, %d failed, %d removed\n", p.coll, c["uploaded"], c["indexed"], c["failed"], c["removed"])
	}
	switch {
	case p.dry:
		return nil
	case rv.FinishedAt == nil:
		return &ExitError{Code: 1, Msg: fmt.Sprintf("kera pipeline: run %d is still being indexed; Kera closes it when that is done", p.run)}
	case rv.Status != "ok":
		return &ExitError{Code: 1, Msg: fmt.Sprintf("kera pipeline: run %d %s: %s", p.run, rv.Status, rv.Error)}
	}
	fmt.Fprintf(p.out, "kera pipeline: run %d ok\n", p.run)
	return nil
}
