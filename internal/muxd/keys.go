package muxd

import "fmt"

// keyBytes translates one of pairmux's send key names (the tmux key names the
// CLI's allowlist admits) into the bytes a terminal would send for it. Cursor
// and Home/End keys follow the program's DECCKM state the way a real terminal
// does: a full-screen program that asked for application cursor keys gets
// ESC O A, everything else ESC [ A.
func keyBytes(name string, appCursor bool) ([]byte, error) {
	csiOrSS3 := func(final byte) []byte {
		if appCursor {
			return []byte{0x1b, 'O', final}
		}
		return []byte{0x1b, '[', final}
	}
	switch name {
	case "Enter":
		return []byte{'\r'}, nil
	case "Escape":
		return []byte{0x1b}, nil
	case "Tab":
		return []byte{'\t'}, nil
	case "Space":
		return []byte{' '}, nil
	case "BSpace":
		return []byte{0x7f}, nil
	case "Up":
		return csiOrSS3('A'), nil
	case "Down":
		return csiOrSS3('B'), nil
	case "Right":
		return csiOrSS3('C'), nil
	case "Left":
		return csiOrSS3('D'), nil
	case "Home":
		return csiOrSS3('H'), nil
	case "End":
		return csiOrSS3('F'), nil
	case "PPage":
		return []byte("\x1b[5~"), nil
	case "NPage":
		return []byte("\x1b[6~"), nil
	case "DC":
		return []byte("\x1b[3~"), nil
	case "IC":
		return []byte("\x1b[2~"), nil
	case "F1":
		return []byte("\x1bOP"), nil
	case "F2":
		return []byte("\x1bOQ"), nil
	case "F3":
		return []byte("\x1bOR"), nil
	case "F4":
		return []byte("\x1bOS"), nil
	case "F5":
		return []byte("\x1b[15~"), nil
	case "F6":
		return []byte("\x1b[17~"), nil
	case "F7":
		return []byte("\x1b[18~"), nil
	case "F8":
		return []byte("\x1b[19~"), nil
	case "F9":
		return []byte("\x1b[20~"), nil
	case "F10":
		return []byte("\x1b[21~"), nil
	case "F11":
		return []byte("\x1b[23~"), nil
	case "F12":
		return []byte("\x1b[24~"), nil
	}
	if len(name) == 3 && name[1] == '-' {
		c := name[2]
		switch name[0] {
		case 'C':
			switch {
			case c >= 'a' && c <= 'z':
				return []byte{c - 'a' + 1}, nil
			case c >= 'A' && c <= 'Z':
				return []byte{c - 'A' + 1}, nil
			case c == '@' || c == ' ':
				return []byte{0}, nil
			case c == '[':
				return []byte{0x1b}, nil
			case c == '\\':
				return []byte{0x1c}, nil
			case c == ']':
				return []byte{0x1d}, nil
			case c == '^':
				return []byte{0x1e}, nil
			case c == '_':
				return []byte{0x1f}, nil
			case c == '?':
				return []byte{0x7f}, nil
			}
		case 'M':
			if c >= 0x21 && c < 0x7f {
				return []byte{0x1b, c}, nil
			}
		}
	}
	return nil, fmt.Errorf("unknown key %q", name)
}
