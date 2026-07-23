package location

import (
	"encoding/json"
	"net/http"
)

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// postBody is the POST /api/location request shape. Lat/Lng are required;
// the rest mirror the browser Geolocation API's optional Coordinates fields.
type postBody struct {
	Lat      float64 `json:"lat"`
	Lng      float64 `json:"lng"`
	Accuracy float64 `json:"accuracy"`
	Altitude float64 `json:"altitude"`
	Heading  float64 `json:"heading"`
	Speed    float64 `json:"speed"`
}

// getResponse is the GET /api/location response shape.
type getResponse struct {
	Found bool `json:"found"`
	// No omitempty on Lat/Lng: 0 is a real coordinate (equator / prime
	// meridian), so it must survive serialization when Found is true.
	Lat        float64 `json:"lat"`
	Lng        float64 `json:"lng"`
	Accuracy   float64 `json:"accuracy,omitempty"`
	Altitude   float64 `json:"altitude,omitempty"`
	Heading    float64 `json:"heading,omitempty"`
	Speed      float64 `json:"speed,omitempty"`
	TS         int64   `json:"ts,omitempty"`
	AgeSeconds int64   `json:"age_seconds,omitempty"`
	Stale      bool    `json:"stale"`
	Source     string  `json:"source,omitempty"`
}

// RegisterHandlers wires POST/GET /api/location onto mux. Both routes are
// scoped to X-User-ID (stamped by the auth middleware — see
// services/auth/handlers.go, which strips any client-supplied copy before it
// reaches app handlers) and reject an unauthenticated caller (empty
// X-User-ID) with 401, fail-closed.
func RegisterHandlers(mux *http.ServeMux, s *Service) {
	mux.HandleFunc("POST /api/location", func(w http.ResponseWriter, r *http.Request) {
		userID := r.Header.Get("X-User-ID")
		if userID == "" {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthenticated"})
			return
		}
		var body postBody
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&body); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid body"})
			return
		}
		p := Position{
			Lat:      body.Lat,
			Lng:      body.Lng,
			Accuracy: body.Accuracy,
			Altitude: body.Altitude,
			Heading:  body.Heading,
			Speed:    body.Speed,
			Source:   "client",
		}
		if err := s.SetPosition(userID, p); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	})

	mux.HandleFunc("GET /api/location", func(w http.ResponseWriter, r *http.Request) {
		userID := r.Header.Get("X-User-ID")
		if userID == "" {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthenticated"})
			return
		}
		reading := s.GetPosition(userID)
		if !reading.Found {
			writeJSON(w, http.StatusOK, getResponse{Found: false})
			return
		}
		writeJSON(w, http.StatusOK, getResponse{
			Found:      true,
			Lat:        reading.Lat,
			Lng:        reading.Lng,
			Accuracy:   reading.Accuracy,
			Altitude:   reading.Altitude,
			Heading:    reading.Heading,
			Speed:      reading.Speed,
			TS:         reading.TS,
			AgeSeconds: reading.AgeSeconds,
			Stale:      reading.Stale,
			Source:     reading.Source,
		})
	})
}
