//go:build windows

package cli

import "github.com/treeleaves30760/pairmux/internal/output"

// execTmuxAttach is unreachable on Windows, where the backend is always the
// built-in one.
func (c *Ctx) execTmuxAttach() int {
	return c.fail(output.CodeTmux, "tmux is not available on Windows", "use the built-in backend")
}
