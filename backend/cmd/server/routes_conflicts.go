package main

import (
	"encoding/json"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"vulos/backend/services/notify"
)

// cl10ConflictEntry describes a single conflict copy file.
type cl10ConflictEntry struct {
	Path      string `json:"path"`      // relative path from dataDir
	BasePath  string `json:"base_path"` // relative path of the original file (without .conflict-* suffix)
	Node      string `json:"node"`      // node ID parsed from filename
	Timestamp string `json:"timestamp"` // timestamp parsed from filename
	Size      int64  `json:"size"`
}

// cl10ConflictPattern matches the conflict-copy filenames written by
// services/sync (conflictCopyPath):
//
//	<stem>.conflict-<nodeID>-<YYYYMMDD-HHMMSS><ext>
//	e.g. notes.conflict-office-20260516-143022.txt
//	     data.conflict-home-server-20260516-143022.bin   (node ID may contain '-')
//	     noext.conflict-node1-20260516-143022            (extension optional)
//
// Capture groups: 1=stem, 2=nodeID, 3=timestamp, 4=ext (with leading dot, or "").
// The fixed-width "\d{8}-\d{6}" timestamp is the reliable anchor that lets the
// node ID itself contain dashes. The base (canonical) file is stem+ext.
var cl10ConflictPattern = regexp.MustCompile(`^(.+)\.conflict-(.+)-(\d{8}-\d{6})(\.[^.]+)?$`)

// registerConflictRoutes wires GET /api/sync/conflicts and POST /api/sync/conflicts/resolve.
func registerConflictRoutes(mux *http.ServeMux, dataDir string, notifySvc *notify.Service) {
	mux.HandleFunc("GET /api/sync/conflicts", cl10ListConflicts(dataDir))
	mux.HandleFunc("POST /api/sync/conflicts/resolve", cl10ResolveConflict(dataDir, notifySvc))
}

// cl10ListConflicts walks dataDir and returns all *.conflict-* files.
func cl10ListConflicts(dataDir string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var entries []cl10ConflictEntry

		walkErr := filepath.WalkDir(dataDir, func(absPath string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			name := d.Name()
			m := cl10ConflictPattern.FindStringSubmatch(name)
			if m == nil {
				return nil
			}
			rel, err := filepath.Rel(dataDir, absPath)
			if err != nil {
				return nil
			}
			info, err := d.Info()
			var size int64
			if err == nil {
				size = info.Size()
			}
			// Reconstruct the canonical (base) filename: stem + ext. The
			// conflict-copy filename interleaves the .conflict-<node>-<ts> marker
			// BEFORE the extension (notes.conflict-office-20260516-143022.txt), so
			// the base file is stem(m[1]) + ext(m[4]).
			baseName := m[1] + m[4]
			node := m[2]
			ts := m[3]
			baseRel := filepath.Join(filepath.Dir(rel), baseName)

			entries = append(entries, cl10ConflictEntry{
				Path:      rel,
				BasePath:  baseRel,
				Node:      node,
				Timestamp: ts,
				Size:      size,
			})
			return nil
		})
		if walkErr != nil {
			writeErr(w, 500, "walk error: "+walkErr.Error())
			return
		}
		if entries == nil {
			entries = []cl10ConflictEntry{}
		}
		writeJSON(w, entries)
	}
}

// cl10ResolveConflict handles POST /api/sync/conflicts/resolve.
// Body: { "path": "<rel path of conflict file>", "keep": "ours"|"theirs" }
//
//   - keep=="ours": delete the conflict copy, keep the existing base file.
//   - keep=="theirs": replace the base file with the conflict copy (atomic rename),
//     then delete the old conflict path.
func cl10ResolveConflict(dataDir string, notifySvc *notify.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Path string `json:"path"`
			Keep string `json:"keep"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
			writeErr(w, 400, "invalid JSON body")
			return
		}

		// Validate keep value
		if req.Keep != "ours" && req.Keep != "theirs" {
			writeErr(w, 400, `keep must be "ours" or "theirs"`)
			return
		}

		// Validate path: must not be empty and must not escape dataDir
		if req.Path == "" || strings.Contains(req.Path, "..") {
			writeErr(w, 400, "invalid path")
			return
		}
		// Strip any leading slash just in case
		cleanRel := filepath.Clean(strings.TrimPrefix(req.Path, "/"))

		// Realpath containment check
		conflictAbs := filepath.Join(dataDir, cleanRel)
		resolvedConflict, err := filepath.EvalSymlinks(filepath.Dir(conflictAbs))
		if err != nil {
			// Directory may not exist
			writeErr(w, 400, "path not found")
			return
		}
		resolvedDataDir, err := filepath.EvalSymlinks(dataDir)
		if err != nil {
			resolvedDataDir = dataDir
		}
		if !strings.HasPrefix(resolvedConflict+string(filepath.Separator), resolvedDataDir+string(filepath.Separator)) &&
			resolvedConflict != resolvedDataDir {
			writeErr(w, 400, "path escapes data directory")
			return
		}

		// Confirm the file matches the conflict pattern
		name := filepath.Base(conflictAbs)
		m := cl10ConflictPattern.FindStringSubmatch(name)
		if m == nil {
			writeErr(w, 400, "path is not a conflict file")
			return
		}

		// Stat the conflict file to ensure it exists
		if _, err := os.Stat(conflictAbs); err != nil {
			writeErr(w, 404, "conflict file not found")
			return
		}

		baseName := m[1] + m[4] // stem + ext → canonical base filename
		baseAbs := filepath.Join(filepath.Dir(conflictAbs), baseName)

		switch req.Keep {
		case "ours":
			// Just delete the conflict copy
			if err := os.Remove(conflictAbs); err != nil {
				writeErr(w, 500, "failed to remove conflict file: "+err.Error())
				return
			}
		case "theirs":
			// Atomic rename: conflict file → base file (overwrites ours)
			if err := os.Rename(conflictAbs, baseAbs); err != nil {
				writeErr(w, 500, "failed to rename conflict file: "+err.Error())
				return
			}
		}

		// Emit a resolved notification
		if notifySvc != nil {
			notifySvc.Send(
				"Sync conflict resolved",
				"Kept "+req.Keep+": "+cleanRel,
				notify.LevelInfo,
				"sync",
			)
		}

		writeJSON(w, map[string]string{"status": "resolved", "kept": req.Keep, "path": cleanRel})
	}
}
