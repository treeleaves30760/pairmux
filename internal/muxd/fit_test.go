package muxd

import (
	"errors"
	"fmt"
	"net"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/treeleaves30760/pairmux/internal/mux"
	"github.com/treeleaves30760/pairmux/internal/rundir"
)

// sizeArgv prints the terminal's size at start and on every SIGWINCH, the way
// a full-screen program redraws for its new size.
func sizeArgv() []string {
	return scriptArgv(`trap 'echo "size $(stty size)"' WINCH; echo "size $(stty size)"; while :; do sleep 0.1; done`,
		`ping -n 60 127.0.0.1 >NUL`)
}

func screenOf(t *testing.T, c *Client, id string) mux.ScreenInfo {
	t.Helper()
	s, err := c.PaneScreen(id)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// A fit resizes the program's terminal, the program sees it, and unfit puts
// back the size from before.
func TestFitAndUnfit(t *testing.T) {
	c := startServer(t)
	id, err := c.NewWindow(mux.NewWindowReq{Name: "fit", Argv: sizeArgv()})
	if err != nil {
		t.Fatal(err)
	}
	if s := screenOf(t, c, id); s.Cols != DefaultCols || s.Rows != DefaultRows || s.Fitted || s.Attached != 0 {
		t.Fatalf("fresh pane = %+v", s)
	}
	if err := c.FitPane(id, 80, 20); err != nil {
		t.Fatal(err)
	}
	if s := screenOf(t, c, id); s.Cols != 80 || s.Rows != 20 || !s.Fitted {
		t.Fatalf("fitted pane = %+v", s)
	}
	if runtime.GOOS != "windows" {
		waitFor(t, "the program to see 20x80", func() bool {
			s, _ := c.CapturePane(id, 0)
			return strings.Contains(s, "size 20 80")
		})
	}
	// A renewal at another size keeps the size from before the first fit.
	if err := c.FitPane(id, 90, 25); err != nil {
		t.Fatal(err)
	}
	if err := c.UnfitPane(id); err != nil {
		t.Fatal(err)
	}
	if s := screenOf(t, c, id); s.Cols != DefaultCols || s.Rows != DefaultRows || s.Fitted {
		t.Fatalf("unfitted pane = %+v", s)
	}
	if err := c.UnfitPane(id); err != nil { // not fitted: a no-op
		t.Fatal(err)
	}
}

func attachRaw(t *testing.T, c *Client, id string, cols, rows int) net.Conn {
	t.Helper()
	conn, err := c.dial()
	if err != nil {
		t.Fatal(err)
	}
	if err := writeMsg(conn, request{Op: "attach", Proto: protoVersion, Pane: id, Cols: cols, Rows: rows}); err != nil {
		t.Fatal(err)
	}
	var resp response
	if err := readMsg(conn, &resp); err != nil || !resp.OK {
		t.Fatalf("attach response %+v, %v", resp, err)
	}
	go func() { // drain output frames
		for {
			if _, _, err := readFrame(conn); err != nil {
				return
			}
		}
	}()
	return conn
}

// Attached humans own the size: a fit is refused while one is attached, an
// attach ends a fit, and an unfit afterwards does not take the size away.
func TestFitYieldsToAttachedHumans(t *testing.T) {
	c := startServer(t)
	id, err := c.NewWindow(mux.NewWindowReq{Name: "fit", Argv: sizeArgv()})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.FitPane(id, 80, 20); err != nil {
		t.Fatal(err)
	}
	conn := attachRaw(t, c, id, 150, 40)
	if s := screenOf(t, c, id); s.Cols != 150 || s.Rows != 40 || s.Fitted || s.Attached != 1 {
		t.Fatalf("attached pane = %+v", s)
	}
	if err := c.FitPane(id, 80, 20); !errors.Is(err, mux.ErrPaneWatched) {
		t.Fatalf("fit while attached = %v, want ErrPaneWatched", err)
	}
	if err := c.UnfitPane(id); err != nil {
		t.Fatal(err)
	}
	if s := screenOf(t, c, id); s.Cols != 150 || s.Rows != 40 {
		t.Fatalf("unfit took the attached human's size: %+v", s)
	}
	conn.Write(encodeFrame(frameDetach, nil))
	conn.Close()
	waitFor(t, "detach back to the default size", func() bool {
		s, err := c.PaneScreen(id)
		return err == nil && s.Attached == 0 && s.Cols == DefaultCols && s.Rows == DefaultRows
	})
}

// A fit nobody renews lapses: a TUI that died cannot leave a pane small.
func TestFitLeaseLapses(t *testing.T) {
	c := startServer(t)
	id, err := c.NewWindow(mux.NewWindowReq{Name: "fit", Argv: sizeArgv()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.call(request{Op: "fit", Pane: id, Cols: 70, Rows: 15, LeaseMS: 200}); err != nil {
		t.Fatal(err)
	}
	if s := screenOf(t, c, id); s.Cols != 70 || !s.Fitted {
		t.Fatalf("fitted = %+v", s)
	}
	waitFor(t, "the lease to lapse", func() bool {
		s, err := c.PaneScreen(id)
		return err == nil && !s.Fitted && s.Cols == DefaultCols && s.Rows == DefaultRows
	})
}

// A server from an older pairmux answers the new requests as unknown; the
// client reports that as "cannot fit", not as an error for the human.
func TestFitAgainstAnOlderServer(t *testing.T) {
	t.Setenv("PAIRMUX_TMPDIR", shortRoot(t))
	if _, err := rundir.Ensure(); err != nil {
		t.Fatal(err)
	}
	socket := fmt.Sprintf("old%d", time.Now().UnixNano()%100000)
	path, err := rundir.SocketPath(socket)
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			var req request
			if readMsg(conn, &req) == nil {
				// What v1.6.1's server answers an operation it does not know.
				_ = writeMsg(conn, response{Proto: 1, Error: fmt.Sprintf("unknown operation %q", req.Op)})
			}
			conn.Close()
		}
	}()
	c := New(socket)
	if _, err := c.PaneScreen("%0"); !errors.Is(err, mux.ErrFitUnsupported) {
		t.Fatalf("PaneScreen = %v", err)
	}
	if err := c.FitPane("%0", 80, 20); !errors.Is(err, mux.ErrFitUnsupported) {
		t.Fatalf("FitPane = %v", err)
	}
	if err := c.UnfitPane("%0"); !errors.Is(err, mux.ErrFitUnsupported) {
		t.Fatalf("UnfitPane = %v", err)
	}
}

func TestScreenReportsModes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses printf escapes through sh")
	}
	c := startServer(t)
	id, err := c.NewWindow(mux.NewWindowReq{Name: "modes", Argv: scriptArgv(`printf '\033[?1049h\033[?25l\033[?1000h'; echo ready; sleep 60`, "")})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "modes", func() bool {
		s, err := c.PaneScreen(id)
		return err == nil && s.AltScreen && s.CursorHidden && s.Mouse
	})
}
