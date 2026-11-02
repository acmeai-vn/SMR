package service

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
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

// A second daemon must fail before it can bind a port or rewrite ui-token.
func TestAcquireLockIsExclusive(t *testing.T) {
	dataDir := t.TempDir()
	lock, err := AcquireLock(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Release()

	if _, err := AcquireLock(dataDir); !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("second AcquireLock error = %v, want ErrAlreadyRunning", err)
	}
	if _, err := os.Stat(filepath.Join(dataDir, daemonPIDFile)); err != nil {
		t.Fatalf("pid file missing: %v", err)
	}
}

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
