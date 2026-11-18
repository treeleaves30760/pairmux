//go:build !windows

package cli

import "os"

// updateRenameNoReplace only serves the Windows layout, which runs here in
// tests. A hard link fails if the destination exists; the file keeps its
// identity, as a Windows rename does.
func updateRenameNoReplace(from, to string) error {
	if err := os.Link(from, to); err != nil {
		return err
	}
	if err := os.Remove(from); err != nil {
		_ = os.Remove(to)
		return err
	}
	return nil
}
