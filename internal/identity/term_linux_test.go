//go:build linux

package identity

import (
	"bytes"
	"context"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"
	"unsafe"
)

// openPTY returns a pseudo-terminal pair: what the CLI sees as its terminal (the slave) and what
// a person's keyboard would be (the master).
func openPTY(t *testing.T) (master, slave *os.File) {
	t.Helper()
	m, err := os.OpenFile("/dev/ptmx", os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Skipf("no pseudo-terminals here: %v", err)
	}
	var unlock, n uint32
	if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, m.Fd(), syscall.TIOCSPTLCK, uintptr(unsafe.Pointer(&unlock))); e != 0 {
		t.Skipf("TIOCSPTLCK: %v", e)
	}
	if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, m.Fd(), syscall.TIOCGPTN, uintptr(unsafe.Pointer(&n))); e != 0 {
		t.Skipf("TIOCGPTN: %v", e)
	}
	s, err := os.OpenFile("/dev/pts/"+itoa(n), os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Skipf("open the slave: %v", err)
	}
	t.Cleanup(func() { m.Close(); s.Close() })
	return m, s
}

func itoa(n uint32) string {
	var b [10]byte
	i := len(b)
	for ; n >= 10; n /= 10 {
		i--
		b[i] = byte('0' + n%10)
	}
	i--
	b[i] = byte('0' + n)
	return string(b[i:])
}

func echoOn(t *testing.T, f *os.File) bool {
	t.Helper()
	var tm syscall.Termios
	if err := ioctl(f.Fd(), ioctlGet, &tm); err != nil {
		t.Fatal(err)
	}
	return tm.Lflag&syscall.ECHO != 0
}

// At a real terminal the password is read without echo, twice, and the terminal is left as found.
func TestPasswdPromptsWithoutEcho(t *testing.T) {
	e := community(t)
	e.mkUser("user", "dee@example.com")
	master, slave := openPTY(t)
	errc := make(chan error, 1)
	go func() { // in and out are both the terminal, as when a person runs it
		errc <- UserCmd(context.Background(), e.a.DB, []string{"passwd", "dee@example.com"}, slave, slave)
	}()
	chunks := make(chan string, 64)
	go func() { // what the person sees on screen: everything the terminal sends to the master
		buf := make([]byte, 4096)
		for {
			n, err := master.Read(buf)
			if n > 0 {
				chunks <- string(buf[:n])
			}
			if err != nil {
				return
			}
		}
	}()
	time.Sleep(300 * time.Millisecond)
	master.WriteString("typed-in-the-pty-1\n")
	time.Sleep(300 * time.Millisecond)
	master.WriteString("typed-in-the-pty-1\n")
	if err := <-errc; err != nil {
		t.Fatal(err)
	}
	if !echoOn(t, slave) {
		t.Fatal("the terminal was left with echo off")
	}
	if r := e.signinAs("dee@example.com", "typed-in-the-pty-1", ""); r.Code != 200 {
		t.Fatalf("sign-in with the typed password: %d %s", r.Code, r.Body)
	}
	var seen strings.Builder
	for quiet := time.After(700 * time.Millisecond); ; {
		select {
		case c := <-chunks:
			seen.WriteString(c)
			continue
		case <-quiet:
		}
		break
	}
	if got := seen.String(); !strings.Contains(got, "New password: ") || !strings.Contains(got, "Repeat it: ") {
		t.Fatalf("the prompts did not reach the terminal: %q", got)
	} else if strings.Contains(got, "typed-in-the-pty-1") {
		t.Fatalf("the terminal echoed the password: %q", got)
	}
	// control: with echo back on, this harness does see what is typed, so the silence above means something
	master.WriteString("control-echo\n")
	var control strings.Builder
	for wait := time.After(time.Second); !strings.Contains(control.String(), "control-echo"); {
		select {
		case c := <-chunks:
			control.WriteString(c)
			continue
		case <-wait:
		}
		break
	}
	if !strings.Contains(control.String(), "control-echo") {
		t.Fatal("the harness cannot see an echoed line, so it proves nothing")
	}
}

// Ctrl-C at the prompt ends it at once, changes nothing, and gives the terminal its echo back.
func TestPasswdPromptInterrupted(t *testing.T) {
	e := community(t)
	id, tok := e.mkUser("user", "dee@example.com")
	master, slave := openPTY(t)
	_ = master
	errc := make(chan error, 1)
	go func() {
		errc <- UserCmd(context.Background(), e.a.DB, []string{"passwd", "dee@example.com"}, slave, new(bytes.Buffer))
	}()
	time.Sleep(400 * time.Millisecond) // at the prompt, echo off
	if echoOn(t, slave) {
		t.Fatal("echo was not turned off at the prompt")
	}
	syscall.Kill(os.Getpid(), syscall.SIGINT)
	select {
	case err := <-errc:
		if err == nil || !strings.Contains(err.Error(), "interrupted") {
			t.Fatalf("after Ctrl-C: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Ctrl-C did not end the prompt")
	}
	if !echoOn(t, slave) {
		t.Fatal("echo stayed off after Ctrl-C")
	}
	e.status(200, "GET", "/api/v1/auths/", tok, nil) // nothing changed: the session still works
	_ = id
}

// --stdin and --password-stdin read a pipe; at a terminal they refuse and say what to do instead.
func TestStdinFlagsRefuseATerminal(t *testing.T) {
	e := community(t)
	e.mkUser("user", "dee@example.com")
	_, slave := openPTY(t)
	var out bytes.Buffer
	err := UserCmd(context.Background(), e.a.DB, []string{"passwd", "--stdin", "dee@example.com"}, slave, &out)
	if err == nil || !strings.Contains(err.Error(), "terminal") || !strings.Contains(err.Error(), "leave --stdin out") {
		t.Fatalf("passwd --stdin at a terminal: %v", err)
	}
	err = UserCmd(context.Background(), e.a.DB, []string{"add", "--password-stdin", "new@example.com"}, slave, &out)
	if err == nil || !strings.Contains(err.Error(), "terminal") || e.count(`SELECT count(*) FROM "user" WHERE email = 'new@example.com'`) != 0 {
		t.Fatalf("add --password-stdin at a terminal: %v", err)
	}
}
