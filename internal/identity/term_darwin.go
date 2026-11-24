//go:build darwin

package identity

import "syscall"

const ioctlGet, ioctlSet = syscall.TIOCGETA, syscall.TIOCSETA
