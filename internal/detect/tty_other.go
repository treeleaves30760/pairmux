//go:build !darwin && !linux && !windows

package detect

// readTTYState has no implementation off the platforms pairmux supports, so
// every caller degrades to the text heuristics rather than failing to build.
func readTTYState(string) TTYState { return TTYState{} }
