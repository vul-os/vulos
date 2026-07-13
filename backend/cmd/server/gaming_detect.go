package main

import (
	"path/filepath"

	"vulos/backend/services/appnet"
	"vulos/backend/services/wine"
)

// detectGamingLaunch reports whether a POST /api/stream/launch-app request should
// engage the low-latency gaming-mode encoder profile (GAME-07). It is true when:
//
//   - the command is a known gaming launcher — wine/wine64/lutris/steam/
//     steam-runtime (via wine.IsGamingCommand, basename of the first token); OR
//   - the app's manifest (appsDir/<appID>/app.json) declares category=="gaming".
//
// It is false for plain desktop apps and GPU-accelerated non-games (e.g.
// blender/kdenlive), so gaming input behaviour and the zero-latency profile do
// NOT engage for ordinary streamed desktop windows.
//
// Extracted from the launch-app handler so the auto-detect decision is
// unit-testable without spawning host processes.
func detectGamingLaunch(command, appID, appsDir string) bool {
	if wine.IsGamingCommand(command) {
		return true
	}
	if appID != "" {
		manifestPath := filepath.Join(appsDir, appID, "app.json")
		if m, err := appnet.LoadManifest(manifestPath); err == nil {
			return m.Category == "gaming"
		}
	}
	return false
}
