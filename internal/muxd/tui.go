package muxd

import "errors"

// ErrNoTUI is RawPoll's answer when the terminal cannot host pairmux tui at
// all. Only a Windows console without VT input (older than Windows 10 1809)
// gives it; a Unix terminal always can.
var ErrNoTUI = errors.New("this console cannot host pairmux tui: it needs VT input (Windows 10 1809 or later, or Windows Terminal)")
