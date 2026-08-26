//go:build !darwin && !linux

package detect

// readFgWait has no implementation off the platforms pairmux supports, so every
// caller degrades to the line discipline and the text heuristics rather than
// failing to build.
func readFgWait(string) FgWait { return FgUnknown }
