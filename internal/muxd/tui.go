package muxd

import "errors"

// ErrNoTUI is RawPoll's answer where pairmux tui is not available yet: the
// Windows console has no polled-read mode to park the UI's reader with.
var ErrNoTUI = errors.New("pairmux tui is not available on Windows yet; use pairmux watch and pairmux attach")
