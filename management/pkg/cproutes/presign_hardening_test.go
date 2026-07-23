// presign_hardening_test.go — M5 (presign TTL max clamp) + L1 (legacy presign
// path rejects a traversal key) regressions.
package cproutes

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// M5: an over-long ttl_seconds is clamped to maxPresignTTL.
func TestPresignPut_ClampsTTLToMax(t *testing.T) {
	e := newStorageTestEnv(t)
	uid, tok := sessionFor(t, e.authSt, "ttl-clamp@example.com")
	ownBucket := "vulos-" + strings.ToLower(boxULID(uid))

	body := map[string]any{
		"account_id":  uid,
		"bucket":      ownBucket,
		"key":         "obj",
		"ttl_seconds": 100000, // ~27h, far over the cap
	}
	w := e.do(t, http.MethodPost, "/api/storage/presign/put", body, tok)
	if w.Code != http.StatusOK {
		t.Fatalf("presign put = %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		URL string `json:"url"`
	}
	_ = json.NewDecoder(w.Body).Decode(&resp)

	// MemProvider encodes the ttl (seconds) into the synthetic URL.
	wantTTL := int64(maxPresignTTL.Seconds())
	if !strings.Contains(resp.URL, "ttl="+itoa(wantTTL)) {
		t.Errorf("ttl not clamped to %ds; url=%q", wantTTL, resp.URL)
	}
}

// L1: the legacy (un-app-scoped) presign path rejects a path-traversal key.
func TestPresignLegacy_RejectsTraversalKey(t *testing.T) {
	e := newStorageTestEnv(t)
	uid, tok := sessionFor(t, e.authSt, "traversal@example.com")
	ownBucket := "vulos-" + strings.ToLower(boxULID(uid))

	body := map[string]any{
		"account_id": uid,
		"bucket":     ownBucket,
		"key":        "a/../../etc/passwd",
		// no app_id → legacy path
	}
	w := e.do(t, http.MethodPost, "/api/storage/presign/get", body, tok)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("traversal key should be 400, got %d: %s", w.Code, w.Body.String())
	}
}

// itoa avoids pulling strconv into the test's import set for a single use.
func itoa(v int64) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	var b [20]byte
	i := len(b)
	for v > 0 {
		i--
		b[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
