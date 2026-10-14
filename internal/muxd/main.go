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

// truncateLog empties the server log once this process owns the endpoint.
// Spawners open the log for appending, not truncating, so a starter that loses
// the race for the endpoint cannot wipe the running server's log; the winner
// starts it afresh here. Best effort: stderr may not be a regular file.
func truncateLog() {
	if fi, err := os.Stderr.Stat(); err == nil && fi.Mode().IsRegular() {
		_ = os.Stderr.Truncate(0)
	}
}
