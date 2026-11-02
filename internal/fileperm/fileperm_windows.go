//go:build windows

package fileperm

import (
	"fmt"
	"os/exec"
	"os/user"
	"strings"
)

// Restrict resets inherited and explicit ACLs, then grants full access only to
// the current user. SYSTEM is intentionally removed as well.
func Restrict(path string) error {
	account, err := currentAccount()
	if err != nil {
		return err
	}
	if err := icacls(path, "/reset"); err != nil {
		return err
	}
	return icacls(path, "/inheritance:r", "/grant:r", account+":F")
}

func icacls(path string, args ...string) error {
	output, err := exec.Command("icacls", append([]string{path}, args...)...).CombinedOutput()
	if err != nil {
		message := strings.TrimSpace(string(output))
		if message != "" {
			return fmt.Errorf("fileperm: icacls %s: %s", path, message)
		}
		return fmt.Errorf("fileperm: icacls %s: %w", path, err)
	}
	return nil
}

// RestrictDir applies the same user-only rule to a directory. The rule is not
// inherited by existing children, so files are restricted individually too.
func RestrictDir(path string) error {
	return Restrict(path)
}

func currentAccount() (string, error) {
	account, err := user.Current()
	if err == nil && strings.TrimSpace(account.Username) != "" {
		return account.Username, nil
	}
	return "", fmt.Errorf("fileperm: cannot resolve the current Windows user: %w", err)
}
