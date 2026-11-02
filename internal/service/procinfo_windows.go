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
	// Prefix each field. A process can expose CommandLine but not
	// ExecutablePath, and a bare null would shift the executable path into the
	// command-line slot and misclassify the daemon as a reused pid.
	script := fmt.Sprintf(`$p = Get-CimInstance Win32_Process -Filter "ProcessId = %d" -ErrorAction SilentlyContinue; if ($p) { "C=" + $p.CommandLine; "E=" + $p.ExecutablePath }`, pid)
	output, err := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", script).Output()
	if err != nil {
		return "", "", fmt.Errorf("query pid %d: %w", pid, err)
	}
	cmdline, exe := parseProcessInfo(string(output))
	return cmdline, exe, nil
}

// parseProcessInfo reads the "C=" and "E=" lines processCommand requests.
func parseProcessInfo(output string) (cmdline, exe string) {
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimRight(line, "\r")
		switch {
		case strings.HasPrefix(line, "C="):
			cmdline = strings.TrimSpace(line[2:])
		case strings.HasPrefix(line, "E="):
			exe = strings.TrimSpace(line[2:])
		}
	}
	return cmdline, exe
}

// processZombie is always false on Windows: an exited process no longer exists.
func processZombie(int) bool { return false }
