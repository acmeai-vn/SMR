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
	window, _, _ := procConsoleWnd.Call()
	if window == 0 {
		return
	}
	_, _, _ = procShowWindow.Call(window, swHide)
}
