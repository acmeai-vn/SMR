//go:build !windows

package service

import (
	"errors"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// TestHelperStubbornServe looks like the daemon but ignores SIGTERM, the way a
// daemon with a hung request handler can. It only dies when the escalation
// sends SIGKILL.
func TestHelperStubbornServe(t *testing.T) {
	if os.Getenv("SMR_HELPER_SERVE") != "1" {
		return
	}
	signal.Ignore(syscall.SIGTERM)
	// Announce readiness: a SIGTERM delivered before signal.Ignore runs would
	// kill the helper and make the ordering test pass for the wrong reason.
	if marker := os.Getenv("SMR_HELPER_READY"); marker != "" {
		_ = os.WriteFile(marker, []byte("ready"), 0o600)
	}
	time.Sleep(30 * time.Second)
}

// startStubbornHelper returns a serve-shaped process that ignores SIGTERM and
// has already installed its handler.
func startStubbornHelper(t *testing.T) *os.Process {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(t.TempDir(), "ready")
	command := exec.Command(exe, "-test.run=TestHelperStubbornServe", "serve")
	command.Env = append(os.Environ(), "SMR_HELPER_SERVE=1", "SMR_HELPER_READY="+marker)
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = command.Process.Kill()
		_, _ = command.Process.Wait()
	})
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(marker); err == nil {
			return command.Process
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("stubborn helper did not become ready")
	return nil
}

// A restart must ask the daemon to drain before forcing it down: SIGTERM is
// sent first, and SIGKILL only after the graceful window expires.
func TestStopDetachedEscalatesAfterDrainWindow(t *testing.T) {
	previousTerminate, previousKill := terminateTimeout, forceKillTimeout
	terminateTimeout, forceKillTimeout = 300*time.Millisecond, 5*time.Second
	t.Cleanup(func() { terminateTimeout, forceKillTimeout = previousTerminate, previousKill })

	dataDir := t.TempDir()
	t.Setenv("SMR_DATA_DIR", dataDir)
	helper := startStubbornHelper(t)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	path := writePIDFile(t, dataDir, helper.Pid, exe)

	started := time.Now()
	if err := StopDetached(); err != nil {
		t.Fatalf("StopDetached: %v", err)
	}
	// A Ctrl-C-style shutdown would have killed it at once; the helper only
	// exits after SIGKILL, so this proves the signal order.
	if elapsed := time.Since(started); elapsed < terminateTimeout {
		t.Fatalf("daemon stopped after %v, before the drain window (%v)", elapsed, terminateTimeout)
	}
	if processAlive(helper.Pid) {
		t.Fatal("daemon survived the forced stop")
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("pid file was not cleared: %v", err)
	}
}

// Daemons that honour SIGTERM must be gone well before the escalation, so a
// normal restart stays fast.
func TestStopDetachedDrainsWithoutForcing(t *testing.T) {
	previousTerminate := terminateTimeout
	terminateTimeout = 5 * time.Second
	t.Cleanup(func() { terminateTimeout = previousTerminate })

	dataDir := t.TempDir()
	t.Setenv("SMR_DATA_DIR", dataDir)
	helper := startHelperServe(t)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	writePIDFile(t, dataDir, helper.Pid, exe)
	if err := StopDetached(); err != nil {
		t.Fatalf("StopDetached: %v", err)
	}
	if processAlive(helper.Pid) {
		t.Fatal("daemon is still running")
	}
}
