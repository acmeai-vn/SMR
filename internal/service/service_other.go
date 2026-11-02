//go:build !windows

package service

import (
	"os"
	"syscall"
)

// Setsid detaches the daemon from the terminal's process group. Without it a
// daemon started by `smr setup` receives the shell's SIGHUP and dies.
func detachedSysProcAttr() *syscall.SysProcAttr { return &syscall.SysProcAttr{Setsid: true} }

// terminateProcess asks the daemon to shut down. SMR drains in-flight requests
// for up to 10 seconds on SIGTERM, so a restart must not SIGKILL it first.
func terminateProcess(process *os.Process) error {
	return process.Signal(syscall.SIGTERM)
}
