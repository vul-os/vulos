package clientcerts

import (
	"encoding/json"
	"fmt"
	"net/http"
)

// RegisterHandlers wires all clientcerts routes into mux.
//
// Routes:
//
//	POST   /api/auth/certs/install        install cert + key
//	GET    /api/auth/certs                list installed certificates (metadata)
//	DELETE /api/auth/certs/{domain}       remove certificate
//	GET    /api/auth/certs/{domain}/status expiry, issuer, etc.
//	POST   /api/auth/certs/generate-csr   generate a CSR for a new cert
func RegisterHandlers(mux *http.ServeMux, s *Store) {
	h := &handler{store: s}

	mux.HandleFunc("POST /api/auth/certs/install", h.handleInstall)
	mux.HandleFunc("GET /api/auth/certs", h.handleList)
	mux.HandleFunc("DELETE /api/auth/certs/{domain}", h.handleDelete)
	mux.HandleFunc("GET /api/auth/certs/{domain}/status", h.handleStatus)
	mux.HandleFunc("POST /api/auth/certs/generate-csr", h.handleGenerateCSR)
}

type handler struct {
	store *Store
}

// handleInstall installs a client certificate and its private key.
//
// Request body (JSON):
//
//	{
//	  "domain":    "bank.example.com",   // required
//	  "cert_pem":  "-----BEGIN CERTIFICATE-----\n...",  // required
//	  "key_pem":   "-----BEGIN EC PRIVATE KEY-----\n...", // required
//	  "chain_pem": "-----BEGIN CERTIFICATE-----\n..."  // optional
//	}
func (h *handler) handleInstall(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Domain   string `json:"domain"`
		CertPEM  string `json:"cert_pem"`
		KeyPEM   string `json:"key_pem"`
		ChainPEM string `json:"chain_pem"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Domain == "" || req.CertPEM == "" || req.KeyPEM == "" {
		writeErr(w, http.StatusBadRequest, "domain, cert_pem, and key_pem are required")
		return
	}
	if err := h.store.Install(req.Domain, req.CertPEM, req.KeyPEM, req.ChainPEM); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"status": "installed", "domain": req.Domain})
}

// handleList returns metadata for every installed certificate.
func (h *handler) handleList(w http.ResponseWriter, r *http.Request) {
	infos, err := h.store.List()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if infos == nil {
		infos = []CertInfo{}
	}
	writeJSON(w, http.StatusOK, infos)
}

// handleDelete removes the certificate for a domain.
func (h *handler) handleDelete(w http.ResponseWriter, r *http.Request) {
	domain := r.PathValue("domain")
	if domain == "" {
		writeErr(w, http.StatusBadRequest, "domain is required")
		return
	}
	if err := h.store.Delete(domain); err != nil {
		writeErr(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted", "domain": domain})
}

// handleStatus returns certificate metadata (expiry, issuer, etc.) for a domain.
func (h *handler) handleStatus(w http.ResponseWriter, r *http.Request) {
	domain := r.PathValue("domain")
	if domain == "" {
		writeErr(w, http.StatusBadRequest, "domain is required")
		return
	}
	info, err := h.store.Status(domain)
	if err != nil {
		writeErr(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, info)
}

// handleGenerateCSR generates a new key pair, seals the private key, and
// returns a PEM-encoded CSR.
//
// Request body (JSON):
//
//	{
//	  "domain": "bank.example.com",      // required — used as storage key
//	  "cn":     "My Vula Client",        // required — certificate common name
//	  "sans":   ["bank.example.com"],    // optional — DNS names or IPs
//	  "o":      "Vula OS",               // optional — organisation
//	  "c":      "ZA"                     // optional — country code
//	}
func (h *handler) handleGenerateCSR(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Domain string   `json:"domain"`
		CN     string   `json:"cn"`
		SANs   []string `json:"sans"`
		O      string   `json:"o"`
		C      string   `json:"c"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Domain == "" || req.CN == "" {
		writeErr(w, http.StatusBadRequest, "domain and cn are required")
		return
	}
	info, err := h.store.GenerateCSR(CSRRequest{
		Domain: req.Domain,
		CN:     req.CN,
		SANs:   req.SANs,
		O:      req.O,
		C:      req.C,
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, info)
}

// --- JSON helpers ---

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	fmt.Fprintf(w, `{"error":%q}`, msg)
}
