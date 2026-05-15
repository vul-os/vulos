#!/bin/sh
# generate-placeholders.sh — create placeholder PNGs for the vulos Plymouth theme.
# Run once during development; replace with real branded assets before shipping.
# Requires: imagemagick (convert) or python3 with Pillow.
#
# Usage: sh generate-placeholders.sh
#   Output: logo.png, progress.png, background.png in the same directory.

set -e
DIR="$(cd "$(dirname "$0")" && pwd)"

if command -v convert >/dev/null 2>&1; then
    # ImageMagick
    convert -size 1920x1080 xc:"#111111" "$DIR/background.png"
    convert -size 200x80    xc:"#C96AFF" -gravity Center \
        -font DejaVu-Sans-Bold -pointsize 28 -fill white \
        -annotate 0 'VULA OS' "$DIR/logo.png"
    convert -size 400x6     xc:"#C96AFF" "$DIR/progress.png"
    echo "Placeholders generated with ImageMagick."
elif python3 -c "import PIL" 2>/dev/null; then
    python3 - << 'EOF'
from PIL import Image, ImageDraw, ImageFont
import os
d = os.path.dirname(os.path.abspath(__file__))

# background.png — 1920x1080 dark
bg = Image.new("RGB", (1920, 1080), (17, 17, 17))
bg.save(os.path.join(d, "background.png"))

# logo.png — 200x80 purple rect (replace with real logo SVG export)
logo = Image.new("RGBA", (200, 80), (204, 107, 255, 255))
draw = ImageDraw.Draw(logo)
draw.text((20, 25), "VULA OS", fill=(255, 255, 255))
logo.save(os.path.join(d, "logo.png"))

# progress.png — 1x1 purple pixel (Plymouth scales sprites)
prog = Image.new("RGBA", (1, 1), (204, 107, 255, 255))
prog.save(os.path.join(d, "progress.png"))
print("Placeholders generated with Pillow.")
EOF
else
    echo "Neither ImageMagick nor Pillow found."
    echo "Install one, then re-run this script."
    echo "Or supply logo.png (200x80), progress.png (400x6), background.png (1920x1080) manually."
    exit 1
fi
