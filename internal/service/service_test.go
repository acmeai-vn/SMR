package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The daemon runs as `smr serve --background`; matching only `serve$` made
// `smr restart` and `smr update` leave the old build serving traffic.
func TestIsServeCommand(t *testing.T) {
	cases := []struct {
		commandLine string
		want        bool
	}{
		{`"C:\Users\me\AppData\Local\AcmeAI\SMR\smr.exe" serve --background`, true},
		{`"C:\Users\me\AppData\Local\AcmeAI\SMR\smr-local.exe" serve --background`, true},
		{`C:\AcmeAI\SMR\smr.exe serve`, true},
		{`smr serve`, true},
		{`smr.exe  serve   --background`, true},
		{`C:\AcmeAI\SMR\smr.exe restart`, false},
		{`C:\AcmeAI\SMR\smr.exe update --version v1.2.3`, false},
		{`C:\AcmeAI\SMR\smr.exe setup`, false},
		{`C:\AcmeAI\SMR\smr.exe serve --background --other`, false},
		{`C:\AcmeAI\SMR\smr.exe uninstall --purge`, false},
	}
	for _, testCase := range cases {
		if got := isServeCommand(testCase.commandLine); got != testCase.want {
			t.Errorf("isServeCommand(%q) = %v, want %v", testCase.commandLine, got, testCase.want)
		}
	}
}

// TestHelperServe is a stand-in daemon: its command line ends in `serve`, like
// the real one, so the lock can tell it apart from the test process itself.
func TestHelperServe(t *testing.T) {
	if os.Getenv("SMR_HELPER_SERVE") != "1" {
		return
	}
	time.Sleep(30 * time.Second)
}

// TestHelperIdle is a live process that is not a daemon.
func TestHelperIdle(t *testing.T) {
	if os.Getenv("SMR_HELPER_SERVE") != "1" {
		return
	}
	time.Sleep(30 * time.Second)
}

func startHelper(t *testing.T, run string, args ...string) *os.Process {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(exe, append([]string{"-test.run=" + run}, args...)...)
	command.Env = append(os.Environ(), "SMR_HELPER_SERVE=1")
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = command.Process.Kill()
		_, _ = command.Process.Wait()
	})
	return command.Process
}

func startHelperServe(t *testing.T) *os.Process {
	t.Helper()
	return startHelper(t, "TestHelperServe", "serve")
}

func startHelperIdle(t *testing.T) *os.Process {
	t.Helper()
	return startHelper(t, "TestHelperIdle")
}

// A second daemon must fail before it can bind a port or rewrite ui-token.
func TestAcquireLockIsExclusive(t *testing.T) {
	dataDir := t.TempDir()
	helper := startHelperServe(t)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	writePIDFile(t, dataDir, helper.Pid, exe)

	_, err = AcquireLock(dataDir)
	if !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("second AcquireLock error = %v, want ErrAlreadyRunning", err)
	}
}

// A released lock is reusable by the same process.
func TestAcquireLockReusesReleasedLock(t *testing.T) {
	dataDir := t.TempDir()
	lock, err := AcquireLock(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := lock.Release(); err != nil {
		t.Fatal(err)
	}
	again, err := AcquireLock(dataDir)
	if err != nil {
		t.Fatalf("re-acquire after release: %v", err)
	}
	_ = again.Release()
}

// A pid file left by a crash must not lock the user out forever.
func TestAcquireLockClearsStalePID(t *testing.T) {
	dataDir := t.TempDir()
	path := filepath.Join(dataDir, daemonPIDFile)
	if err := os.WriteFile(path, []byte("2147483647\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	lock, err := AcquireLock(dataDir)
	if err != nil {
		t.Fatalf("stale lock blocked start: %v", err)
	}
	defer lock.Release()
}

// An old process must not delete the lock a newer daemon just took.
func TestReleaseLeavesAnotherProcessLock(t *testing.T) {
	dataDir := t.TempDir()
	lock, err := AcquireLock(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dataDir, daemonPIDFile)
	if err := os.WriteFile(path, []byte("2147483647\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := lock.Release(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("release removed a lock owned by another pid: %v", err)
	}
}

func writePIDFile(t *testing.T, dataDir string, pid int, exe string) string {
	t.Helper()
	path := filepath.Join(dataDir, daemonPIDFile)
	body := fmt.Sprintf("%d\n%s\n", pid, exe)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// A pid file survives a crash or reboot, and the recorded pid may then belong
// to an unrelated process. A dead pid must be cleared instead of blocking the
// next start.
func TestStopDetachedClearsDeadPID(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("SMR_DATA_DIR", dataDir)
	path := writePIDFile(t, dataDir, 2147483647, filepath.Join(dataDir, "smr.exe"))

	if err := StopDetached(); err != nil {
		t.Fatalf("StopDetached: %v", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale pid file was left behind: %v", err)
	}
}

// The test process is alive but its command line is not `smr serve`, so a
// reboot that reused the pid must not get it killed; the stale lock is dropped
// instead of blocking the next start.
func TestStopDetachedLeavesReusedPIDAlive(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("SMR_DATA_DIR", dataDir)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	path := writePIDFile(t, dataDir, os.Getpid(), exe)

	if err := StopDetached(); err != nil {
		t.Fatalf("StopDetached: %v", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale pid file was not cleared: %v", err)
	}
}

// Even a serve-like command at a different binary is not the recorded daemon.
func TestStopDetachedIgnoresDifferentBinary(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("SMR_DATA_DIR", dataDir)
	path := writePIDFile(t, dataDir, os.Getpid(), filepath.Join(dataDir, "somewhere-else", "smr.exe"))

	if err := StopDetached(); err != nil {
		t.Fatalf("StopDetached: %v", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale pid file was not cleared: %v", err)
	}
}

// A pid file left after a reboot must not lock the user out when the pid now
// belongs to a different program.
func TestAcquireLockReplacesReusedPID(t *testing.T) {
	dataDir := t.TempDir()
	helper := startHelperIdle(t)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	path := writePIDFile(t, dataDir, helper.Pid, exe)

	lock, err := AcquireLock(dataDir)
	if err != nil {
		t.Fatalf("reused pid blocked the lock: %v", err)
	}
	defer lock.Release()
	record := readPIDRecord(path)
	if record.PID != os.Getpid() {
		t.Fatalf("lock still names pid %d", record.PID)
	}
}

// The happy path: a verified daemon is stopped and its lock is cleared so the
// next start is not blocked.
func TestStopDetachedStopsRecordedDaemon(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("SMR_DATA_DIR", dataDir)
	helper := startHelperServe(t)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	path := writePIDFile(t, dataDir, helper.Pid, exe)

	if err := StopDetached(); err != nil {
		t.Fatalf("StopDetached: %v", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("pid file was not cleared: %v", err)
	}
	if processAlive(helper.Pid) {
		t.Fatal("daemon is still running after StopDetached")
	}
}

// macOS prints argv unquoted, so an install path with spaces used to be cut
// down to "/Users/me/Library/Application". The daemon was then treated as a
// reused pid and left running, while a restart reported success because the
// old process still answered /health.
func TestMatchesDaemonWithSpacesInExecutable(t *testing.T) {
	exe := "/Users/me/Library/Application Support/AcmeAI/SMR/smr"
	record := pidRecord{PID: 42, Exe: exe}
	cases := []struct {
		name    string
		cmdline string
		exe     string
		want    bool
	}{
		{"ps comm unavailable", exe + " serve --background", "", true},
		{"ps comm truncated at the first space", exe + " serve --background", "/Users/me/Library/Application", true},
		{"ps comm present", exe + " serve", exe, true},
		{"same prefix, different binary", exe + "2 serve", exe + "2", false},
		{"different install", "/opt/smr/smr serve", "/opt/smr/smr", false},
		{"spoofed argv, different image", exe + " serve", "/opt/other/smr", false},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := matchesDaemon(record, testCase.cmdline, testCase.exe); got != testCase.want {
				t.Errorf("matchesDaemon(%q, %q) = %v, want %v", testCase.cmdline, testCase.exe, got, testCase.want)
			}
		})
	}
}

// A pid file without the executable path (written by an older build) still
// identifies the daemon from its command line.
func TestMatchesDaemonWithoutRecordedExecutable(t *testing.T) {
	record := pidRecord{PID: 42}
	if !matchesDaemon(record, "/opt/smr/smr serve", "") {
		t.Fatal("old pid file no longer identifies the daemon")
	}
}

// Windows quotes argv[0] when the install path contains spaces, so the prefix
// check has to accept the closing quote as the argument boundary.
func TestExecutablePrefixesCommandLine(t *testing.T) {
	cases := []struct {
		name    string
		cmdline string
		exe     string
		want    bool
	}{
		{"unquoted", "/opt/smr/smr serve", "/opt/smr/smr", true},
		{"quoted path with spaces", `"C:\Program Files\SMR\smr.exe" serve`, `C:\Program Files\SMR\smr.exe`, true},
		{"quoted without arguments", `"/opt/smr/smr"`, "/opt/smr/smr", false},
		{"prefix of a different binary", "/opt/smr/smr2 serve", "/opt/smr/smr", false},
		{"no argument separator", "/opt/smr/smr-serve", "/opt/smr/smr", false},
		{"empty image", "/opt/smr/smr serve", "", false},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := executablePrefixesCommandLine(testCase.cmdline, testCase.exe); got != testCase.want {
				t.Errorf("executablePrefixesCommandLine(%q, %q) = %v, want %v", testCase.cmdline, testCase.exe, got, testCase.want)
			}
		})
	}
}

// restartWith has to fall through to a detached start when the service manager
// is missing, and must never start a second daemon before stopping the first.
func TestRestartWith(t *testing.T) {
	cases := []struct {
		name        string
		goos        string
		stopErr     error
		managerErr  error
		wantCommand string
		wantStarts  int
		wantErr     bool
	}{
		{name: "windows task", goos: "windows", wantCommand: "schtasks /Run /TN AcmeAI SMR", wantStarts: 0},
		{name: "windows startup fallback", goos: "windows", managerErr: errors.New("no task"), wantCommand: "schtasks /Run /TN AcmeAI SMR", wantStarts: 1},
		{name: "linux unit", goos: "linux", wantCommand: "systemctl --user restart smr.service", wantStarts: 0},
		{name: "linux without unit", goos: "linux", managerErr: errors.New("no unit"), wantCommand: "systemctl --user restart smr.service", wantStarts: 1},
		{name: "macOS launch agent", goos: "darwin", wantCommand: "launchctl kickstart -k gui/501/vn.acmeai.smr", wantStarts: 0},
		{name: "macOS without agent", goos: "darwin", managerErr: errors.New("no agent"), wantCommand: "launchctl kickstart -k gui/501/vn.acmeai.smr", wantStarts: 1},
		{name: "stop failure aborts", goos: "linux", stopErr: errors.New("cannot verify"), wantStarts: 0, wantErr: true},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			var stopped, started int
			var commands []string
			deps := restartDeps{
				goos:          testCase.goos,
				uid:           501,
				stopDetached:  func() error { stopped++; return testCase.stopErr },
				stopWindows:   func(context.Context) error { return nil },
				startDetached: func() error { started++; return nil },
				command: func(_ context.Context, name string, args ...string) error {
					commands = append(commands, strings.Join(append([]string{name}, args...), " "))
					if testCase.managerErr != nil {
						return testCase.managerErr
					}
					return nil
				},
			}
			err := restartWith(context.Background(), deps)
			if (err != nil) != testCase.wantErr {
				t.Fatalf("restartWith error = %v, wantErr %v", err, testCase.wantErr)
			}
			if started != testCase.wantStarts {
				t.Errorf("detached starts = %d, want %d", started, testCase.wantStarts)
			}
			if testCase.wantErr {
				if stopped != 1 || len(commands) != 0 {
					t.Errorf("stopped = %d, commands = %v; want the stop to abort first", stopped, commands)
				}
				return
			}
			// The managed path stops once; the detached fallback stops before
			// it starts so the new daemon does not hit ErrAlreadyRunning.
			wantStops := 1
			if testCase.wantStarts == 1 {
				wantStops = 2
			}
			if stopped != wantStops {
				t.Errorf("stops = %d, want %d", stopped, wantStops)
			}
			if len(commands) == 0 || commands[0] != testCase.wantCommand {
				t.Fatalf("commands = %v, want first %q", commands, testCase.wantCommand)
			}
		})
	}
}
