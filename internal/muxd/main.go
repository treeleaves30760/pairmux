package muxd

import (
	"log"
	"os"
)

// ServeCommand is the hidden subcommand a pairmux binary runs as a server.
const ServeCommand = "__muxd"

// ServeMain is the entry point behind `pairmux __muxd --socket NAME`, which
// pairmux commands spawn on demand. It is not a user-facing command.
func ServeMain(args []string) int {
	socket := ""
	for i := 0; i < len(args); i++ {
		if args[i] == "--socket" && i+1 < len(args) {
			socket = args[i+1]
			i++
		}
	}
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	log.SetOutput(os.Stderr)
	if err := Serve(socket); err != nil {
		log.Printf("%v", err)
		return 1
	}
	return 0
}

// serverExecutable is the program spawned as the server: this pairmux binary.
// A package variable so tests can point it elsewhere.
var serverExecutable = os.Executable
