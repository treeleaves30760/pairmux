//go:build windows

package rundir

import (
	"fmt"
	"os"
	"path/filepath"
)

// dir is under the per-user local application data folder, whose default ACL
// already admits only the user (plus SYSTEM and Administrators).
func envRoot() string { return os.Getenv("PAIRMUX_TMPDIR") }

func dirUnder(root string) (string, error) {
	if root == "" {
		cache, err := os.UserCacheDir() // %LocalAppData%
		if err != nil {
			return "", fmt.Errorf("locate %%LocalAppData%%: %w (set PAIRMUX_TMPDIR)", err)
		}
		root = filepath.Join(cache, "pairmux")
	}
	return filepath.Join(canonical(root), "run"), nil
}

// Ensure creates the runtime directory if needed.
func Ensure() (string, error) {
	d, err := dir()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(d, 0o700); err != nil {
		return "", fmt.Errorf("create runtime directory %s: %w (set PAIRMUX_TMPDIR to a writable directory)", d, err)
	}
	return d, nil
}
