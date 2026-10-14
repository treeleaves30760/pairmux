//go:build windows

package mcpserver

import "errors"

func detachSession() error { return errors.New("no sessions on Windows") }
