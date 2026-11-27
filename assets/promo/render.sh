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
for lang in en zh; do
  sfx=$([ "$lang" = zh ] && echo .zh-TW || echo "")
  shot "land-$lang" 1200 675 "x-1200x675$sfx.png"
  shot "port-$lang" 1080 1350 "threads-1080x1350$sfx.png"
  shot "og-$lang" 1280 640 "banner-1280x640$sfx.png"
done
