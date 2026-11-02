//go:build windows

package main

import "syscall"

var (
	user32         = syscall.NewLazyDLL("user32.dll")
	procConsoleWnd = user32.NewProc("GetConsoleWindow")
	procShowWindow = user32.NewProc("ShowWindow")
)

const swHide = 0

// hideConsoleWindow removes the console window that would otherwise flash on
// screen when the daemon is started by an autostart entry.
func hideConsoleWindow() {
	// user32 may be unavailable when the daemon is started without an
	// interactive desktop (service hosts, WMI, session 0). Autostart must
	// never crash the router just because there is no console to hide.
	if procConsoleWnd.Find() != nil || procShowWindow.Find() != nil {
		return
	}
	window, _, _ := procConsoleWnd.Call()
	if window == 0 {
		return
	}
	_, _, _ = procShowWindow.Call(window, swHide)
}
