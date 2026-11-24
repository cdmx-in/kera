//go:build linux

package identity

import "syscall"

const ioctlGet, ioctlSet = syscall.TCGETS, syscall.TCSETS
