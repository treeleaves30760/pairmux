// Command pairmux gives AI agents reliable terminal primitives on tmux. It is a
// thin entry point: all parsing, routing, and rendering live in internal/cli.
package main

import (
	"os"

	"github.com/treeleaves30760/pairmux/internal/cli"
)

func main() {
	os.Exit(cli.Main(os.Args[1:]))
}
