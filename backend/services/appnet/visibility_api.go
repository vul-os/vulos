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

// RegisterVisibilityHandlers registers the app-visibility API endpoints onto mux.
//
//	GET  /api/apps/visibility         — list all apps with their visibility (default private)
//	POST /api/apps/{id}/visibility    — set visibility for one app
//
// Both routes share the same auth as all other /api/* endpoints (enforced by
// the global auth middleware in main.go).
func RegisterVisibilityHandlers(mux *http.ServeMux, store *AppStore, vis *VisibilityStore) {
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

	// POST /api/apps/{id}/visibility — validate and persist visibility for one app.
	mux.HandleFunc("POST /api/apps/{id}/visibility", func(w http.ResponseWriter, r *http.Request) {
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

		if err := vis.Set(appID, req.Visibility); err != nil {
			visWriteErr(w, http.StatusInternalServerError, "failed to persist visibility: "+err.Error())
			return
		}

		visWriteJSON(w, visAppVisibility{
			AppID:      appID,
			Visibility: req.Visibility,
		})
	})
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
