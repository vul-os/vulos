#!/bin/sh
# vulos-kiosk-genconfig.sh — write the labwc config for a multi-output kiosk.
#
#   vulos-kiosk-genconfig.sh <outdir> <base-url> <output-name>...
#
# Writes <outdir>/rc.xml and <outdir>/session.sh. One windowRule per output,
# one browser per output.
#
# This is a REAL FILE rather than a heredoc inside build.sh, and that is the
# whole point of it existing separately. build.sh installs it into the image and
# vulos-kiosk calls it; a test runs the same file directly with fake outputs and
# checks what it produces. One source, so there is nothing to extract and
# nothing to drift.
#
# How the placement works, since it is not obvious and every part of it fails
# silently if wrong:
#
#   labwc cannot be told "put browser 2 on screen 2". It places windows by RULE,
#   and a rule can only name a window it can tell apart. Every instance of the
#   Vulos shell is otherwise identical — same origin, same app. So each browser
#   is given a URL naming its screen, the shell puts that name in its window
#   title (screenWindowTitle in frontend/src/providers/screenIdentity.ts), and
#   the rule below matches that title and moves the window to that output.
#
#   The connector name is therefore the same string in four places: read from
#   /sys/class/drm by vulos-kiosk, passed here, written into both the rule's
#   title and MoveToOutput's output attribute, and echoed back by the shell.
#
# Syntax is from labwc-config(5) and labwc-actions(5): windowRule matches on
# title/identifier/type and carries actions; MoveToOutput takes output="<name>".
set -eu

if [ "$#" -lt 3 ]; then
    echo "usage: $0 <outdir> <base-url> <output-name>..." >&2
    exit 2
fi

outdir=$1
base_url=$2
shift 2

count=$#
if [ "$count" -lt 2 ]; then
    # One screen needs no placement rules at all, and writing them would be a
    # claim this script cannot honour. Callers gate on this too; refusing here
    # as well means the file cannot produce a misleading config on its own.
    echo "$0: refusing to write a multi-output config for $count output(s)" >&2
    exit 3
fi

mkdir -p "$outdir"

{
    echo '<?xml version="1.0"?>'
    echo '<labwc_config>'
    echo '  <windowRules>'
    for nm in "$@"; do
        echo "    <windowRule title=\"Vulos — $nm\" matchOnce=\"yes\">"
        echo "      <action name=\"MoveToOutput\" output=\"$nm\" />"
        echo '    </windowRule>'
    done
    echo '  </windowRules>'
    echo '</labwc_config>'
} > "$outdir/rc.xml"

{
    echo '#!/bin/sh'
    i=0
    for nm in "$@"; do
        i=$((i + 1))
        echo "cog \"$base_url?screen=$nm&screens=$count&screenIndex=$i\" &"
    done
    echo 'wait'
} > "$outdir/session.sh"
chmod +x "$outdir/session.sh"
