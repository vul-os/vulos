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
# ── How the placement works, and why it is NOT the title ─────────────────────
#
#   labwc cannot be told "put browser 2 on screen 2". It places windows by RULE,
#   and a rule can only name a window it can tell apart. Every instance of the
#   Vulos shell is otherwise identical — same origin, same app.
#
#   This matched on the window TITLE until 2026-08-14, and that could never have
#   worked, for TWO independent reasons — each sufficient on its own. Both were
#   read out of the source of the exact versions in the image.
#
#   1. labwc 0.8.3 evaluates rules ONCE, at first map.
#
#        enum window_rule_event { LAB_WINDOW_RULE_EVENT_ON_FIRST_MAP = 0 };
#
#      One member; and window_rules_apply() is called from exactly one place,
#      src/view-impl-common.c inside view_impl_map(), guarded by
#      `if (!view->been_mapped)`. A later title change re-runs nothing.
#
#   2. cog 0.18.4 NEVER puts the document title on the Wayland toplevel at all.
#      platform/wayland/cog-platform-wl.c contains exactly one
#      xdg_toplevel_set_title() call in the whole file, at window creation:
#
#        xdg_toplevel_set_title (win_data.xdg_toplevel, COG_DEFAULT_APPNAME);
#
#      and COG_DEFAULT_APPNAME is the literal "Cog" (core/cog-config.h.in).
#      Nothing propagates the WebKitWebView title to the compositor on this
#      platform. So the title labwc sees for a cog window is "Cog", forever, on
#      every screen — not the shell's per-screen title, and not even
#      frontend/index.html's static one. No page-side change could have fixed
#      this; there was nothing on the page end of the wire.
#
#   Two two-display QEMU boots confirmed the consequence: both windows stacked
#   on output 1, head 1 one flat colour, nothing logged.
#
#   So the rule matches on `identifier`, which labwc compares against the
#   Wayland app_id (src/view.c: view_matches_query reads
#   view_get_string_prop(view, "app_id"); src/xdg.c returns
#   xdg_toplevel->app_id). cog sets app_id at TOPLEVEL CREATION —
#   xdg_toplevel_set_app_id() from g_application_get_application_id() — and the
#   very next statement is wl_surface_commit(), i.e. the app id rides the SAME
#   commit that maps the surface. It is unambiguously there at ON_FIRST_MAP:
#
#     xdg_toplevel_set_app_id (win_data.xdg_toplevel, app_id);
#     wl_surface_commit(win_data.wl_surface);
#
#   The flag is `--gapplication-app-id`, and it is NOT cog's own. cog builds its
#   GApplication with G_APPLICATION_CAN_OVERRIDE_APP_ID
#   (launcher/cog-launcher.c, cog_launcher_new), and that flag is what makes
#   GLib itself add `--gapplication-app-id=ID` to the command line — it appears
#   under "GApplication Options" in `cog --help-all`, not in plain `--help`.
#
#   cog has no `--application-id` option of its own. MEASURED against the
#   packaged binary (cog 0.18.4, WPE WebKit 2.48.3, debian trixie):
#
#       $ cog --application-id=org.vulos.kiosk.out-Virtual-2 about:blank
#       Unknown option --application-id=org.vulos.kiosk.out-Virtual-2
#
#   Without a working override every instance keeps COG_DEFAULT_APPID —
#   "com.igalia.Cog" for all of them — which is why matching on `identifier`
#   does nothing at all until the ids are made distinct.
#
#   The window title is still set by the shell (screenWindowTitle in
#   frontend/src/providers/screenIdentity.ts) and is still correct in the tab.
#   It is now COSMETIC and, per point 2 above, the compositor never sees it.
#
#   The rule must therefore carry `identifier` and NOTHING ELSE as criteria.
#   labwc ANDs its criteria — view_matches_query returns false as soon as any
#   one fails — so adding `title=` back alongside would reintroduce the exact
#   bug this replaced, because the only title a cog window ever has is "Cog".
#
# ── The connector name is the same string in FOUR places ─────────────────────
#
#   Read from /sys/class/drm by vulos-kiosk, passed here, and then: put in the
#   URL, turned into the app id that cog is launched with AND that the rule
#   matches, and given to MoveToOutput verbatim.
#
#   The app id is not the raw name — GLib will not accept most of them — so the
#   two sides that must agree (cog's --gapplication-app-id and the rule's
#   identifier) are computed by ONE call to kiosk_app_id() per output, stored in
#   one variable, and written into both files from it. A rule built from the raw
#   name while cog got a sanitised one is precisely the silent failure this
#   whole exercise has been about, and the single variable is what makes it
#   unrepresentable rather than merely unlikely.
#
# Syntax is from labwc-config(5) and labwc-actions(5): windowRule matches on
# title/identifier/type and carries actions; MoveToOutput takes output="<name>".
set -eu

# kiosk_app_id maps a DRM connector name to the GApplication id its browser
# runs under.
#
#   Virtual-2  ->  org.vulos.kiosk.out-Virtual-2
#   9PinDIN-1  ->  org.vulos.kiosk.out-9PinDIN-1
#
# Every part of that shape is there for a reason, and every rule it obeys was
# MEASURED against
# g_application_id_is_valid() from GLib 2.84.4 — the version in the image's own
# suite (build.sh: SUITE=trixie) — in a debian:trixie container.
#
# What an invalid id actually does was measured too, rather than assumed, and it
# is worth stating precisely because the obvious guess is wrong. cog does NOT
# refuse to start:
#
#   $ cog --gapplication-app-id=org.vulos.kiosk.9PinDIN-1 about:blank
#   GLib-GIO-CRITICAL: g_application_set_application_id: assertion
#     'application_id == NULL || g_application_id_is_valid (application_id)' failed
#
# That is a g_return_if_fail — the setter becomes a no-op and cog carries on
# under COG_DEFAULT_APPID. So an invalid id is not a black screen; it is every
# instance sharing "com.igalia.Cog", no rule matching anything, and all windows
# stacked on one output with one CRITICAL line in the journal. Which is to say:
# EXACTLY the bug this replaced, restored silently. The same command with
# `org.vulos.kiosk.out-9PinDIN-1` produced no such line.
#
#   * At least two period-separated elements. `nodot` is INVALID.
#   * No element may start with a digit, and `9PinDIN` is a real connector type
#     in the kernel's table (drm_connector.c), alongside Unknown, VGA, DVI-I,
#     DVI-D, DVI-A, Composite, SVIDEO, LVDS, Component, DP, HDMI-A, HDMI-B, TV,
#     eDP, Virtual, DSI, DPI, Writeback, SPI and USB. `org.vulos.kiosk.9PinDIN-1`
#     is INVALID; the constant `out-` prefix makes it VALID, and being constant
#     it cannot introduce a collision.
#   * Hyphens ARE accepted (measured: `org.vulos.kiosk.DP-2-1` is VALID), so
#     connector names need no rewriting in practice and the id stays readable in
#     a journal — which matters, because the journal is the only window into a
#     box that is showing you nothing.
#   * Anything outside [A-Za-z0-9] is folded to `-` anyway, for input that is
#     not from that table. This is belt and braces with a useful side effect:
#     `*`, `?`, `[` and `/` are all INVALID app ids (measured), so a valid id can
#     never contain an fnmatch metacharacter — and labwc matches `identifier`
#     with fnmatch(pattern, app_id, FNM_CASEFOLD) (src/common/match.c). A
#     pattern with no metacharacters is a literal, so the glob degenerates to a
#     case-insensitive exact match, which is what we want and not what we would
#     get if a connector name could smuggle a `*` into the rule.
#
# VULOS_KIOSK_FORCE_APP_ID collapses EVERY output onto one app id, and exists
# for exactly one caller: the control arm of scripts/smoke-multiscreen-qemu.sh.
# A real boot sets it no more than it sets VULOS_DRM_ROOT, VULOS_DRI_ROOT or
# VULOS_MEMINFO, and this file reads it in precisely one place.
#
# Why a seam rather than a test that reads the generated files: the claim the
# QEMU harness makes is that the app id is what puts each browser on its own
# monitor. That claim is only worth anything if a run in which the app ids are
# NOT distinct can be shown to fail — on the same image, through the same
# launcher, with two connectors, two browsers and labwc all still real. There
# is no way to reach that state from outside the box, because both halves of
# the pair are derived in here from the connector name.
#
# It collapses rather than desynchronises, and the difference matters. Both
# halves — cog's --gapplication-app-id and the rule's identifier — still come
# from this one function, so they still cannot disagree; they simply stop being
# distinct from each other. That is faithfully the state this whole mechanism
# replaced, where every instance ran under COG_DEFAULT_APPID "com.igalia.Cog".
# A seam that let the two halves say different things would be a loaded gun in
# shipping code and is deliberately not what this is.
kiosk_app_id() {
    if [ -n "${VULOS_KIOSK_FORCE_APP_ID:-}" ]; then
        printf '%s\n' "$VULOS_KIOSK_FORCE_APP_ID"
        return
    fi
    printf 'org.vulos.kiosk.out-%s\n' \
        "$(printf '%s' "$1" | sed 's/[^A-Za-z0-9]/-/g')"
}

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

# REFUSE two outputs that would share an app id, rather than writing a config
# that puts two browsers on one screen and logs nothing about it.
#
# kiosk_app_id is injective over the names the kernel actually produces: those
# are drawn from [A-Za-z0-9-], the fold is the identity on them, and no two
# entries in the connector-type table are equal ignoring case. It is NOT
# injective over arbitrary input — `DP.1` and `DP-1` both fold to `out-DP-1` —
# and the comparison below is case-INSENSITIVE because labwc's is (FNM_CASEFOLD),
# so `eDP-1` and `EDP-1` would match each other's rule.
#
# That makes this a cheap conversion of a theoretical hazard into an observable
# failure. A refusal here is loud; the alternative is a dark monitor.
#
# Skipped, and ONLY skipped, when VULOS_KIOSK_FORCE_APP_ID is set. The check
# guards against a collision arriving by accident out of two connector names;
# under the force variable the collision IS the instruction, and refusing would
# mean the control arm could never produce the failure it exists to produce —
# the generator would exit 4, vulos-kiosk would exit 1, labwc would never
# start, and the run would go red for want of a compositor rather than for want
# of placement. A control that fails for the wrong reason is not a control.
#
# Written as an `if` wrapping the loop rather than a `[ … ] && break` inside it,
# deliberately: under `set -e` an AND-OR list whose test fails is a well-known
# way to end a script on the line that was supposed to do nothing, and this
# file runs with `set -eu` on a box whose only symptom would be a dark monitor.
if [ -z "${VULOS_KIOSK_FORCE_APP_ID:-}" ]; then
    seen=""
    for nm in "$@"; do
        key=$(kiosk_app_id "$nm" | tr 'A-Z' 'a-z')
        case " $seen " in
            *" $key "*)
                echo "$0: outputs collide on app id '$key'; refusing to write a config" \
                     "that would place two browsers on one screen" >&2
                exit 4
                ;;
        esac
        seen="$seen $key"
    done
fi

mkdir -p "$outdir"

rcxml="$outdir/rc.xml"
session="$outdir/session.sh"

{
    echo '<?xml version="1.0"?>'
    echo '<labwc_config>'
    echo '  <windowRules>'
} > "$rcxml"
echo '#!/bin/sh' > "$session"

# ONE loop, ONE app_id variable per output, written into BOTH files. The two
# halves cannot disagree because there is only one value.
i=0
for nm in "$@"; do
    i=$((i + 1))
    app_id=$(kiosk_app_id "$nm")

    # No XML escaping needed and none pretended: kiosk_app_id emits only
    # [A-Za-z0-9.-]. The output attribute carries the raw connector name, which
    # comes from the same kernel table.
    {
        echo "    <windowRule identifier=\"$app_id\" matchOnce=\"yes\">"
        echo "      <action name=\"MoveToOutput\" output=\"$nm\" />"
        echo '    </windowRule>'
    } >> "$rcxml"

    echo "cog --gapplication-app-id=\"$app_id\" \"$base_url?screen=$nm&screens=$count&screenIndex=$i\" &" \
        >> "$session"
done

{
    echo '  </windowRules>'
    echo '</labwc_config>'
} >> "$rcxml"
echo 'wait' >> "$session"
chmod +x "$session"
