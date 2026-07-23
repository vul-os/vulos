// routes_kms.go — Settings → Encryption (BYO-KMS) surface: register/rotate
// the owner's KEK reference and wrap/unwrap DEK operations.
//
// Routes registered (all OWNER/ADMIN-ONLY via instRequireAdmin — this is a
// single-owner box feature, not a per-user one):
//
//	GET  /api/kms/status              — configured?, kind, endpoint, kek_version, dek_count
//	POST /api/kms/configure           — register the KEK reference (first time only)
//	POST /api/kms/rotate              — rotate the KEK reference (step-up gated)
//	GET  /api/kms/deks                — list wrapped-DEK records (never the wrapped bytes)
//	POST /api/kms/deks                — wrap: envelope-encrypt caller-supplied data
//	POST /api/kms/deks/{id}/decrypt   — unwrap-export: returns plaintext (step-up gated)
//	POST /api/kms/deks/{id}/revoke    — revoke a DEK (soft-delete, kept for audit)
//
// Step-up ("extra security step") gates KEK rotation and the one
// unwrap-export endpoint that returns plaintext to the caller — both are the
// destructive/sensitive edges of the feature: rotation changes which key
// protects every wrapped DEK, and export is the only path that hands
// decrypted bytes back over the wire.
package main

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"log"
	"net/http"

	"vulos/backend/services/auth"
	vulenv "vulos/backend/services/env"
	"vulos/backend/services/kms"
	"vulos/backend/services/stepup"
)

// registerKMSRoutes wires the BYO-KMS routes onto mux. It opens the KMS
// SQLite store and loads the local KMS_STORAGE_KEK (fail-closed in prod); on
// either failure it logs and registers NOTHING for the KMS surface — the box
// simply has no BYO-KMS feature rather than running with an insecure default
// key. authStore may be nil in degraded boot; the routes then fail closed
// (403) via instRequireAdmin rather than allow an unauthenticated caller to
// touch key material.
func registerKMSRoutes(mux *http.ServeMux, authStore *auth.Store, dbDir string, activeEnv vulenv.Env) {
	storageKEK, err := kms.LoadKEK(activeEnv)
	if err != nil {
		log.Printf("[kms] BYO-KMS DISABLED: %v (set KMS_STORAGE_KEK to enable Settings → Encryption)", err)
		return
	}
	store, err := kms.OpenStore(dbDir)
	if err != nil {
		log.Printf("[kms] BYO-KMS DISABLED: could not open store: %v", err)
		return
	}
	svc := kms.NewService(store, storageKEK)

	mux.HandleFunc("GET /api/kms/status", func(w http.ResponseWriter, r *http.Request) {
		if !instRequireAdmin(w, r, authStore) {
			return
		}
		st, err := svc.Status(r.Context())
		if err != nil {
			log.Printf("[kms] status: %v", err)
			writeErr(w, http.StatusInternalServerError, "could not read KMS status")
			return
		}
		writeJSON(w, st)
	})

	mux.HandleFunc("POST /api/kms/configure", func(w http.ResponseWriter, r *http.Request) {
		if !instRequireAdmin(w, r, authStore) {
			return
		}
		var body struct {
			Kind        string `json:"kind"`
			Endpoint    string `json:"endpoint"`
			KeyMaterial string `json:"key_material"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10)).Decode(&body); err != nil {
			writeErr(w, http.StatusBadRequest, "invalid JSON body")
			return
		}
		kind, err := parseProviderKind(body.Kind)
		if err != nil {
			writeErr(w, http.StatusUnprocessableEntity, err.Error())
			return
		}
		cfg, generatedKey, err := svc.Configure(r.Context(), kind, body.Endpoint, body.KeyMaterial)
		switch {
		case err == kms.ErrAlreadyConfigured:
			writeErr(w, http.StatusConflict, "BYO-KMS is already configured — use /api/kms/rotate to change the KEK")
			return
		case err != nil:
			log.Printf("[kms] configure: %v", err)
			writeErr(w, http.StatusUnprocessableEntity, err.Error())
			return
		}
		writeJSON(w, map[string]any{
			"config":            cfg,
			"generated_key_hex": generatedKey, // "" unless the box generated the key — shown ONCE
		})
	})

	mux.HandleFunc("POST /api/kms/rotate", func(w http.ResponseWriter, r *http.Request) {
		if !instRequireAdmin(w, r, authStore) {
			return
		}
		if err := stepup.Require(r); err != nil {
			writeErr(w, http.StatusForbidden, "step-up verification required to rotate the KEK")
			return
		}
		var body struct {
			Kind        string `json:"kind"`
			Endpoint    string `json:"endpoint"`
			KeyMaterial string `json:"key_material"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<10)).Decode(&body); err != nil {
			writeErr(w, http.StatusBadRequest, "invalid JSON body")
			return
		}
		kind, err := parseProviderKind(body.Kind)
		if err != nil {
			writeErr(w, http.StatusUnprocessableEntity, err.Error())
			return
		}
		result, generatedKey, err := svc.RotateKEK(r.Context(), kind, body.Endpoint, body.KeyMaterial)
		switch {
		case err == kms.ErrNotConfigured:
			writeErr(w, http.StatusConflict, "BYO-KMS is not configured yet — use /api/kms/configure first")
			return
		case err != nil:
			log.Printf("[kms] rotate: %v", err)
			writeErr(w, http.StatusInternalServerError, "could not rotate KEK")
			return
		}
		writeJSON(w, map[string]any{
			"result":            result,
			"generated_key_hex": generatedKey,
		})
	})

	mux.HandleFunc("GET /api/kms/deks", func(w http.ResponseWriter, r *http.Request) {
		if !instRequireAdmin(w, r, authStore) {
			return
		}
		includeRevoked := r.URL.Query().Get("include_revoked") == "true"
		deks, err := svc.ListDEKs(r.Context(), includeRevoked)
		if err != nil {
			log.Printf("[kms] list deks: %v", err)
			writeErr(w, http.StatusInternalServerError, "could not list DEKs")
			return
		}
		writeJSON(w, deks)
	})

	mux.HandleFunc("POST /api/kms/deks", func(w http.ResponseWriter, r *http.Request) {
		if !instRequireAdmin(w, r, authStore) {
			return
		}
		var body struct {
			ObjectRef       string `json:"object_ref"`
			PlaintextBase64 string `json:"plaintext_base64"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body); err != nil {
			writeErr(w, http.StatusBadRequest, "invalid JSON body")
			return
		}
		if body.ObjectRef == "" {
			writeErr(w, http.StatusUnprocessableEntity, "object_ref is required")
			return
		}
		plaintext, err := base64.StdEncoding.DecodeString(body.PlaintextBase64)
		if err != nil {
			writeErr(w, http.StatusUnprocessableEntity, "plaintext_base64 is not valid base64")
			return
		}
		ciphertext, dekID, err := svc.WrapData(r.Context(), body.ObjectRef, plaintext)
		switch {
		case err == kms.ErrNotConfigured:
			writeErr(w, http.StatusConflict, "BYO-KMS is not configured yet — use /api/kms/configure first")
			return
		case err != nil:
			log.Printf("[kms] wrap: %v", err)
			writeErr(w, http.StatusBadGateway, "could not wrap data under the owner's KEK")
			return
		}
		writeJSON(w, map[string]string{"id": dekID, "ciphertext": ciphertext})
	})

	mux.HandleFunc("POST /api/kms/deks/{id}/decrypt", func(w http.ResponseWriter, r *http.Request) {
		if !instRequireAdmin(w, r, authStore) {
			return
		}
		if err := stepup.Require(r); err != nil {
			writeErr(w, http.StatusForbidden, "step-up verification required to export decrypted data")
			return
		}
		var body struct {
			Ciphertext string `json:"ciphertext"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body); err != nil {
			writeErr(w, http.StatusBadRequest, "invalid JSON body")
			return
		}
		plaintext, err := svc.UnwrapData(r.Context(), r.PathValue("id"), body.Ciphertext)
		switch {
		case err == kms.ErrUnknownKey:
			writeErr(w, http.StatusNotFound, "unknown key id")
			return
		case err == kms.ErrKeyRevoked:
			writeErr(w, http.StatusConflict, "key has been revoked")
			return
		case err != nil:
			log.Printf("[kms] unwrap: %v", err)
			writeErr(w, http.StatusBadGateway, "could not unwrap data — check the ciphertext and owner KEK availability")
			return
		}
		writeJSON(w, map[string]string{"plaintext_base64": base64.StdEncoding.EncodeToString(plaintext)})
	})

	mux.HandleFunc("POST /api/kms/deks/{id}/revoke", func(w http.ResponseWriter, r *http.Request) {
		if !instRequireAdmin(w, r, authStore) {
			return
		}
		if err := svc.RevokeDEK(r.Context(), r.PathValue("id")); err != nil {
			if err == kms.ErrUnknownKey {
				writeErr(w, http.StatusNotFound, "unknown key id")
				return
			}
			log.Printf("[kms] revoke: %v", err)
			writeErr(w, http.StatusInternalServerError, "could not revoke key")
			return
		}
		writeJSON(w, map[string]string{"status": "revoked", "id": r.PathValue("id")})
	})

	log.Printf("[kms] BYO-KMS ready: registered /api/kms/{status,configure,rotate,deks,...}")
}

// parseProviderKind validates the request-supplied provider kind string.
func parseProviderKind(s string) (kms.ProviderKind, error) {
	switch kms.ProviderKind(s) {
	case kms.KindSymmetric, kms.KindHTTP:
		return kms.ProviderKind(s), nil
	default:
		return "", errInvalidProviderKind
	}
}

var errInvalidProviderKind = errors.New(`kind must be "symmetric" or "http"`)
