package gpu

import (
	"encoding/json"
	"net/http"
)

// RegisterGPUInfoHandlers registers GPU info endpoints on the provided mux.
// GET /api/gpu/info returns the detected GPU capabilities as JSON.
func RegisterGPUInfoHandlers(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/gpu/info", gpuapiHandleInfo)
}

func gpuapiHandleInfo(w http.ResponseWriter, r *http.Request) {
	info := Detect()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(info)
}
