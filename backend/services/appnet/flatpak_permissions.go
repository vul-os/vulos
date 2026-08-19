package appnet

import (
	"context"
	"fmt"
	"log"
	"os/exec"
	"sort"
	"strings"
)

// The bridge between Vulos's `permissions` array and Flatpak's sandbox.
//
// WHAT WAS WRONG. A registry recipe carries `"permissions": ["filesystem"]`,
// the manifest writer copies it into app.json, and the App Hub shows it. For a
// Flatpak app it decided NOTHING: FlatpakInstall ran `flatpak install` and
// stopped, so the app kept exactly what its Flathub manifest asked for —
// commonly `--filesystem=host` AND `--share=network` — while its recipe said
// `["filesystem"]`. A declared permission model where the declaration has no
// effect is worse than no model, because the manifest reads as a sandbox to
// anyone reviewing it. That is the whole reason this file exists.
//
// WHAT THIS ENFORCES, and it is a subset, stated rather than implied:
//
//	network     undeclared -> --unshare=network
//	filesystem  undeclared -> --nofilesystem=host --nofilesystem=home
//	bluetooth   undeclared -> --disallow=bluetooth
//
// WHAT IT DOES NOT, with the reason each one is refused rather than faked:
//
//	camera, microphone
//	    Not separable in Flatpak's model. Audio capture and playback ride ONE
//	    socket (`pulseaudio`); revoking it to enforce "no microphone" would also
//	    silence every app that only plays sound. Camera access rides
//	    `--device=all`, which is the same grant as /dev/dri and /dev/input. An
//	    enforcement that broke playback and controllers to half-enforce a
//	    microphone rule would be traded for a worse defect than the one it fixes.
//	gpu
//	    Would require `--nodevice=all` to mean anything, because Flathub grants
//	    `devices=[all]` to most of the catalogue. That same negation removes
//	    `shm` — which X11 apps use for shared-memory image transport, i.e. every
//	    streamed desktop app here — and `input`, which is how RetroArch and the
//	    emulators see a controller. Measured on real manifests: telegram, signal,
//	    zapzap, retroarch and mpv all carry devices=[all].
//	usb, background, notifications, storage
//	    Not Flatpak concepts at all. They describe Vulos's own process and
//	    network model, and belong to the per-app namespace, not to bwrap.
//
// The list above is not prose: unenforcedFlatpakPermissions holds it, and a
// test fails if any permission in ValidPermissions is neither enforced here nor
// named there. A permission added later cannot quietly become a tenth inert
// string.
//
// THE BRIDGE ONLY EVER REMOVES. There is no path here that grants a permission
// the Flathub manifest did not request. Vulos narrowing a publisher's sandbox
// on the owner's behalf is a defensible thing to do; Vulos WIDENING it, because
// a registry entry listed a string, is not.

// enforcedFlatpakPermissions maps a Vulos permission to the override flags that
// revoke it when the recipe does NOT declare it.
var enforcedFlatpakPermissions = map[string][]string{
	"network":    {"--unshare=network"},
	"filesystem": {"--nofilesystem=host", "--nofilesystem=home"},
	"bluetooth":  {"--disallow=bluetooth"},
}

// unenforcedFlatpakPermissions names every valid permission this bridge cannot
// express, so the gap is enumerated rather than inferred from an absence.
var unenforcedFlatpakPermissions = map[string]string{
	"camera":        "rides --device=all together with /dev/dri and /dev/input; revoking it breaks GPU and controllers",
	"microphone":    "shares one pulseaudio socket with playback; revoking it silences apps that only play sound",
	"gpu":           "would need --nodevice=all, which also removes shm (X11 shared-memory transport) and input",
	"usb":           "not a Flatpak context key; belongs to the Vulos device model",
	"background":    "describes the Vulos launcher's process lifetime, not the bwrap sandbox",
	"notifications": "delivered through Vulos's own notification service, not a portal grant",
	"storage":       "issues per-user object-store credentials; nothing for bwrap to restrict",
}

// FlatpakOverrideFlags returns the `flatpak override` arguments that narrow an
// app's Flathub sandbox to what its recipe declares. Empty means the recipe
// declares everything this bridge can enforce, so there is nothing to remove.
//
// Order is deterministic (sorted by permission name) so the command a box runs
// is reproducible and a test can assert on it without sorting first.
func FlatpakOverrideFlags(permissions []string) []string {
	declared := make(map[string]bool, len(permissions))
	for _, p := range permissions {
		declared[strings.TrimSpace(strings.ToLower(p))] = true
	}
	names := make([]string, 0, len(enforcedFlatpakPermissions))
	for name := range enforcedFlatpakPermissions {
		names = append(names, name)
	}
	sort.Strings(names)

	var flags []string
	for _, name := range names {
		if !declared[name] {
			flags = append(flags, enforcedFlatpakPermissions[name]...)
		}
	}
	return flags
}

// FlatpakApplyOverrides narrows an installed Flatpak app to its recipe's
// declared permissions.
//
// It applies SYSTEM overrides because FlatpakInstall installs system-wide. A
// user override would be per-home and would leave every other account on the
// box running the unnarrowed app.
func FlatpakApplyOverrides(ctx context.Context, flatpakID string, permissions []string) error {
	flags := FlatpakOverrideFlags(permissions)
	if len(flags) == 0 {
		return nil
	}
	args := append([]string{"override", "--system"}, flags...)
	args = append(args, flatpakID)
	cmd := exec.CommandContext(ctx, "flatpak", args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("flatpak override %s %s: %w: %s",
			flatpakID, strings.Join(flags, " "), err, strings.TrimSpace(string(out)))
	}
	log.Printf("[flatpak] narrowed %s to declared permissions %v (%s)",
		flatpakID, permissions, strings.Join(flags, " "))
	return nil
}

// FlatpakResetOverrides drops the overrides on uninstall, so a later reinstall
// starts from the publisher's own sandbox rather than from a stale narrowing
// that outlived the recipe which asked for it.
func FlatpakResetOverrides(ctx context.Context, flatpakID string) {
	if out, err := exec.CommandContext(ctx, "flatpak", "override", "--system", "--reset", flatpakID).
		CombinedOutput(); err != nil {
		log.Printf("[flatpak] override reset for %s failed: %v: %s", flatpakID, err, strings.TrimSpace(string(out)))
	}
}
