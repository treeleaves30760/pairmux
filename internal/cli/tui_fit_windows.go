//go:build windows

package cli

// processAlive reports every owner alive: fits that outlive their owner exist
// only on tmux, which does not run natively on Windows, so there is nothing
// to sweep and no reason to guess.
func processAlive(int) bool { return true }
