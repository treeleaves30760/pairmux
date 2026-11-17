package cli

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/treeleaves30760/pairmux/internal/core"
	"github.com/treeleaves30760/pairmux/internal/mux"
)

// This file draws the TUI. The layout follows btop's lead: every region is a
// rounded box, its name and its controls live in the box's border, and every
// control shows the key that triggers it highlighted — so the screen is its
// own key reference — and is also a button the mouse can click.
//
//	 pairmux  default · tmux   ● 2 running  ◆ 1 needs input          15:04:05
//	╭┐terminals 4┌──────────────┐/ filter┌╮╭┐¹screen┌┐²activity┌ boss/a ─┐open┌╮
//	│   name          status        age   ││ $ make test                       │
//	│▌▾ ● boss        running       2s    ││ ok   ./...                        │
//	╰┘new┌┘child┌┘delete┌┘x prune┌───────╯╰┘attach┌┘input┌┘m note┌───────────╯
//	 hint or last action                           : command  ? help  q quit

// --- primitives --------------------------------------------------------------

type seg struct {
	text  string
	style string // SGR parameters, "" for plain
}

type tline []seg

func (l tline) width() int {
	w := 0
	for _, s := range l {
		w += strWidth(s.text)
	}
	return w
}

// strWidth is how many cells s takes in this terminal (see tuiCells).
func strWidth(s string) int {
	w := 0
	for _, r := range s {
		w += runeCells(r)
	}
	return w
}

// clip cuts s to at most w cells, ending in … when it had to cut.
func clip(s string, w int) string {
	if w <= 0 {
		return ""
	}
	if strWidth(s) <= w {
		return s
	}
	var b strings.Builder
	used := 0
	for _, r := range s {
		rw := runeCells(r)
		if used+rw > w-1 {
			break
		}
		b.WriteRune(r)
		used += rw
	}
	b.WriteString(gl("…", "~"))
	return b.String()
}

func pad(s string, w int) string {
	s = clip(s, w)
	if n := w - strWidth(s); n > 0 {
		s += strings.Repeat(" ", n)
	}
	return s
}

// render writes l into exactly w cells, with fill as the style of padding.
func (a *tuiApp) render(l tline, w int, fill string) string {
	var b strings.Builder
	used := 0
	for _, s := range l {
		if used >= w {
			break
		}
		t := clip(s.text, w-used)
		used += strWidth(t)
		b.WriteString(a.sty(s.style, t))
	}
	if used < w {
		b.WriteString(a.sty(fill, strings.Repeat(" ", w-used)))
	}
	return b.String()
}

func (a *tuiApp) sty(style, s string) string {
	if a.noColor || style == "" || s == "" {
		return s
	}
	return "\x1b[" + style + "m" + s + "\x1b[0m"
}

const (
	stAccent  = "1;38;5;16;48;5;79"
	stTitle   = "1;38;5;255"
	stDim     = "38;5;245"
	stSel     = "1;48;5;237"
	stRunning = "38;5;78"
	stAwait   = "1;38;5;220"
	stIdle    = "38;5;111"
	stDead    = "38;5;203"
	stBorder  = "38;5;240"
	stKey     = "1;38;5;79"
	stOK      = "38;5;78"
	stErr     = "1;38;5;203"
	stLabel   = "38;5;252"
	stHead    = "1;38;5;244"

	// Each box has its own border colour, as in btop, so the eye finds a
	// region by colour before reading anything.
	stBoxList    = "38;5;73"
	stBoxPreview = "38;5;140"
	stBoxView    = "38;5;179"
	stBoxPopup   = "38;5;250"
)

// statusLook is a status's one-cell glyph, colour and words.
func statusLook(s core.Status) (glyph, style, label string) {
	switch s {
	case core.StatusRunning:
		return gl("●", "*"), stRunning, "running"
	case core.StatusAwaitingInput:
		return gl("◆", "!"), stAwait, "needs input"
	case core.StatusIdle:
		return gl("○", "o"), stIdle, "idle"
	case core.StatusDead:
		return gl("✕", "x"), stDead, "dead"
	}
	return "?", stDim, string(s)
}

// --- buttons and clickable regions ---------------------------------------------

// hit is a clickable cell range on one screen row.
type hit struct {
	y, x0, x1 int
	fn        func()
}

// rect is a screen region, used for the mouse wheel.
type rect struct{ x0, y0, x1, y1 int }

func (r rect) has(x, y int) bool { return x >= r.x0 && x < r.x1 && y >= r.y0 && y < r.y1 }

// bItem is one thing set into a box border: a button (key + label), a tab, or
// plain text.
type bItem struct {
	key, label string
	fn         func()
	tab, on    bool  // a tab; on = the one showing
	text       tline // plain text instead of a button
}

// press returns a button action that behaves exactly like typing the key, so
// a click can never do anything the keyboard cannot.
func (a *tuiApp) press(name string, r rune) func() {
	return func() { a.handleKey(tuiKey{name: name, r: r}) }
}

func (a *tuiApp) btn(key, label string) bItem {
	var fn func()
	switch key {
	case "⏎":
		fn = a.press("enter", 0)
	case "esc":
		fn = a.press("esc", 0)
	case "tab":
		fn = a.press("tab", 0)
	case "¹":
		fn = a.press("rune", '1')
	case "²":
		fn = a.press("rune", '2')
	default:
		r := []rune(key)
		fn = a.press("rune", r[0])
	}
	return bItem{key: key, label: label, fn: fn}
}

// segs renders an item between the border's open and close pieces. A label
// that starts with its key highlights that letter in place ("new" for n),
// otherwise the key comes first ("x prune").
func (it bItem) segs(open, close, border string) tline {
	l := tline{{open, border}}
	switch {
	case it.text != nil:
		l = append(l, it.text...)
	case it.tab:
		lst := stDim
		if it.on {
			lst = stTitle
		}
		l = append(l, seg{it.key, stKey}, seg{it.label, lst})
	case it.key != "" && strings.HasPrefix(it.label, it.key):
		l = append(l, seg{it.key, stKey}, seg{strings.TrimPrefix(it.label, it.key), stLabel})
	default:
		l = append(l, seg{it.key, stKey}, seg{" " + it.label, stLabel})
	}
	return append(l, seg{close, border})
}

// border draws one horizontal box edge of width w at screen row y, starting at
// column x0, with items set into it on the left and right. Items that do not
// fit are dropped from the end, right side first. Buttons become clickable.
func (a *tuiApp) border(y, x0, w int, top bool, style string, left, right []bItem) string {
	lc, rc, open, close := gl("╭", "+"), gl("╮", "+"), gl("┐", "["), gl("┌", "]")
	if !top {
		lc, rc, open, close = gl("╰", "+"), gl("╯", "+"), gl("┘", "["), gl("└", "]")
	}
	hz := gl("─", "-")
	width := func(items []bItem) int {
		n := 0
		for _, it := range items {
			n += it.segs(open, close, style).width()
		}
		return n
	}
	avail := w - 4 // corners and one ─ on each side
	for len(right) > 0 && width(left)+width(right) > avail {
		right = right[:len(right)-1]
	}
	for len(left) > 0 && width(left) > avail {
		left = left[:len(left)-1]
	}
	l := tline{{lc + hz, style}}
	x := x0 + 2
	put := func(items []bItem) {
		for _, it := range items {
			s := it.segs(open, close, style)
			if it.fn != nil {
				a.hits = append(a.hits, hit{y: y, x0: x, x1: x + s.width(), fn: it.fn})
			}
			x += s.width()
			l = append(l, s...)
		}
	}
	put(left)
	gap := max(0, avail-width(left)-width(right))
	l = append(l, seg{strings.Repeat(hz, gap), style})
	x += gap
	put(right)
	l = append(l, seg{hz + rc, style})
	return a.render(l, w, "")
}

// box frames body lines (each already exactly w-2 cells) into h rows.
func (a *tuiApp) box(y0, x0, w, h int, style string, topL, topR, botL, botR []bItem, body []string) []string {
	out := make([]string, 0, h)
	out = append(out, a.border(y0, x0, w, true, style, topL, topR))
	side := a.sty(style, gl("│", "|"))
	blank := strings.Repeat(" ", max(0, w-2))
	for i := 0; i < h-2; i++ {
		row := blank
		if i < len(body) {
			row = body[i]
		}
		out = append(out, side+row+side)
	}
	return append(out, a.border(y0+h-1, x0, w, false, style, botL, botR))
}

// --- geometry ---------------------------------------------------------------

// bodyHeight is the rows between the header and the footer.
func (a *tuiApp) bodyHeight() int { return max(6, max(a.h, 10)-2) }

func (a *tuiApp) wide() bool { return max(a.w, 40) >= 110 }

// listBoxHeight is the terminals box's height including its borders.
func (a *tuiApp) listBoxHeight() int {
	if a.wide() {
		return a.bodyHeight()
	}
	return max(5, a.bodyHeight()*55/100)
}

// listHeight is how many terminal rows the list shows at once.
func (a *tuiApp) listHeight() int { return max(1, a.listBoxHeight()-3) }

// --- the frame ----------------------------------------------------------------

func (a *tuiApp) draw() {
	w, h := max(a.w, 40), max(a.h, 10)
	a.hits = a.hits[:0]
	a.listRect = rect{}
	lines := make([]string, 0, h)
	lines = append(lines, a.drawHeader(w))

	vis := a.visible()
	bodyH := a.bodyHeight()
	switch {
	case a.viewer != nil && (a.mode == modeView || a.mode == modeForm && a.form != nil && a.form.title == commandTitle):
		lines = append(lines, a.drawViewer(1, w, bodyH)...)
	case a.wide():
		listW := w * 52 / 100
		left := a.drawList(vis, 1, 0, listW, bodyH)
		right := a.drawPreview(vis, 1, listW, w-listW, bodyH)
		for i := 0; i < bodyH; i++ {
			lines = append(lines, left[i]+right[i])
		}
	default:
		listH := a.listBoxHeight()
		lines = append(lines, a.drawList(vis, 1, 0, w, listH)...)
		lines = append(lines, a.drawPreview(vis, 1+listH, 0, w, bodyH-listH)...)
	}
	lines = append(lines, a.drawFooter(vis, h-1, w))

	// Overlays sit on top and take the mouse: only their own buttons respond.
	switch a.mode {
	case modeHelp:
		a.hits = a.hits[:0]
		a.overlay(lines, w, "Keys · click anywhere to close", helpLines(), nil)
	case modeConfirm:
		a.hits = a.hits[:0]
		body := wrap(a.confirm.msg, min(64, w-8))
		a.overlay(lines, w, a.confirm.title, body, []bItem{a.btn("y", "yes"), a.btn("n", "no")})
	case modeForm:
		a.hits = a.hits[:0]
		a.overlayForm(lines, w)
	}

	// The frame is laid out for at least 40x10. On a smaller terminal it is
	// clipped rather than written whole: a row past the last one would scroll
	// the screen and put every click on the wrong row (autowrap is off, see
	// tuiEnter, so over-wide rows are cut by the terminal itself).
	//
	// Each row is erased before it is written, never after: with autowrap off
	// the cursor stays ON the last column after a full-width row, so an erase
	// to end of line there would wipe the row's last cell — the right border.
	rowsOut := h
	if a.h > 0 && a.h < rowsOut {
		rowsOut = a.h
	}
	var b strings.Builder
	b.WriteString("\x1b[H")
	for i, l := range lines {
		if i >= rowsOut {
			break
		}
		b.WriteString("\x1b[2K")
		b.WriteString(l)
		b.WriteString("\x1b[0m")
		if i < rowsOut-1 {
			b.WriteString("\r\n")
		}
	}
	io.WriteString(a.out, b.String())
}

func (a *tuiApp) drawHeader(w int) string {
	counts := map[core.Status]int{}
	for _, n := range a.nodes {
		counts[n.Status]++
	}
	hdr := tline{{" pairmux ", stAccent}, {"  " + a.c.Mux.SocketName() + " · " + a.c.Mux.Backend(), stDim}, {"   ", ""}}
	for _, s := range []core.Status{core.StatusRunning, core.StatusAwaitingInput, core.StatusIdle, core.StatusDead} {
		if counts[s] > 0 {
			g, st, label := statusLook(s)
			hdr = append(hdr, seg{g + " ", st}, seg{fmt.Sprintf("%d %s   ", counts[s], label), ""})
		}
	}
	clock := time.Now().Format("15:04:05")
	if gap := w - hdr.width() - strWidth(clock) - 1; gap > 0 {
		hdr = append(hdr, seg{strings.Repeat(" ", gap), ""})
	}
	hdr = append(hdr, seg{clock + " ", stDim})
	return a.render(hdr, w, "")
}

func (a *tuiApp) drawFooter(vis []visRow, y, w int) string {
	var left tline
	switch {
	case a.flash != "" && time.Since(a.flashAt) < 6*time.Second:
		st := stDim
		switch a.flashKind {
		case 'o':
			st = stOK
		case 'e':
			st = stErr
		}
		left = tline{{" " + a.flash, st}}
	case a.listErr != nil:
		left = tline{{" error listing terminals: " + a.listErr.Error(), stErr}}
	case a.mode == modeView:
		left = tline{{" " + viewerHint(a.viewer), stDim}}
	case a.sel < len(vis) && len(vis) > 0:
		left = a.hintFor(vis[a.sel].node)
	}
	// The reader carries its own buttons in its border.
	var right []bItem
	if a.mode != modeView {
		right = []bItem{a.btn(":", "command"), a.btn("r", "refresh"), a.btn("?", "help"), a.btn("q", "quit")}
	}
	// The footer's buttons are borderless; one space separates them.
	var rl tline
	type span struct {
		x0, x1 int
		fn     func()
	}
	var spans []span
	x := 0
	for _, it := range right {
		s := it.segs("  ", "", "")
		spans = append(spans, span{x, x + s.width(), it.fn})
		x += s.width()
		rl = append(rl, s...)
	}
	rl = append(rl, seg{" ", ""})
	rw := rl.width()
	if rw > w {
		return a.render(left, w, "")
	}
	start := w - rw
	for _, s := range spans {
		a.hits = append(a.hits, hit{y: y, x0: start + s.x0, x1: start + s.x1, fn: s.fn})
	}
	return a.render(left, start, "") + a.render(rl, rw, "")
}

func (a *tuiApp) drawList(vis []visRow, y0, x0, w, h int) []string {
	inner := w - 2
	rows := h - 3 // borders and the column header
	a.listRect = rect{x0, y0 + 2, x0 + w, y0 + h - 1}

	title := tline{{"terminals", stTitle}, {fmt.Sprintf(" %d", len(vis)), stDim}}
	filterBtn := a.btn("/", "filter")
	if a.filter != "" || a.mode == modeFilter {
		cursor := ""
		if a.mode == modeFilter {
			cursor = gl("█", "_")
		}
		filterBtn = bItem{text: tline{{"/", stKey}, {a.filter + cursor, stAwait}}, fn: a.press("rune", '/')}
	}
	topL := []bItem{{text: title}}
	topR := []bItem{filterBtn}
	botL := []bItem{a.btn("n", "new"), a.btn("c", "child"), a.btn("d", "delete"), a.btn("x", "prune dead")}
	botR := []bItem{a.btn("␣", "fold")}
	botR[0].fn = a.press("rune", ' ')

	if a.sel < a.offset {
		a.offset = a.sel
	}
	if a.sel >= a.offset+rows {
		a.offset = a.sel - rows + 1
	}
	a.offset = max(0, min(a.offset, max(0, len(vis)-rows)))

	nameW := 12
	for _, r := range vis {
		shown := r.node.Term.Name
		if a.filter != "" {
			shown = r.node.Path
		}
		nameW = max(nameW, strWidth(r.prefix)+4+strWidth(shown))
	}
	nameW = min(nameW, max(16, inner/2))

	body := []string{a.render(tline{{"   " + pad("name", nameW-2) + "  ", stHead}, {pad("status", 13), stHead},
		{pad("age", 6), stHead}, {"command", stHead}}, inner, "")}
	if len(vis) == 0 {
		msg := "  no terminals yet — press n (or click new) to create one"
		if a.filter != "" {
			msg = "  nothing matches the filter (esc clears it)"
		}
		body = append(body, a.render(tline{{msg, stDim}}, inner, ""))
	}
	for i := a.offset; i < len(vis) && len(body) <= rows; i++ {
		r := vis[i]
		n := r.node
		g, st, label := statusLook(n.Status)
		fold := " "
		if n.Child != "" {
			fold = gl("▾", "v")
			if a.collapsed[n.Path] {
				fold = gl("▸", ">")
			}
		}
		name := n.Term.Name
		if a.filter != "" {
			name = n.Path
		}
		selected := i == a.sel
		mark := " "
		if selected {
			mark = gl("▌", ">")
		}
		age := "-"
		if !n.LastMod.IsZero() {
			age = humanizeAge(time.Since(n.LastMod))
		}
		var badges []string
		if n.Notes > 0 {
			badges = append(badges, fmt.Sprintf("✉%d", n.Notes))
		}
		if n.Child != "" && a.collapsed[n.Path] {
			badges = append(badges, fmt.Sprintf("+%d", a.descendants(n)))
		}
		l := tline{
			{mark, stKey},
			{r.prefix, stBorder},
			{fold + " ", stDim},
			{g + " ", st},
			{pad(name, max(1, nameW-strWidth(r.prefix)-4)) + "  ", ""},
			{pad(label, 13), st},
			{pad(age, 6), stDim},
		}
		if len(badges) > 0 {
			l = append(l, seg{strings.Join(badges, " ") + " ", stAwait})
		}
		l = append(l, seg{n.Pending, stDim})
		fill := ""
		if selected {
			for k := range l {
				if l[k].style == "" {
					l[k].style = stSel
				} else {
					l[k].style += ";48;5;237"
				}
			}
			fill = stSel
		}
		row := y0 + 1 + len(body)
		idx := i
		if n.Child != "" { // the fold arrow folds, the rest of the row selects
			fx := x0 + 1 + 1 + strWidth(r.prefix)
			a.hits = append(a.hits, hit{y: row, x0: fx, x1: fx + 2, fn: func() { a.toggleFold(idx) }})
		}
		a.hits = append(a.hits, hit{y: row, x0: x0 + 1, x1: x0 + w - 1, fn: func() { a.clickRow(idx) }})
		body = append(body, a.render(l, inner, fill))
	}
	return a.box(y0, x0, w, h, stBoxList, topL, topR, botL, botR, body)
}

func (a *tuiApp) drawPreview(vis []visRow, y0, x0, w, h int) []string {
	if h < 3 {
		return nil
	}
	inner := w - 2
	var topL []bItem
	for i, name := range viewNames {
		key := []string{"¹", "²"}[i]
		it := a.btn(key, name)
		it.tab, it.on = true, i == a.view
		topL = append(topL, it)
	}
	var body []string
	if a.sel < len(vis) && len(vis) > 0 {
		n := vis[a.sel].node
		g, st, label := statusLook(n.Status)
		topL = append(topL, bItem{text: tline{{" " + n.Path + " ", ""}, {g + " " + label + " ", st}}})
	}
	topR := []bItem{a.btn("o", "open")}
	botL := []bItem{a.btn("⏎", "attach"), a.btn("i", "input"), a.btn("m", "note")}
	if a.previewMeta != "" && h > 4 {
		body = append(body, a.render(tline{{" " + a.previewMeta, stDim}}, inner, ""))
	}
	room := h - 2 - len(body)
	src := a.preview
	if len(src) > room {
		src = src[len(src)-room:]
	}
	for _, l := range src {
		body = append(body, a.render(tline{{" " + strings.ReplaceAll(l, "\t", "    "), ""}}, inner, ""))
	}
	if len(a.preview) == 0 && room > 0 && len(vis) > 0 {
		body = append(body, a.render(tline{{" (no output yet)", stDim}}, inner, ""))
	}
	return a.box(y0, x0, w, h, stBoxPreview, topL, topR, botL, nil, body)
}

// --- mouse --------------------------------------------------------------------

const doubleClick = 400 * time.Millisecond

// clickRow selects a list row; a second click on the same row attaches.
func (a *tuiApp) clickRow(idx int) {
	vis := a.visible()
	if idx < 0 || idx >= len(vis) {
		return
	}
	again := idx == a.sel && time.Since(a.lastClick) < doubleClick
	a.sel, a.selPath = idx, vis[idx].node.Path
	a.lastClick = time.Now()
	if again {
		a.lastClick = time.Time{}
		n := vis[idx].node
		a.doAttach(&n)
		return
	}
	a.loadPreview()
}

func (a *tuiApp) toggleFold(idx int) {
	vis := a.visible()
	if idx < 0 || idx >= len(vis) {
		return
	}
	p := vis[idx].node.Path
	a.collapsed[p] = !a.collapsed[p]
	a.selectPath(p)
}

// mouse acts on one SGR mouse report: a left click presses whatever button or
// row is under it, the wheel scrolls the box under it.
func (a *tuiApp) mouse(k tuiKey) {
	x, y := k.mx, k.my
	if k.wheel != 0 {
		switch {
		case a.mode == modeView && a.viewer != nil:
			a.viewer.off = max(0, a.viewer.off+3*k.wheel)
		case a.mode == modeList && a.listRect.has(x, y):
			a.move(k.wheel)
		}
		return
	}
	if a.mode == modeHelp {
		a.mode = modeList
		return
	}
	for _, h := range a.hits {
		if h.y == y && x >= h.x0 && x < h.x1 {
			if a.mode == modeFilter { // clicking elsewhere ends typing the filter
				a.mode = modeList
			}
			h.fn()
			return
		}
	}
}

// --- hints, help, overlays --------------------------------------------------------

func (a *tuiApp) hintFor(n treeNode) tline {
	g, _, _ := statusLook(n.Status)
	switch n.Status {
	case core.StatusAwaitingInput:
		return tline{{" " + g + " " + n.Path + " is waiting for input — ⏎ attach to answer it, or i to send a line", stAwait}}
	case core.StatusDead:
		return tline{{" " + g + " " + n.Path + " is dead — d deletes its journal, x deletes every dead terminal", stDead}}
	}
	if n.Notes > 0 {
		return tline{{fmt.Sprintf(" ✉ %s has %d note(s) its agent has not read yet", n.Path, n.Notes), stDim}}
	}
	if n.Child != "" {
		return tline{{" " + n.Path + " drives its own terminals — space folds them, c creates one under it", stDim}}
	}
	if a.c.Mux.Backend() == mux.BackendTmux && insideTmux() {
		return tline{{" ⏎ attaches as a nested tmux inside this one — detach with Ctrl-b Ctrl-b d", stDim}}
	}
	return tline{{" ⏎ or double-click attaches · every highlighted letter is a key, every box label a button", stDim}}
}

func helpLines() []string {
	return []string{
		"↑ ↓  j k        move            PgUp PgDn g G   jump",
		"⏎  a            attach (detach: Ctrl-b d)",
		"n               new terminal in the top layer",
		"c               new child terminal under the selected one",
		"d               kill a live terminal / delete a dead one",
		"x               delete every dead terminal's journal",
		"m               leave a note for the terminal's agent",
		"i               send a line of input",
		"space ← →       fold / unfold a terminal's sub-terminals",
		"/               filter by path, command or status",
		"1 2  tab        preview: screen / activity (commands, exit codes, notes)",
		"o               open it full screen: whole output history / activity",
		":               run any pairmux command; {} = the selected path",
		"r               refresh now            q  Ctrl-C  quit",
		"",
		"Mouse: click a row to select it, click it again to attach; click any",
		"label in a box border to press it; the wheel scrolls.",
		"",
		"Agents below may only drive their own subtree; the top is yours.",
	}
}

// overlay draws a centred box over lines, with buttons in its bottom border.
func (a *tuiApp) overlay(lines []string, w int, title string, body []string, buttons []bItem) {
	inner := strWidth(title) + 6
	for _, b := range body {
		inner = max(inner, strWidth(b)+2)
	}
	inner = min(inner, w-4)
	top := max(1, (len(lines)-len(body)-2)/2)
	left := max(0, (w-inner-2)/2)
	put := func(row int, s string) {
		if row < 0 || row >= len(lines) {
			return
		}
		lines[row] = strings.Repeat(" ", left) + s + strings.Repeat(" ", max(0, w-left-inner-2))
	}
	put(top, a.border(top, left, inner+2, true, stBoxPopup, []bItem{{text: tline{{title, stTitle}}}}, nil))
	side := a.sty(stBoxPopup, gl("│", "|"))
	for i, b := range body {
		put(top+1+i, side+a.render(tline{{" " + pad(b, inner-2) + " ", ""}}, inner, "")+side)
	}
	put(top+1+len(body), a.border(top+1+len(body), left, inner+2, false, stBoxPopup, buttons, nil))
}

func (a *tuiApp) overlayForm(lines []string, w int) {
	f := a.form
	inner := min(72, w-6)
	var body []string
	for i, fld := range f.fields {
		cursor := ""
		marker := "  "
		if i == f.focus {
			cursor, marker = gl("█", "_"), gl("▸", ">")+" "
		}
		v := fld.value + cursor
		if fw := inner - 14; strWidth(v) > fw {
			r := []rune(v)
			for strWidth(string(r)) > fw-1 {
				r = r[1:]
			}
			v = gl("…", "~") + string(r)
		}
		body = append(body, marker+pad(fld.label, 10)+" "+v)
		if i == f.focus && fld.hint != "" {
			body = append(body, "             "+fld.hint)
		}
	}
	if f.err != "" {
		body = append(body, "")
		body = append(body, wrap("✕ "+f.err, inner-2)...)
	}
	if len(body) > 0 {
		body[0] = pad(body[0], inner-2) // the form keeps its width as values change
	}
	buttons := []bItem{a.btn("⏎", "submit"), a.btn("esc", "cancel")}
	if len(f.fields) > 1 {
		buttons = append(buttons, a.btn("tab", "next field"))
	}
	a.overlay(lines, w, f.title, body, buttons)
}

func wrap(s string, w int) []string {
	var out []string
	var cur string
	for _, word := range strings.Fields(s) {
		if cur != "" && strWidth(cur)+1+strWidth(word) > w {
			out = append(out, cur)
			cur = word
			continue
		}
		if cur != "" {
			cur += " "
		}
		cur += word
	}
	if cur != "" {
		out = append(out, cur)
	}
	return out
}
