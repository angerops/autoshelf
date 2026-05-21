//go:build unix

package rules

import "syscall"

// mkfifo creates a named pipe at path. Unix-only; the non-unix variant
// returns an error and the test skips.
func mkfifo(path string, mode uint32) error {
	return syscall.Mkfifo(path, mode)
}
