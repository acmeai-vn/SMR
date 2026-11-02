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

func Restart(ctx context.Context) error {
	switch runtime.GOOS {
	case "windows":
		if err := stopWindowsServe(ctx); err != nil {
			return err
		}
		if err := run(ctx, "schtasks", "/Run", "/TN", windowsTask); err == nil {
			return nil
		}
		// Fall back to the Startup-folder entry used when task registration was
		// denied.
		return StartBackground()
	case "darwin":
		if err := run(ctx, "launchctl", "kickstart", "-k", "gui/"+strconv.Itoa(os.Getuid())+"/"+macLabel); err == nil {
			return nil
		}
		return restartDetached(ctx)
	default:
		if err := run(ctx, "systemctl", "--user", "restart", linuxUnit); err == nil {
			return nil
		}
		return restartDetached(ctx)
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

// restartDetached covers installs that skipped the service manager
// (`--no-service`): stop the recorded daemon, then start a fresh one.
func restartDetached(ctx context.Context) error {
	if err := StopDetached(); err != nil {
		return err
	}
	return StartBackground()
}

// StopDetached stops a daemon that is not managed by systemd or launchd, using
// the single-instance pid file the daemon writes.
func StopDetached() error {
	dataDir, err := smrpaths.DataDir()
	if err != nil {
		return err
	}
	pid, running := readLockOwner(filepath.Join(dataDir, daemonPIDFile))
	if !running {
		return nil
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	if err := process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return err
	}
	return nil
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
