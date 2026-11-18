//go:build !windows

package detect

import (
	"fmt"
	"os"
)

// ConsoleProbeMain exists only so the CLI can dispatch ConsoleProbeCommand on
// every platform; there is no Windows console to read anywhere else.
func ConsoleProbeMain([]string) int {
	fmt.Fprintln(os.Stdout, formatProbeReply(consoleProbe{}, fmt.Errorf("%s is Windows-only", ConsoleProbeCommand)))
	return 0
}
