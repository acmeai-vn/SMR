package service

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// daemonPIDFile is the single-instance lock. It lives next to the UI token so
// one data directory always maps to one daemon.
const daemonPIDFile = "daemon.pid"

// ErrAlreadyRunning means another SMR daemon holds the data directory.
var ErrAlreadyRunning = errors.New("another SMR daemon is already running")

// Lock is the daemon's single-instance lock, held from before the ports are
// bound until shutdown. Without it a second start could overwrite ui-token and
// then die on the busy port, leaving the running daemon rejecting the token the
// setup page just printed.
type Lock struct {
	path string
}

// AcquireLock takes the daemon lock in dataDir. A pid file left behind by a
// crash does not block later starts: it is only trusted while its process
// still exists.
func AcquireLock(dataDir string) (*Lock, error) {
	path := filepath.Join(dataDir, daemonPIDFile)
	for attempt := 0; attempt < 3; attempt++ {
		file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			_, writeErr := fmt.Fprintf(file, "%d\n", os.Getpid())
			closeErr := file.Close()
			if writeErr != nil {
				_ = os.Remove(path)
				return nil, writeErr
			}
			if closeErr != nil {
				_ = os.Remove(path)
				return nil, closeErr
			}
			return &Lock{path: path}, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, err
		}
		pid, running := readLockOwner(path)
		if running {
			return nil, fmt.Errorf("%w (pid %d)", ErrAlreadyRunning, pid)
		}
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
	}
	return nil, fmt.Errorf("could not acquire %s", path)
}

// Release removes the pid file, but only if this process still owns it. A
// newer daemon that already replaced the lock keeps its file.
func (l *Lock) Release() error {
	if l == nil {
		return nil
	}
	pid, _ := readLockOwner(l.path)
	if pid != os.Getpid() {
		return nil
	}
	return os.Remove(l.path)
}

// readLockOwner returns the pid stored in path and whether it is alive.
func readLockOwner(path string) (int, bool) {
	body, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(body)))
	if err != nil || pid <= 0 {
		return 0, false
	}
	return pid, processAlive(pid)
}
