#!/bin/sh
# generate-placeholders.sh — regenerate PNG assets for the vulos Plymouth theme.
#
# Assets produced:
#   background.png  — 1920x1080, solid #111111 (sRGB TrueColor)
#   progress.png    — 1x6 purple #C96AFF sprite (used as reference; bar is
#                     drawn programmatically in vulos.script)
#   logo.png        — copied from ../../../../public/icon-128.png if present,
#                     otherwise a 128x128 fallback purple square
#
# Requires: ImageMagick 7 (magick) or IM6 (convert); or python3 + Pillow.
#
# Usage: sh generate-placeholders.sh
#
set -e
DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$DIR/../../../.." && pwd)"
ICON="$REPO_ROOT/public/icon-128.png"

if command -v magick >/dev/null 2>&1; then
    IM="magick"
elif command -v convert >/dev/null 2>&1; then
    IM="convert"
else
    IM=""
fi

if [ -n "$IM" ]; then
    # background.png — 1920x1080 #111111, 8-bit RGB
    $IM -size 1920x1080 xc:"#111111" -depth 8 \
        -define png:color-type=2 "$DIR/background.png"
    echo "[ok] background.png (1920x1080 #111111 sRGB)"

    # progress.png — 1x6 #C96AFF, 8-bit RGB (tiny reference sprite)
    $IM -size 1x6 xc:"#C96AFF" -depth 8 \
        -define png:color-type=2 "$DIR/progress.png"
    echo "[ok] progress.png (1x6 #C96AFF sRGB)"

    # logo.png — prefer real icon, else generate a placeholder
    if [ -f "$ICON" ]; then
        cp "$ICON" "$DIR/logo.png"
        echo "[ok] logo.png (copied from public/icon-128.png, 128x128)"
    else
        $IM -size 128x128 xc:"#C96AFF" -depth 8 \
            -define png:color-type=2 "$DIR/logo.png"
        echo "[ok] logo.png (128x128 purple placeholder — real asset missing)"
    fi

elif python3 -c "import PIL" 2>/dev/null; then
    python3 - <<'EOF'
import os, shutil
from PIL import Image

d    = os.path.dirname(os.path.abspath(__file__))
root = os.path.normpath(os.path.join(d, "../../../.."))
icon = os.path.join(root, "public", "icon-128.png")

# background.png
bg = Image.new("RGB", (1920, 1080), (17, 17, 17))
bg.save(os.path.join(d, "background.png"))
print("[ok] background.png (1920x1080 #111111)")

# progress.png
pr = Image.new("RGB", (1, 6), (201, 106, 255))
pr.save(os.path.join(d, "progress.png"))
print("[ok] progress.png (1x6 #C96AFF)")

# logo.png
if os.path.exists(icon):
    shutil.copy(icon, os.path.join(d, "logo.png"))
    print("[ok] logo.png (copied from public/icon-128.png)")
else:
    lo = Image.new("RGBA", (128, 128), (201, 106, 255, 255))
    lo.save(os.path.join(d, "logo.png"))
    print("[ok] logo.png (128x128 purple placeholder — real asset missing)")
EOF

else
    echo "ERROR: Neither ImageMagick nor Pillow found."
    echo "Install one, then re-run this script."
    echo "Or supply manually:"
    echo "  logo.png       — 128x128 RGBA PNG"
    echo "  background.png — 1920x1080 RGB PNG, colour #111111"
    echo "  progress.png   — 1x6 RGB PNG, colour #C96AFF"
    exit 1
fi
