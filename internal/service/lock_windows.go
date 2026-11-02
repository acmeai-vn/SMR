//go:build windows

package service

import "syscall"

const (
	processQueryLimitedInformation = 0x1000
	stillActive                    = 259
)

func processAlive(pid int) bool {
	handle, err := syscall.OpenProcess(processQueryLimitedInformation, false, uint32(pid))
	if err != nil {
		return false
	}
	defer syscall.CloseHandle(handle)
	var code uint32
	if err := syscall.GetExitCodeProcess(handle, &code); err != nil {
		// The handle opened, so the process exists even if the query failed.
		return true
	}
	return code == stillActive
}
