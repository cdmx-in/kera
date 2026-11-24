//go:build linux || darwin

package identity

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"unsafe"
)

// Reading a password without echo needs the terminal's mode. golang.org/x/term
// does this; the standard library's syscall does the same in a few lines on the
// two systems Kera ships for, and the CLI is not worth another dependency.

func ioctl(fd, req uintptr, t *syscall.Termios) error {
	if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, fd, req, uintptr(unsafe.Pointer(t))); e != 0 {
		return e
	}
	return nil
}

func isTerminal(f *os.File) bool {
	var t syscall.Termios
	return ioctl(f.Fd(), ioctlGet, &t) == nil
}

// readSecret prints prompt on out and reads one line from the terminal f with echo off. Ctrl-C
// (SIGINT) or SIGTERM while it waits puts the terminal back as it was and returns an error: the
// read itself cannot be cancelled, so it runs on the side and is left behind when the process exits.
func readSecret(f *os.File, prompt string, out io.Writer) (string, error) {
	var old syscall.Termios
	if err := ioctl(f.Fd(), ioctlGet, &old); err != nil {
		return "", errors.New("standard input is not a terminal")
	}
	quiet := old
	quiet.Lflag &^= syscall.ECHO
	if err := ioctl(f.Fd(), ioctlSet, &quiet); err != nil {
		return "", fmt.Errorf("turn off echo: %w", err)
	}
	defer ioctl(f.Fd(), ioctlSet, &old)
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sig)
	fmt.Fprint(out, prompt)
	type result struct {
		line string
		err  error
	}
	done := make(chan result, 1)
	go func() {
		line, err := bufio.NewReader(f).ReadString('\n')
		done <- result{line, err}
	}()
	select {
	case <-sig:
		fmt.Fprintln(out)
		return "", errors.New("interrupted; nothing was changed")
	case r := <-done:
		fmt.Fprintln(out)
		if r.err != nil && (!errors.Is(r.err, io.EOF) || r.line == "") {
			return "", r.err
		}
		return trimEOL(r.line), nil
	}
}
