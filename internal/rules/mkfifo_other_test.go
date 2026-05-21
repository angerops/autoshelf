//go:build !unix

package rules

import "errors"

func mkfifo(path string, mode uint32) error {
	return errors.New("mkfifo not supported on this platform")
}
