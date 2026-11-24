//go:build !linux && !darwin

package identity

import (
	"errors"
	"io"
	"os"
)

func isTerminal(*os.File) bool { return false }

func readSecret(*os.File, string, io.Writer) (string, error) {
	return "", errors.New("reading a password from the terminal is not supported here; use --stdin")
}
