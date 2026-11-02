// Package smrpaths resolves the per-user locations SMR uses on each OS.
package smrpaths

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

// ConfigPath returns the default config path unless SMR_CONFIG overrides it.
func ConfigPath() (string, error) {
	if custom := os.Getenv("SMR_CONFIG"); custom != "" {
		return custom, nil
	}
	base, err := dataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "config.yaml"), nil
}

// DataDir returns the directory for logs, backups and the local UI token.
func DataDir() (string, error) {
	if custom := os.Getenv("SMR_DATA_DIR"); custom != "" {
		return custom, nil
	}
	return dataDir()
}

func dataDir() (string, error) {
	switch runtime.GOOS {
	case "windows":
		base := os.Getenv("LOCALAPPDATA")
		if base == "" {
			return "", fmt.Errorf("LOCALAPPDATA is not set")
		}
		return filepath.Join(base, "AcmeAI", "SMR"), nil
	case "darwin":
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, "Library", "Application Support", "AcmeAI", "SMR"), nil
	default:
		if base := os.Getenv("XDG_DATA_HOME"); base != "" {
			return filepath.Join(base, "acmeai", "smr"), nil
		}
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, ".local", "share", "acmeai", "smr"), nil
	}
}

// EnsureDir creates a private directory.
func EnsureDir(path string) error {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return err
	}
	return os.Chmod(path, 0o700)
}

// HomePath resolves a path relative to the current user's home.
func HomePath(parts ...string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(append([]string{home}, parts...)...), nil
}
