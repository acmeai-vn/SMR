//go:build !windows

package service

import (
	"errors"
	"syscall"
)

// processAlive uses signal 0: it checks existence without sending a signal.
// EPERM still proves the process exists, just under another user.
func processAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
