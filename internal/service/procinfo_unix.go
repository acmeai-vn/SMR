//go:build !windows

package service

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

// processCommand returns the command line and, when it can be resolved, the
// executable path of pid. A restart compares both against the pid file before
// signalling anything: after a reboot the recorded pid may belong to an
// unrelated process.
func processCommand(pid int) (string, string, error) {
	if runtime.GOOS == "linux" {
		raw, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/cmdline")
		if err != nil {
			return "", "", err
		}
		cmdline := strings.TrimSpace(strings.ReplaceAll(string(raw), "\x00", " "))
		// readlink keeps working when the binary was replaced by an update and
		// now ends with " (deleted)"; sameExecutable strips that suffix.
		exe, _ := os.Readlink("/proc/" + strconv.Itoa(pid) + "/exe")
		return cmdline, exe, nil
	}
	output, err := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "command=").Output()
	if err != nil {
		return "", "", fmt.Errorf("ps pid %d: %w", pid, err)
	}
	cmdline := strings.TrimSpace(string(output))
	exe := ""
	if fields := strings.Fields(cmdline); len(fields) > 0 && filepath.IsAbs(fields[0]) {
		exe = fields[0]
	}
	return cmdline, exe, nil
}

// processZombie reports whether pid has exited but has not been reaped.
func processZombie(pid int) bool {
	if runtime.GOOS == "linux" {
		body, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
		if err != nil {
			return false
		}
		// Format: pid (comm) state ...; comm may contain spaces and parens.
		if index := strings.LastIndexByte(string(body), ')'); index >= 0 && index+2 < len(body) {
			return body[index+2] == 'Z'
		}
		return false
	}
	output, err := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "stat=").Output()
	if err != nil {
		return false
	}
	return strings.HasPrefix(strings.TrimSpace(string(output)), "Z")
}
