package secrets

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"time"
)

// platformKeychainPresent asks macOS for the default keychain path, which is
// read-only and never shows a dialog, and checks the file exists.
func platformKeychainPresent() bool {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "/usr/bin/security", "default-keychain").Output()
	if err != nil {
		return false
	}
	path := strings.Trim(strings.TrimSpace(string(out)), `"`)
	_, err = os.Stat(path)
	return err == nil
}
