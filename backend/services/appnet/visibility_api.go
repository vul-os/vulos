package appnet

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
)

// visAppVisibility is the per-app entry returned by GET /api/apps/visibility.
type visAppVisibility struct {
	AppID      string `json:"app_id"`
	Visibility string `json:"visibility"`
}

// visSetRequest is the body expected by POST /api/apps/{id}/visibility.
type visSetRequest struct {
	Visibility string `json:"visibility"`
}

// visWriteJSON writes v as JSON with status 200.
func visWriteJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

// visWriteErr writes a JSON error with the given HTTP status.
func visWriteErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// PublishAuthorizer decides whether the current request may set appID to a
// NON-private visibility (i.e. publish it to the local network or the public
// web). It is called only for local/public writes; reverting to private is
// always allowed for any authenticated caller. Returning false makes the
// handler answer 403. It is the right place to also audit-log publish events.
//
// nil authorizer ⇒ publishing is open to any authenticated caller (self-host /
// standalone posture — the same "gating off by default" convention the
// entitlement layer uses). main.go always installs a real authorizer that
// requires RoleAdmin OR app ownership (Finding 2, HIGH).
type PublishAuthorizer func(r *http.Request, appID, visibility string) bool

// RegisterVisibilityHandlers registers the app-visibility API endpoints onto mux.
//
//	GET   /api/apps/visibility         — list all apps with their visibility (default private)
//	POST  /api/apps/{id}/visibility    — set visibility for one app
//	PATCH /api/apps/{id}/visibility    — same as POST; preferred for partial updates
//
// System apps (settings, files, terminal) reject any non-private visibility.
// Both write routes share the same auth as all other /api/* endpoints
// (enforced by the global auth middleware in main.go). A non-private write is
// additionally gated by authorizePublish (Finding 2): before this, ANY
// authenticated user could publish ANY non-system app to the public internet.
func RegisterVisibilityHandlers(mux *http.ServeMux, store *AppStore, vis *VisibilityStore, authorizePublish PublishAuthorizer) {
	// GET /api/apps/visibility — returns every known app id + current visibility.
	// Apps with no explicit setting are listed as "private" (the default).
	mux.HandleFunc("GET /api/apps/visibility", func(w http.ResponseWriter, r *http.Request) {
		// Collect all known app IDs from installed apps.
		manifests, _ := ScanApps(store.appsDir)

		// Also collect AI apps from the ai-apps dir, which live next to appsDir.
		aiAppsDir := store.aiAppsDir()

		seen := make(map[string]struct{})
		var entries []visAppVisibility

		for _, m := range manifests {
			seen[m.ID] = struct{}{}
			entries = append(entries, visAppVisibility{
				AppID:      m.ID,
				Visibility: vis.Get(m.ID),
			})
		}

		// Include AI-generated apps that may not have a full manifest.
		if dirs, err := visReadDirNames(aiAppsDir); err == nil {
			for _, id := range dirs {
				if _, ok := seen[id]; ok {
					continue
				}
				seen[id] = struct{}{}
				entries = append(entries, visAppVisibility{
					AppID:      id,
					Visibility: vis.Get(id),
				})
			}
		}

		// Include any apps that have an explicit visibility setting but are no
		// longer present on disk (e.g., uninstalled but not yet cleaned up).
		for id, v := range vis.All() {
			if _, ok := seen[id]; ok {
				continue
			}
			entries = append(entries, visAppVisibility{
				AppID:      id,
				Visibility: v,
			})
		}

		if entries == nil {
			entries = []visAppVisibility{}
		}
		visWriteJSON(w, entries)
	})

	// setVisibilityHandler is shared by POST and PATCH.
	setVisibilityHandler := func(w http.ResponseWriter, r *http.Request) {
		appID := r.PathValue("id")
		if appID == "" {
			visWriteErr(w, http.StatusBadRequest, "app id required")
			return
		}

		var req visSetRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			visWriteErr(w, http.StatusBadRequest, "invalid JSON body")
			return
		}

		if err := ValidateVisibility(req.Visibility); err != nil {
			visWriteErr(w, http.StatusBadRequest, err.Error())
			return
		}

		// System apps (settings, files, terminal) may never be published.
		if IsSystemApp(appID) && req.Visibility != VisibilityPrivate {
			visWriteErr(w, http.StatusForbidden, "system apps cannot be published")
			return
		}

		// Finding 2 (HIGH): publishing an app (any non-private visibility) exposes
		// it beyond this device — to the LAN ("local") or the public internet
		// ("public"). That must be gated on admin/ownership, not merely on being
		// authenticated. Reverting to "private" is always allowed.
		if req.Visibility != VisibilityPrivate && authorizePublish != nil {
			if !authorizePublish(r, appID, req.Visibility) {
				visWriteErr(w, http.StatusForbidden, "only an admin or the app owner may publish an app")
				return
			}
		}

		if err := vis.Set(appID, req.Visibility); err != nil {
			visWriteErr(w, http.StatusInternalServerError, "failed to persist visibility: "+err.Error())
			return
		}

		visWriteJSON(w, visAppVisibility{
			AppID:      appID,
			Visibility: req.Visibility,
		})
	}

	// POST /api/apps/{id}/visibility — set visibility for one app.
	mux.HandleFunc("POST /api/apps/{id}/visibility", setVisibilityHandler)

	// PATCH /api/apps/{id}/visibility — preferred partial-update form (PUBWEB-01).
	mux.HandleFunc("PATCH /api/apps/{id}/visibility", setVisibilityHandler)
}

// aiAppsDir returns the AI-generated apps directory, which lives alongside appsDir.
// e.g. ~/.vulos/apps → ~/.vulos/ai-apps
func (s *AppStore) aiAppsDir() string {
	return filepath.Join(filepath.Dir(s.appsDir), "ai-apps")
}

// visReadDirNames returns the names of all subdirectories in dir.
func visReadDirNames(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	return names, nil
}
