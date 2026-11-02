//go:build windows

package service

import (
	"fmt"
	"os/exec"
	"strings"
)

// processCommand asks WMI for the command line and image path. Go's syscall
// package can read an image path but not the arguments, and the arguments are
// what separate a daemon (`smr serve`) from `smr status` when a stale pid has
// been reused by another SMR invocation.
func processCommand(pid int) (string, string, error) {
	script := fmt.Sprintf(`$p = Get-CimInstance Win32_Process -Filter "ProcessId = %d" -ErrorAction SilentlyContinue; if ($p) { $p.CommandLine; $p.ExecutablePath }`, pid)
	output, err := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", script).Output()
	if err != nil {
		return "", "", fmt.Errorf("query pid %d: %w", pid, err)
	}
	lines := strings.SplitN(strings.ReplaceAll(strings.TrimRight(string(output), "\r\n"), "\r\n", "\n"), "\n", 2)
	cmdline := strings.TrimSpace(lines[0])
	exe := ""
	if len(lines) > 1 {
		exe = strings.TrimSpace(lines[1])
	}
	return cmdline, exe, nil
}

// processZombie is always false on Windows: an exited process no longer exists.
func processZombie(int) bool { return false }
