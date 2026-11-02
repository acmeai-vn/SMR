// Package service controls the per-user background service that the installers
// register. It never runs shell strings; commands are passed as arguments.
package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/acmeai-vn/SMR/internal/smrpaths"
)

const (
	windowsTask = "AcmeAI SMR"
	// windowsStartupShortcutName is the per-user autostart fallback. Some
	// machines deny scheduled-task creation to non-elevated users, and the
	// installers promise no administrator rights are needed. The Startup folder
	// is used instead of HKCU\Run because an unsigned binary writing a Run entry
	// is what Defender classifies as Behavior:Win32/Persistence.A!ml.
	windowsStartupShortcutName = "AcmeAI SMR.lnk"
	// windowsLegacyRunKey only exists so upgrades can remove the Run entry that
	// releases up to v0.1.1 created.
	windowsLegacyRunKey = `HKCU\Software\Microsoft\Windows\CurrentVersion\Run`
	macLabel            = "vn.acmeai.smr"
	linuxUnit           = "smr.service"
)

type Health struct {
	Status     string `json:"status"`
	Classifier struct {
		BaseURL    string `json:"base_url"`
		Configured bool   `json:"configured"`
		LastOK     bool   `json:"last_ok"`
		LastAt     string `json:"last_at"`
	} `json:"classifier"`
}

// Signal budgets for stopping a detached daemon. The daemon drains in-flight
// requests for up to 10 seconds after SIGTERM, so the graceful window has to be
// longer than that. Tests shorten both.
var (
	terminateTimeout = 12 * time.Second
	forceKillTimeout = 3 * time.Second
)

func HealthCheck(ctx context.Context, baseURL string) (Health, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(baseURL, "/")+"/health", nil)
	if err != nil {
		return Health{}, err
	}
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return Health{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Health{}, fmt.Errorf("health returned %d", resp.StatusCode)
	}
	var health Health
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&health); err != nil {
		return Health{}, err
	}
	return health, nil
}

// restartDeps is the per-OS part of Restart. Tests swap it out so every branch
// is covered without stopping the daemon that runs them or calling schtasks.
type restartDeps struct {
	goos          string
	uid           int
	stopDetached  func() error
	stopWindows   func(context.Context) error
	command       func(context.Context, string, ...string) error
	startDetached func() error
}

func Restart(ctx context.Context) error {
	return restartWith(ctx, restartDeps{
		goos:          runtime.GOOS,
		uid:           os.Getuid(),
		stopDetached:  StopDetached,
		stopWindows:   stopWindowsServe,
		command:       run,
		startDetached: StartBackground,
	})
}

func restartWith(ctx context.Context, deps restartDeps) error {
	// A daemon started outside the service manager holds the data directory, so
	// the process the service manager spawns would exit with ErrAlreadyRunning
	// while systemctl/launchctl still report success. Stop it first; the pid
	// file also covers --no-service installs.
	if err := deps.stopDetached(); err != nil {
		return err
	}
	restartDetached := func() error {
		if err := deps.stopDetached(); err != nil {
			return err
		}
		return deps.startDetached()
	}
	switch deps.goos {
	case "windows":
		if err := deps.stopWindows(ctx); err != nil {
			return err
		}
		if err := deps.command(ctx, "schtasks", "/Run", "/TN", windowsTask); err == nil {
			return nil
		}
		// Fall back to the Startup-folder entry used when task registration was
		// denied.
		return restartDetached()
	case "darwin":
		label := "gui/" + strconv.Itoa(deps.uid) + "/" + macLabel
		if err := deps.command(ctx, "launchctl", "kickstart", "-k", label); err == nil {
			return nil
		}
		return restartDetached()
	default:
		if err := deps.command(ctx, "systemctl", "--user", "restart", linuxUnit); err == nil {
			return nil
		}
		return restartDetached()
	}
}

func Uninstall(ctx context.Context) error {
	// A daemon started by `smr setup` or by hand has no service manager to stop
	// it, so use the pid file first.
	_ = StopDetached()
	switch runtime.GOOS {
	case "windows":
		_ = run(ctx, "schtasks", "/End", "/TN", windowsTask)
		taskErr := run(ctx, "schtasks", "/Delete", "/TN", windowsTask, "/F")
		_ = run(ctx, "taskkill", "/IM", "smr.exe", "/F")
		_ = run(ctx, "reg", "delete", windowsLegacyRunKey, "/v", windowsTask, "/f")
		shortcut, shortcutErr := windowsStartupShortcut()
		if shortcutErr == nil {
			shortcutErr = os.Remove(shortcut)
			if os.IsNotExist(shortcutErr) {
				shortcutErr = nil
			}
		}
		// Removing either autostart mechanism counts as success; the other one
		// was simply never installed.
		if taskErr == nil || shortcutErr == nil {
			return nil
		}
		return fmt.Errorf("remove autostart: task: %v; startup shortcut: %w", taskErr, shortcutErr)
	case "darwin":
		_ = run(ctx, "launchctl", "bootout", "gui/"+strconv.Itoa(os.Getuid())+"/"+macLabel)
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		plist := filepath.Join(home, "Library", "LaunchAgents", macLabel+".plist")
		if err := os.Remove(plist); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	default:
		_ = run(ctx, "systemctl", "--user", "disable", "--now", linuxUnit)
		base := os.Getenv("XDG_CONFIG_HOME")
		if base == "" {
			home, err := os.UserHomeDir()
			if err != nil {
				return err
			}
			base = filepath.Join(home, ".config")
		}
		unit := filepath.Join(base, "systemd", "user", linuxUnit)
		if err := os.Remove(unit); err != nil && !os.IsNotExist(err) {
			return err
		}
		_ = run(ctx, "systemctl", "--user", "daemon-reload")
		return nil
	}
}

func OpenBrowser(ctx context.Context, target string) error {
	switch runtime.GOOS {
	case "windows":
		return run(ctx, "rundll32", "url.dll,FileProtocolHandler", target)
	case "darwin":
		return run(ctx, "open", target)
	default:
		return run(ctx, "xdg-open", target)
	}
}

// windowsStartupShortcut is the per-user autostart entry the Windows installer
// writes when it cannot register a scheduled task.
func windowsStartupShortcut() (string, error) {
	appData := os.Getenv("APPDATA")
	if appData == "" {
		return "", fmt.Errorf("APPDATA is not set")
	}
	return filepath.Join(appData, "Microsoft", "Windows", "Start Menu", "Programs", "Startup", windowsStartupShortcutName), nil
}

// serveCommandPattern matches the daemon process: `smr serve` or
// `smr serve --background`, with nothing after it. Keep the PowerShell scripts
// below in sync; they use the same expression. Lifecycle commands such as
// `smr restart` and `smr update` never end in `serve`, so a command cannot
// match, and kill, itself.
const serveCommandPattern = `\bserve(\s+--background)?\s*$`

var serveCommandRegexp = regexp.MustCompile(serveCommandPattern)

// isServeCommand reports whether a Windows command line belongs to the daemon.
func isServeCommand(commandLine string) bool {
	return serveCommandRegexp.MatchString(commandLine)
}

// Windows PowerShell cannot be tested from the Go test suite on another OS, so
// the process lookup lives in one place and is verified by isServeCommand.
// The process name is matched as smr*.exe because users rename the binary
// (for example to smr-local.exe) and an update must still stop that daemon.
const stopServeScript = `Get-CimInstance Win32_Process | Where-Object { $_.Name -like 'smr*.exe' -and $_.CommandLine -match '` + serveCommandPattern + `' } | ForEach-Object { Stop-Process -Id $_.ProcessId -Force }`

const listServeScript = `Get-CimInstance Win32_Process | Where-Object { $_.Name -like 'smr*.exe' -and $_.CommandLine -match '` + serveCommandPattern + `' } | ForEach-Object { $_.ProcessId }`

// stopWindowsServe ends every daemon started by any install path: the
// scheduled task (End), the Startup-folder shortcut, and a manual
// `smr serve --background` (process match). `install.ps1` mixed Start-Process
// and scheduled tasks, which is why ending the task alone left the old build
// running and the new one unable to bind the port.
func stopWindowsServe(ctx context.Context) error {
	_ = run(ctx, "schtasks", "/End", "/TN", windowsTask)
	_ = run(ctx, "powershell", "-NoProfile", "-NonInteractive", "-Command", stopServeScript)
	output, err := outputOf(ctx, "powershell", "-NoProfile", "-NonInteractive", "-Command", listServeScript)
	if err != nil {
		// Do not report a successful restart when the process list could not be
		// read: starting now could bind-fail while the old daemon keeps serving.
		return fmt.Errorf("verify that SMR stopped: %w", err)
	}
	leftover := strings.TrimSpace(output)
	if leftover != "" {
		return fmt.Errorf("SMR is still running (pid %s); stop it and try again", strings.Join(strings.Fields(leftover), ", "))
	}
	return nil
}

// StopDetached stops a daemon that is not managed by systemd or launchd, using
// the single-instance pid file the daemon writes.
func StopDetached() error {
	dataDir, err := smrpaths.DataDir()
	if err != nil {
		return err
	}
	return stopDetachedAt(filepath.Join(dataDir, daemonPIDFile))
}

// stopDetachedAt stops the daemon recorded in path. It verifies the process
// before signalling it: a pid file survives a crash or power loss, and the
// recorded pid may have been reused by an unrelated program after a reboot.
func stopDetachedAt(path string) error {
	record := readPIDRecord(path)
	if record.PID <= 0 {
		return nil
	}
	if !processAlive(record.PID) {
		// Remove the stale file so a reused pid cannot block the next start.
		return removePIDFile(path, record.PID)
	}
	match, err := processMatchesDaemon(record)
	if err != nil {
		if !processAlive(record.PID) {
			// It exited between the liveness check and the query. Returning
			// the error here would make a restart fail on a stale pid file.
			return removePIDFile(path, record.PID)
		}
		return fmt.Errorf("cannot verify pid %d before stopping it: %w", record.PID, err)
	}
	if !match {
		// The pid was reused after a crash or reboot. Leave that process alone
		// and drop the stale lock so the next start is not blocked by it.
		return removePIDFile(path, record.PID)
	}
	process, err := os.FindProcess(record.PID)
	if err != nil {
		return err
	}
	// Ask for a graceful shutdown first so the daemon can finish in-flight
	// requests. Windows has no SIGTERM, and terminateProcess kills there.
	if err := terminateProcess(process); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return err
	}
	if waitForExit(record.PID, terminateTimeout) {
		return removePIDFile(path, record.PID)
	}
	// Still serving after the drain window: force it down so the next start
	// can bind the ports.
	if err := process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return err
	}
	if waitForExit(record.PID, forceKillTimeout) {
		return removePIDFile(path, record.PID)
	}
	return fmt.Errorf("SMR pid %d did not exit", record.PID)
}

// waitForExit polls until pid disappears or the timeout expires. The signal
// only requests an exit; a following start must not race the still-held lock.
func waitForExit(pid int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if !processAlive(pid) {
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return !processAlive(pid)
}

// removePIDFile deletes path only when it still names pid.
func removePIDFile(path string, pid int) error {
	if current := readPIDRecord(path); current.PID != pid {
		return nil
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// processMatchesDaemon reports whether the recorded pid still runs SMR: the
// command line must end in `serve` / `serve --background` and, when both sides
// are known, the executable must be the one that wrote the lock.
func processMatchesDaemon(record pidRecord) (bool, error) {
	cmdline, exe, err := processCommand(record.PID)
	if err != nil {
		return false, err
	}
	if strings.TrimSpace(cmdline) == "" {
		if !processAlive(record.PID) {
			return false, nil
		}
		// Windows returns no command line for a process owned by another user
		// or one that is protected. Refuse rather than guess: clearing this
		// lock could let a second daemon start while this one still serves.
		return false, fmt.Errorf("cannot read the command line of pid %d; stop it manually if it is an SMR daemon", record.PID)
	}
	if !isServeCommand(cmdline) {
		return false, nil
	}
	return matchesDaemon(record, cmdline, exe), nil
}

// matchesDaemon decides whether a live process is the one the pid file
// recorded. The image path is checked first: argv[0] is under the process's
// control, so a daemon-shaped command line alone must not authorize a signal.
// exe may be empty, or truncated at the first space by macOS ps.
func matchesDaemon(record pidRecord, cmdline, exe string) bool {
	if record.Exe == "" {
		// Older pid file: the command line is the strongest signal left, and
		// the caller already required it to end in `serve`.
		return true
	}
	if exe != "" && sameExecutable(exe, record.Exe) {
		return true
	}
	if exe != "" && !truncatedExecutable(exe, record.Exe) {
		// A complete image path that is not the recorded one: this is either a
		// different install or a reused pid.
		return false
	}
	// The image path is unknown or truncated. Unix argv is not quoted, so the
	// recorded executable is a prefix of the command line even when the
	// install path contains spaces.
	return executablePrefixesCommandLine(cmdline, record.Exe)
}

// truncatedExecutable reports whether exe looks like a prefix of recorded cut
// at a space. macOS ps historically truncated the path there, and such a value
// must not be read as "a different binary".
func truncatedExecutable(exe, recorded string) bool {
	if len(exe) == 0 || len(recorded) <= len(exe) {
		return false
	}
	head := recorded[:len(exe)]
	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		if !strings.EqualFold(head, exe) {
			return false
		}
	} else if head != exe {
		return false
	}
	return recorded[len(exe)] == ' ' || recorded[len(exe)] == '\t'
}

// executablePrefixesCommandLine reports whether cmdline starts with exe,
// followed by the argument separator. Windows quotes argv[0] when the path
// contains spaces, which is accepted too. The boundary check keeps
// "/opt/smr/smr" from matching "/opt/smr/smr2".
func executablePrefixesCommandLine(cmdline, exe string) bool {
	cmdline = strings.TrimSpace(cmdline)
	exe = strings.TrimSpace(exe)
	if exe == "" || cmdline == "" {
		return false
	}
	quoted := strings.HasPrefix(cmdline, `"`)
	head := strings.TrimPrefix(cmdline, `"`)
	if len(head) <= len(exe) {
		return false
	}
	prefix := head[:len(exe)]
	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		if !strings.EqualFold(prefix, exe) {
			return false
		}
	} else if prefix != exe {
		return false
	}
	rest := head[len(exe):]
	if quoted {
		return len(rest) > 1 && rest[0] == '"' && (rest[1] == ' ' || rest[1] == '\t')
	}
	return rest[0] == ' ' || rest[0] == '\t'
}

// sameExecutable compares paths, tolerating the " (deleted)" suffix Linux adds
// to /proc/<pid>/exe after an update replaced the binary on disk.
func sameExecutable(left, right string) bool {
	trim := func(path string) string {
		return strings.TrimSuffix(filepath.Clean(path), " (deleted)")
	}
	left, right = trim(left), trim(right)
	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		// Both filesystems are case-insensitive by default.
		return strings.EqualFold(left, right)
	}
	return left == right
}

// StartBackground launches the daemon detached from the current terminal. The
// binary keeps its own log file, so the parent can discard stdio and exit
// immediately; `smr setup` uses this when the user has no autostart entry yet.
func StartBackground() error {
	binary, err := os.Executable()
	if err != nil {
		return err
	}
	null, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	defer null.Close()
	command := exec.Command(binary, "serve", "--background")
	command.SysProcAttr = detachedSysProcAttr()
	command.Stdin = nil
	command.Stdout = null
	command.Stderr = null
	if err := command.Start(); err != nil {
		return err
	}
	return command.Process.Release()
}

func run(ctx context.Context, name string, args ...string) error {
	_, err := outputOf(ctx, name, args...)
	return err
}

func outputOf(ctx context.Context, name string, args ...string) (string, error) {
	command := exec.CommandContext(ctx, name, args...)
	output, err := command.CombinedOutput()
	if err != nil {
		message := strings.TrimSpace(string(output))
		if message != "" {
			return string(output), fmt.Errorf("%s: %s", name, message)
		}
		return string(output), err
	}
	return string(output), nil
}
