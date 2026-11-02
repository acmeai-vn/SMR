//go:build !windows

package fileperm

import "os"

// Restrict makes path readable and writable only by the current user.
func Restrict(path string) error {
	return os.Chmod(path, 0o600)
}

// RestrictDir makes a directory accessible only by the current user.
func RestrictDir(path string) error {
	return os.Chmod(path, 0o700)
}
