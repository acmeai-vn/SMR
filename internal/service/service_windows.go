//go:build windows

package service

import "syscall"

// windowsDetachedProcess keeps the daemon alive after the launching process
// exits, which is what a background service needs.
const windowsDetachedProcess = 0x00000008

func detachedSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{CreationFlags: windowsDetachedProcess, HideWindow: true}
}
