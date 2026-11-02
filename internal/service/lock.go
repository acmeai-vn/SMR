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
			writeErr := writePIDRecord(file)
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
		record := readPIDRecord(path)
		// A pid file that already names this process is our own lock: a single
		// process must never take it twice, however its command line looks.
		if record.PID == os.Getpid() {
			return nil, fmt.Errorf("%w (pid %d)", ErrAlreadyRunning, record.PID)
		}
		if record.PID > 0 && processAlive(record.PID) {
			match, verifyErr := processMatchesDaemon(record)
			// A verification failure keeps the lock: a false "not running"
			// would let two daemons race for the ports. A definite mismatch is
			// a stale pid reused after a reboot, so the file is replaced.
			if verifyErr != nil || match {
				return nil, fmt.Errorf("%w (pid %d)", ErrAlreadyRunning, record.PID)
			}
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

// pidRecord is what the pid file stores. The executable path lets a later
// restart confirm that a recorded pid still belongs to SMR: after a crash or a
// reboot the same pid can be handed to an unrelated program.
type pidRecord struct {
	PID int
	// Exe is empty for pid files written by builds before this field existed.
	Exe string
}

// writePIDRecord stores the current process in an already-opened lock file.
func writePIDRecord(file *os.File) error {
	exe, err := os.Executable()
	if err != nil {
		exe = ""
	}
	_, err = fmt.Fprintf(file, "%d\n%s\n", os.Getpid(), exe)
	return err
}

// readPIDRecord parses the lock file. Only the first line is required so an
// older pid file is still readable.
func readPIDRecord(path string) pidRecord {
	body, err := os.ReadFile(path)
	if err != nil {
		return pidRecord{}
	}
	lines := strings.SplitN(string(body), "\n", 2)
	pid, err := strconv.Atoi(strings.TrimSpace(lines[0]))
	if err != nil || pid <= 0 {
		return pidRecord{}
	}
	record := pidRecord{PID: pid}
	if len(lines) > 1 {
		record.Exe = strings.TrimSpace(lines[1])
	}
	return record
}

// readLockOwner returns the pid stored in path and whether it is alive.
func readLockOwner(path string) (int, bool) {
	record := readPIDRecord(path)
	if record.PID <= 0 {
		return 0, false
	}
	return record.PID, processAlive(record.PID)
}
