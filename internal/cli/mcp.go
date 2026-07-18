package cli

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/treeleaves30760/pairmux/internal/mcpserver"
	"github.com/treeleaves30760/pairmux/internal/version"
)

// cmdMCP starts the newline-delimited MCP stdio transport. Once serving begins,
// stdout belongs exclusively to JSON-RPC; startup and transport failures are
// therefore reported on stderr rather than through a pairmux.v1 envelope.
func (c *Ctx) cmdMCP(args []string) int {
	if len(args) != 1 || args[0] != "serve" {
		return c.usage("pairmux mcp serve", "serve is the only supported MCP transport command")
	}
	executable, err := os.Executable()
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "pairmux mcp: resolve executable: %v\n", err)
		return 1
	}
	server := mcpserver.New(
		mcpserver.SubprocessExecutor{Path: executable},
		c.Tmux.Socket,
		version.Version,
		os.Stderr,
	)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer stop()
	if err := server.Serve(ctx, os.Stdin, c.Stdout); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "pairmux mcp: %v\n", err)
		return 1
	}
	return 0
}
