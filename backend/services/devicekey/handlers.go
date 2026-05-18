package devicekey

// handlers.go — HTTP handlers for the device identity and TPM status API.
//
// Ported from task/AUTH-10c onto main's interface-based KeyStore (main
// already had the more complete keystore/software/tpm backends; only this
// HTTP layer was genuinely unlanded). Routes:
//
//	GET  /api/auth/device/identity    — public: device public key + backend info
//	GET  /api/auth/device/tpm/status  — public: TPM availability, type
//	POST /api/auth/device/seal        — admin-only: seal arbitrary data
//	POST /api/auth/device/unseal      — admin-only: unseal previously sealed data

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
)

// RegisterHandlers wires all device-key routes into mux using the supplied
// KeyStore. adminCheck is called for admin-only endpoints and should return
// true when the request is from an authorised administrator.
func RegisterHandlers(mux *http.ServeMux, ks KeyStore, adminCheck func(r *http.Request) bool) {
	h := &deviceHandler{ks: ks, isAdmin: adminCheck}

	mux.HandleFunc("GET /api/auth/device/identity", h.handleIdentity)
	mux.HandleFunc("GET /api/auth/device/tpm/status", h.handleTPMStatus)
	mux.HandleFunc("POST /api/auth/device/seal", h.handleSeal)
	mux.HandleFunc("POST /api/auth/device/unseal", h.handleUnseal)
}

type deviceHandler struct {
	ks      KeyStore
	isAdmin func(r *http.Request) bool
}

// identityResponse is the JSON shape for GET /api/auth/device/identity.
// main's KeyStore.DeviceIdentity returns raw PKIX DER bytes, so the
// human/machine-friendly object is assembled here from Status + the key.
type identityResponse struct {
	DeviceID  string      `json:"device_id"`
	PublicKey string      `json:"public_key"` // base64-encoded PKIX DER
	Backend   BackendType `json:"backend"`
}

// handleIdentity returns the device public key and backend type.
func (h *deviceHandler) handleIdentity(w http.ResponseWriter, r *http.Request) {
	der, err := h.ks.DeviceIdentity()
	if err != nil {
		writeDeviceErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	st := h.ks.Status()
	writeDeviceJSON(w, identityResponse{
		DeviceID:  st.DeviceID,
		PublicKey: base64.StdEncoding.EncodeToString(der),
		Backend:   st.Backend,
	})
}

// handleTPMStatus returns the current TPM/keystore availability status.
func (h *deviceHandler) handleTPMStatus(w http.ResponseWriter, r *http.Request) {
	writeDeviceJSON(w, h.ks.Status())
}

// handleSeal seals request body data to this device.
// Request: {"data": "<base64-plaintext>"}  Response: {"sealed": "<base64>"}
func (h *deviceHandler) handleSeal(w http.ResponseWriter, r *http.Request) {
	if !h.isAdmin(r) {
		writeDeviceErr(w, http.StatusForbidden, "admin required")
		return
	}
	var req struct {
		Data []byte `json:"data"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeDeviceErr(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if len(req.Data) == 0 {
		writeDeviceErr(w, http.StatusBadRequest, "data is required")
		return
	}
	sealed, err := h.ks.Seal(req.Data)
	if err != nil {
		writeDeviceErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeDeviceJSON(w, map[string][]byte{"sealed": sealed})
}

// handleUnseal unseals previously sealed data.
// Request: {"sealed": "<base64>"}  Response: {"data": "<base64-plaintext>"}
func (h *deviceHandler) handleUnseal(w http.ResponseWriter, r *http.Request) {
	if !h.isAdmin(r) {
		writeDeviceErr(w, http.StatusForbidden, "admin required")
		return
	}
	var req struct {
		Sealed []byte `json:"sealed"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeDeviceErr(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if len(req.Sealed) == 0 {
		writeDeviceErr(w, http.StatusBadRequest, "sealed is required")
		return
	}
	plaintext, err := h.ks.Unseal(req.Sealed)
	if err != nil {
		writeDeviceErr(w, http.StatusInternalServerError, "unseal failed: "+err.Error())
		return
	}
	writeDeviceJSON(w, map[string][]byte{"data": plaintext})
}

// helpers ----------------------------------------------------------------

func writeDeviceJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func writeDeviceErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
