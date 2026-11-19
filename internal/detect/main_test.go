package detect

import (
	"os"
	"testing"
)

// testChildren are programs this test binary can be re-run as, keyed by the
// first argument. The Windows tests use them as the programs inside a pseudo
// console (see console_windows_test.go).
var testChildren = map[string]func() int{}

// TestMain lets the test binary stand in for the pairmux executable: run with
// ConsoleProbeCommand it is the console helper, as cli.Main dispatches it, and
// run with a registered child name it is that child. Neither runs any test.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 {
		if os.Args[1] == ConsoleProbeCommand {
			os.Exit(ConsoleProbeMain(os.Args[2:]))
		}
		if child := testChildren[os.Args[1]]; child != nil {
			os.Exit(child())
		}
	}
	os.Exit(m.Run())
}
