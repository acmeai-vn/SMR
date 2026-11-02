//go:build windows

package service

import (
	"os"
	"syscall"
)

// windowsDetachedProcess keeps the daemon alive after the launching process
// exits, which is what a background service needs.
const windowsDetachedProcess = 0x00000008

func detachedSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{CreationFlags: windowsDetachedProcess, HideWindow: true}
}

// terminateProcess ends the daemon. A console-less Windows process has no
// SIGTERM equivalent, so this is a hard stop.
func terminateProcess(process *os.Process) error { return process.Kill() }
