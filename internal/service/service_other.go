//go:build !windows

package service

import "syscall"

// Setsid detaches the daemon from the terminal's process group. Without it a
// daemon started by `smr setup` receives the shell's SIGHUP and dies.
func detachedSysProcAttr() *syscall.SysProcAttr { return &syscall.SysProcAttr{Setsid: true} }
