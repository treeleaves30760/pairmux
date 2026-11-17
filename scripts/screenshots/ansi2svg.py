#!/usr/bin/env python3
"""Render a terminal capture (tmux capture-pane -e -p) as a window-framed SVG.

Usage: ansi2svg.py [--title T] [--cols N] [--rows N] < capture.ans > out.svg

Only what pairmux's TUI and ordinary shells emit is understood: SGR colours
(16, 256 and truecolor), bold, dim, reverse. Every run of text is pinned to its
cell grid with textLength, so box-drawing borders line up in any monospace font.
"""
import argparse
import html
import re
import sys
import unicodedata

BG = "#0f1419"
FG = "#d6dde6"
BASE16 = [
    "#1c2128", "#ff6b6b", "#5fd38d", "#e5c07b", "#6cb6ff", "#c792ea", "#56d4dd", "#d6dde6",
    "#636e7b", "#ff8787", "#7ee2a8", "#ffd580", "#91cbff", "#dcb0ff", "#7ee8f0", "#ffffff",
]


def xterm256(n):
    if n < 16:
        return BASE16[n]
    if n < 232:
        n -= 16
        steps = [0, 95, 135, 175, 215, 255]
        return "#%02x%02x%02x" % (steps[n // 36], steps[(n // 6) % 6], steps[n % 6])
    v = 8 + (n - 232) * 10
    return "#%02x%02x%02x" % (v, v, v)


def cell_width(ch):
    if unicodedata.combining(ch):
        return 0
    return 2 if unicodedata.east_asian_width(ch) in ("W", "F") else 1


SGR = re.compile(r"\x1b\[([0-9;:]*)m")
OTHER = re.compile(r"\x1b(\[[0-9;?]*[A-Za-z]|\][^\x07]*\x07|[()][A-Z0-9])")


def parse(lines):
    """Yield rows as lists of (text, fg, bg, bold, cells)."""
    rows = []
    for line in lines:
        st = {"fg": None, "bg": None, "bold": False, "rev": False}
        runs = []
        pos = 0
        line = OTHER.sub(lambda m: "" if not m.group(0).endswith("m") else m.group(0), line)
        for m in list(SGR.finditer(line)) + [None]:
            end = m.start() if m else len(line)
            text = line[pos:end]
            if text:
                fg, bg = st["fg"] or FG, st["bg"]
                if st["rev"]:
                    fg, bg = bg or BG, fg
                runs.append((text, fg, bg, st["bold"]))
            if not m:
                break
            pos = m.end()
            codes = [c for c in re.split("[;:]", m.group(1))] or ["0"]
            i = 0
            while i < len(codes):
                c = int(codes[i] or 0)
                if c == 0:
                    st = {"fg": None, "bg": None, "bold": False, "rev": False}
                elif c == 1:
                    st["bold"] = True
                elif c == 22:
                    st["bold"] = False
                elif c == 7:
                    st["rev"] = True
                elif c == 27:
                    st["rev"] = False
                elif 30 <= c <= 37:
                    st["fg"] = BASE16[c - 30]
                elif 90 <= c <= 97:
                    st["fg"] = BASE16[c - 90 + 8]
                elif 40 <= c <= 47:
                    st["bg"] = BASE16[c - 40]
                elif 100 <= c <= 107:
                    st["bg"] = BASE16[c - 100 + 8]
                elif c == 39:
                    st["fg"] = None
                elif c == 49:
                    st["bg"] = None
                elif c in (38, 48) and i + 1 < len(codes):
                    key = "fg" if c == 38 else "bg"
                    if codes[i + 1] == "5" and i + 2 < len(codes):
                        st[key] = xterm256(int(codes[i + 2]))
                        i += 2
                    elif codes[i + 1] == "2" and i + 4 < len(codes):
                        st[key] = "#%02x%02x%02x" % tuple(int(x) for x in codes[i + 2:i + 5])
                        i += 4
                i += 1
        rows.append(runs)
    return rows


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--title", default="pairmux")
    ap.add_argument("--cols", type=int, default=0)
    ap.add_argument("--rows", type=int, default=0)
    args = ap.parse_args()
    lines = sys.stdin.read().rstrip("\n").split("\n")
    rows = parse(lines)
    cols = args.cols or max(sum(cell_width(ch) for t, *_ in r for ch in t) for r in rows)
    nrows = args.rows or len(rows)
    cw, lh, fs = 8.4, 17.0, 14
    padx, top, bottom = 18, 44, 18
    W = int(padx * 2 + cols * cw)
    H = int(top + nrows * lh + bottom)
    out = [
        f'<svg xmlns="http://www.w3.org/2000/svg" width="{W}" height="{H}" viewBox="0 0 {W} {H}" role="img" aria-label="{html.escape(args.title)}">',
        "<style>text{font-family:'JetBrains Mono','SF Mono',Menlo,Consolas,'DejaVu Sans Mono',monospace;"
        f"font-size:{fs}px;white-space:pre;dominant-baseline:central}}.b{{font-weight:700}}</style>",
        f'<rect width="{W}" height="{H}" rx="10" fill="{BG}"/>',
        f'<rect width="{W}" height="32" rx="10" fill="#1a2028"/><rect y="22" width="{W}" height="10" fill="#1a2028"/>',
        '<circle cx="20" cy="16" r="6" fill="#ff5f57"/><circle cx="40" cy="16" r="6" fill="#febc2e"/><circle cx="60" cy="16" r="6" fill="#28c840"/>',
        f'<text x="{W/2}" y="16" fill="#8b95a1" text-anchor="middle" style="font-size:12px">{html.escape(args.title)}</text>',
    ]
    for y, runs in enumerate(rows[:nrows]):
        x = 0
        cy = top + y * lh
        for text, fg, bg, bold in runs:
            n = sum(cell_width(ch) for ch in text)
            if n == 0:
                continue
            px = padx + x * cw
            if bg:
                out.append(f'<rect x="{px:.1f}" y="{cy:.1f}" width="{n*cw:.1f}" height="{lh}" fill="{bg}"/>')
            # Each word is placed on its own cells: renderers disagree on
            # whitespace, but never on an x coordinate.
            cls = ' class="b"' if bold else ""
            cx = x
            for word in re.split(r"( +)", text):
                k = sum(cell_width(ch) for ch in word)
                if word.strip():
                    out.append(
                        f'<text x="{padx + cx * cw:.1f}" y="{cy + lh/2:.1f}" fill="{fg}"{cls} '
                        f'textLength="{k*cw:.1f}" lengthAdjust="spacingAndGlyphs">{html.escape(word)}</text>'
                    )
                cx += k
            x += n
    out.append("</svg>")
    sys.stdout.write("\n".join(out) + "\n")


if __name__ == "__main__":
    main()
