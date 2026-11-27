#!/bin/sh
# Renders promo.html to PNGs with headless Chrome (macOS path by default; override CHROME=...).
set -eu
cd "$(dirname "$0")"
CHROME=${CHROME:-"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"}
shot() { # layout width height out
  "$CHROME" --headless=new --disable-gpu --hide-scrollbars --force-device-scale-factor=2 \
    --window-size="$2,$3" --virtual-time-budget=3000 --screenshot="$PWD/$4" "file://$PWD/promo.html#$1" >/dev/null 2>&1
  echo "wrote $4"
}
shot land 1200 675 x-1200x675.png
shot port 1080 1350 threads-1080x1350.png
shot og 1280 640 banner-1280x640.png
