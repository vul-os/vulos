package devicekey

// HTTP-layer tests for the device-key API. Written against main's
// interface-based KeyStore (devicekey.Open → software backend in CI).
// AUTH-10c's original test targeted a different concrete API (New,
// *KeyStore, Identity, Verify, ks.b) that does not exist on main; the
// keystore internals are covered by keystore_test.go.

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func dhSetup(t *testing.T) (mux *http.ServeMux, setAdmin func(bool)) {
	t.Helper()
	ks, err := Open(t.TempDir()) // no TPM in CI → software backend
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = ks.Close() })
	adminFlag := true
	mux = http.NewServeMux()
	RegisterHandlers(mux, ks, func(r *http.Request) bool { return adminFlag })
	return mux, func(v bool) { adminFlag = v }
}

func dhGet(t *testing.T, h http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, path, nil))
	return rr
}

func dhPost(t *testing.T, h http.Handler, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr
}

func TestDeviceIdentityEndpoint(t *testing.T) {
	mux, _ := dhSetup(t)
	rr := dhGet(t, mux, "/api/auth/device/identity")
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	var id identityResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &id); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if id.DeviceID == "" || id.PublicKey == "" || id.Backend == "" {
		t.Errorf("identity fields must be non-empty: %+v", id)
	}
}

func TestDeviceTPMStatusEndpoint(t *testing.T) {
	mux, _ := dhSetup(t)
	rr := dhGet(t, mux, "/api/auth/device/tpm/status")
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	var st Status
	if err := json.Unmarshal(rr.Body.Bytes(), &st); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !st.Available {
		t.Error("software backend should report available=true")
	}
	if st.Backend != BackendSoftware {
		t.Errorf("expected backend=%q, got %q", BackendSoftware, st.Backend)
	}
}

func TestDeviceSealUnsealRoundTrip(t *testing.T) {
	mux, _ := dhSetup(t)
	plaintext := []byte("hello device key")

	sealResp := dhPost(t, mux, "/api/auth/device/seal", map[string][]byte{"data": plaintext})
	if sealResp.Code != http.StatusOK {
		t.Fatalf("seal: expected 200, got %d: %s", sealResp.Code, sealResp.Body.String())
	}
	var sealed struct {
		Sealed []byte `json:"sealed"`
	}
	if err := json.Unmarshal(sealResp.Body.Bytes(), &sealed); err != nil {
		t.Fatalf("seal unmarshal: %v", err)
	}
	if len(sealed.Sealed) == 0 {
		t.Fatal("sealed data should not be empty")
	}

	unsealResp := dhPost(t, mux, "/api/auth/device/unseal", map[string][]byte{"sealed": sealed.Sealed})
	if unsealResp.Code != http.StatusOK {
		t.Fatalf("unseal: expected 200, got %d: %s", unsealResp.Code, unsealResp.Body.String())
	}
	var result struct {
		Data []byte `json:"data"`
	}
	if err := json.Unmarshal(unsealResp.Body.Bytes(), &result); err != nil {
		t.Fatalf("unseal unmarshal: %v", err)
	}
	if !bytes.Equal(result.Data, plaintext) {
		t.Errorf("round-trip mismatch: got %q, want %q", result.Data, plaintext)
	}
}

func TestDeviceSealForbiddenWithoutAdmin(t *testing.T) {
	mux, setAdmin := dhSetup(t)
	setAdmin(false)
	if rr := dhPost(t, mux, "/api/auth/device/seal", map[string][]byte{"data": []byte("x")}); rr.Code != http.StatusForbidden {
		t.Errorf("expected 403, got %d", rr.Code)
	}
	if rr := dhPost(t, mux, "/api/auth/device/unseal", map[string][]byte{"sealed": []byte("x")}); rr.Code != http.StatusForbidden {
		t.Errorf("expected 403, got %d", rr.Code)
	}
}

func TestDeviceSealEmptyData(t *testing.T) {
	mux, _ := dhSetup(t)
	if rr := dhPost(t, mux, "/api/auth/device/seal", map[string][]byte{"data": nil}); rr.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rr.Code)
	}
}
