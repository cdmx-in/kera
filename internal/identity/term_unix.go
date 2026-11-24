//go:build linux || darwin

package identity

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
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

// readSecret prints prompt on out and reads one line from the terminal f with echo off.
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
	fmt.Fprint(out, prompt)
	line, err := bufio.NewReader(f).ReadString('\n')
	fmt.Fprintln(out)
	if err != nil && (!errors.Is(err, io.EOF) || line == "") {
		return "", err
	}
	return trimEOL(line), nil
}
