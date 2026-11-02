// Package service controls the per-user background service that the installers
// register. It never runs shell strings; commands are passed as arguments.
package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const (
	windowsTask = "AcmeAI SMR"
	// windowsRunKey is the per-user autostart fallback. Some machines deny
	// scheduled-task creation to non-elevated users, and the installers promise
	// no administrator rights are needed.
	windowsRunKey = `HKCU\Software\Microsoft\Windows\CurrentVersion\Run`
	macLabel      = "vn.acmeai.smr"
	linuxUnit     = "smr.service"
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
		_ = run(ctx, "schtasks", "/End", "/TN", windowsTask)
		if err := run(ctx, "schtasks", "/Run", "/TN", windowsTask); err == nil {
			return nil
		}
		// Fall back to the per-user Run entry used when task registration was
		// denied. The helper restarts the daemon in place.
		return runDetachedServe(ctx)
	case "darwin":
		return run(ctx, "launchctl", "kickstart", "-k", "gui/"+strconv.Itoa(os.Getuid())+"/"+macLabel)
	default:
		return run(ctx, "systemctl", "--user", "restart", linuxUnit)
	}
}

func Uninstall(ctx context.Context) error {
	switch runtime.GOOS {
	case "windows":
		_ = run(ctx, "schtasks", "/End", "/TN", windowsTask)
		taskErr := run(ctx, "schtasks", "/Delete", "/TN", windowsTask, "/F")
		_ = run(ctx, "taskkill", "/IM", "smr.exe", "/F")
		runErr := run(ctx, "reg", "delete", windowsRunKey, "/v", windowsTask, "/f")
		// Removing either autostart mechanism counts as success; the other one
		// was simply never installed.
		if taskErr == nil || runErr == nil {
			return nil
		}
		return taskErr
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

// stopServeScript matches only the background daemon. The CLI that calls
// restart runs with a different final argument, so this never kills itself.
const stopServeScript = `Get-CimInstance Win32_Process -Filter "Name='smr.exe'" | Where-Object { $_.CommandLine -match '\bserve\s*$' } | ForEach-Object { Stop-Process -Id $_.ProcessId -Force }`

// runDetachedServe stops the current daemon and starts a detached one. It is
// the restart path when the machine never allowed a scheduled task to be
// registered, so there is no task for schtasks to restart.
func runDetachedServe(ctx context.Context) error {
	_ = run(ctx, "powershell", "-NoProfile", "-NonInteractive", "-Command", stopServeScript)

	binary, err := os.Executable()
	if err != nil {
		return err
	}
	command := exec.Command(binary, "serve")
	command.SysProcAttr = detachedSysProcAttr()
	if err := command.Start(); err != nil {
		return err
	}
	return command.Process.Release()
}

func run(ctx context.Context, name string, args ...string) error {
	command := exec.CommandContext(ctx, name, args...)
	output, err := command.CombinedOutput()
	if err != nil {
		message := strings.TrimSpace(string(output))
		if message != "" {
			return fmt.Errorf("%s: %s", name, message)
		}
		return err
	}
	return nil
}
