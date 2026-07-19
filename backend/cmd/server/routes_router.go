package main

// routes_router.go — GET /api/router/classify?app=<id>
// Exposes the Open Router classifier over HTTP so the shell launcher and other
// clients can resolve the execution lane for any app ID without re-implementing
// the classification rules.

import (
	"encoding/json"
	"net/http"

	"vulos/backend/services/openrouter"
)

// registryLaneFlags maps app IDs from registry.json to their lane flags.
// This is the canonical tagging for existing registry entries (ROUTER-01 AC).
// Lane flags follow the rules in openrouter.Classify:
//
//   - web-native apps (office, mail, terminal, notes, browser + all type:"web"
//     registry apps): Web = true
//   - GPU-heavy creative apps: NeedsGPU = true
//   - Games and Windows compat layers: Game = true
//
// Apps with type:"web" already classify via AppType without needing explicit
// flags here, but we include the Web flag for belt-and-suspenders clarity.
// NOTE: no "meet" entry — first-party Vulos Meet is retired; video calling is
// third-party (Jitsi Meet / Element Call), installed from the App Store and
// classified through the ordinary registry.json type:"web"/"desktop" path
// below, not a built-in AppID.
var registryLaneFlags = map[string]openrouter.Intent{
	// ── Web-native built-in apps ────────────────────────────────────────────
	"office":    {AppID: "office", AppType: "web", Web: true},
	"mail":      {AppID: "mail", AppType: "web", Web: true},
	"terminal":  {AppID: "terminal", AppType: "web", Web: true},
	"notes":     {AppID: "notes", AppType: "web", Web: true},
	"library":   {AppID: "library", AppType: "web", Web: true},
	"browser":   {AppID: "browser", AppType: "web", Web: true}, // Smart Browser (client-side)
	"spaces":    {AppID: "spaces", AppType: "web", Web: true},
	"calendar":  {AppID: "calendar", AppType: "web", Web: true},
	"dashboard": {AppID: "dashboard", AppType: "web", Web: true},
	"messages":  {AppID: "messages", AppType: "web", Web: true},

	// ── Default + system built-ins (client-side React; render in-shell, zero stream) ──
	"calculator":    {AppID: "calculator", AppType: "web", Web: true},
	"clock":         {AppID: "clock", AppType: "web", Web: true},
	"weather":       {AppID: "weather", AppType: "web", Web: true},
	"gallery":       {AppID: "gallery", AppType: "web", Web: true},
	"social":        {AppID: "social", AppType: "web", Web: true},
	"sheets":        {AppID: "sheets", AppType: "web", Web: true},
	"text-editor":   {AppID: "text-editor", AppType: "web", Web: true},
	"pdf-viewer":    {AppID: "pdf-viewer", AppType: "web", Web: true},
	"files":         {AppID: "files", AppType: "web", Web: true},
	"activity":      {AppID: "activity", AppType: "web", Web: true},
	"apphub":        {AppID: "apphub", AppType: "web", Web: true},
	"authenticator": {AppID: "authenticator", AppType: "web", Web: true},
	"vault":         {AppID: "vault", AppType: "web", Web: true},
	"persona":       {AppID: "persona", AppType: "web", Web: true},
	"disks":         {AppID: "disks", AppType: "web", Web: true},
	"drivers":       {AppID: "drivers", AppType: "web", Web: true},
	"packages":      {AppID: "packages", AppType: "web", Web: true},

	// ── registry.json type:"web" apps (already classify via AppType) ─────────
	"navidrome":      {AppID: "navidrome", AppType: "web"},
	"gitea":          {AppID: "gitea", AppType: "web"},
	"grafana":        {AppID: "grafana", AppType: "web"},
	"jupyter":        {AppID: "jupyter", AppType: "web"},
	"cockpit":        {AppID: "cockpit", AppType: "web"},
	"drawio":         {AppID: "drawio", AppType: "web"},
	"excalidraw":     {AppID: "excalidraw", AppType: "web"},
	"memos":          {AppID: "memos", AppType: "web"},
	"syncthing":      {AppID: "syncthing", AppType: "web"},
	"transmission":   {AppID: "transmission", AppType: "web"},
	"uptime-kuma":    {AppID: "uptime-kuma", AppType: "web"},
	"cinny":          {AppID: "cinny", AppType: "web"},
	"hoppscotch":     {AppID: "hoppscotch", AppType: "web"},
	"vaultwarden":    {AppID: "vaultwarden", AppType: "web"},
	"libretranslate": {AppID: "libretranslate", AppType: "web"},
	"httpbin":        {AppID: "httpbin", AppType: "web"},
	"nginx":          {AppID: "nginx", AppType: "web"},
	"minio":          {AppID: "minio", AppType: "web"},
	"wede":           {AppID: "wede", AppType: "web"},

	// ── WEBAPP-01: curated web apps (lane.web) ───────────────────────────────
	// image editing
	"minipaint": {AppID: "minipaint", AppType: "web", Web: true},
	// IDE (browser-based VS Code)
	"code-server": {AppID: "code-server", AppType: "web", Web: true},
	// photo management
	"immich": {AppID: "immich", AppType: "web", Web: true},
	// diagramming / Visio replacement — also registered as "diagrams.net" alias
	"diagrams-net": {AppID: "diagrams-net", AppType: "web", Web: true},
	// media server (replaces VLC for playback)
	"jellyfin": {AppID: "jellyfin", AppType: "web", Web: true},
	// CAD — kerf is browser-native WASM CAD (WEBAPP-01; see roadmap/CAD-KERF.md)
	"kerf": {AppID: "kerf", AppType: "web", Web: true},
	// light audio editing
	"audiomass": {AppID: "audiomass", AppType: "web", Web: true},
	// light vector editing
	"svg-edit": {AppID: "svg-edit", AppType: "web", Web: true},

	// ── GPU-accelerated creative apps (needs_gpu) ────────────────────────────
	"blender":    {AppID: "blender", AppType: "desktop", NeedsGPU: true},
	"kdenlive":   {AppID: "kdenlive", AppType: "desktop", NeedsGPU: true},
	"darktable":  {AppID: "darktable", AppType: "desktop", NeedsGPU: true},
	"obs-studio": {AppID: "obs-studio", AppType: "desktop", NeedsGPU: true},
	"shotcut":    {AppID: "shotcut", AppType: "desktop", NeedsGPU: true},

	// ── Games and Windows compat layers (game) ───────────────────────────────
	"steam":  {AppID: "steam", AppType: "desktop", Game: true},
	"lutris": {AppID: "lutris", AppType: "desktop", Game: true},
	"wine":   {AppID: "wine", AppType: "desktop", Game: true},

	// ── Plain desktop apps → CPUStream (no special flags needed) ────────────
	// Listed here for documentation; they fall through to the CPUStream default.
	"gimp":        {AppID: "gimp", AppType: "desktop"},
	"inkscape":    {AppID: "inkscape", AppType: "desktop"},
	"vlc":         {AppID: "vlc", AppType: "desktop"},
	"audacity":    {AppID: "audacity", AppType: "desktop"},
	"libreoffice": {AppID: "libreoffice", AppType: "desktop"},
	"kicad":       {AppID: "kicad", AppType: "desktop"},
	"geany":       {AppID: "geany", AppType: "desktop"},
	"gnucash":     {AppID: "gnucash", AppType: "desktop"},
	"qbittorrent": {AppID: "qbittorrent", AppType: "desktop"},
	"keepassxc":   {AppID: "keepassxc", AppType: "desktop"},
	"filezilla":   {AppID: "filezilla", AppType: "desktop"},
	"firefox":     {AppID: "firefox", AppType: "desktop"},
	"octave":      {AppID: "octave", AppType: "desktop"},
	"qgis":        {AppID: "qgis", AppType: "desktop"},
	"lmms":        {AppID: "lmms", AppType: "desktop"},
	"ardour":      {AppID: "ardour", AppType: "desktop"},
}

// classifyAppID resolves the lane for a given app ID using the registry flag
// table. Unknown IDs fall back to CPUStream (safe conservative default).
func classifyAppID(appID string) openrouter.Lane {
	if intent, ok := registryLaneFlags[appID]; ok {
		return openrouter.Classify(intent)
	}
	// Unknown app — conservative default is CPUStream (streamed window).
	return openrouter.LaneCPUStream
}

// registerRouterRoutes wires GET /api/router/classify onto mux.
func registerRouterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/router/classify", func(w http.ResponseWriter, r *http.Request) {
		appID := r.URL.Query().Get("app")
		if appID == "" {
			writeErr(w, 400, "app query parameter required")
			return
		}

		// Validate app ID — alphanumeric, hyphen, underscore only.
		for _, c := range appID {
			if !((c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-' || c == '_') {
				writeErr(w, 400, "invalid app id")
				return
			}
		}

		lane := classifyAppID(appID)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"lane": string(lane)})
	})
}
