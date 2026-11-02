//go:build !windows

package service

import (
	"errors"
	"syscall"
)

// processAlive uses signal 0: it checks existence without sending a signal.
// EPERM still proves the process exists, just under another user. A zombie is
// dead for our purposes: it can never serve a request, and leaving it "alive"
// would make the next start fail with ErrAlreadyRunning.
func processAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	if err != nil && !errors.Is(err, syscall.EPERM) {
		return false
	}
	return !processZombie(pid)
}
