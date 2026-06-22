package cloudenroll

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// xorSealer is a trivial Sealer for tests (NOT real sealing): it round-trips so
// Load() can recover the key.
type xorSealer struct{}

func (xorSealer) Seal(p []byte) ([]byte, error) {
	out := make([]byte, len(p))
	for i, b := range p {
		out[i] = b ^ 0x5a
	}
	return out, nil
}
func (xorSealer) Unseal(c []byte) ([]byte, error) { return xorSealer{}.Seal(c) }

// caSignCert reproduces the CP management-CA device-cert signature.
func caSignCert(caPriv ed25519.PrivateKey, devicePub []byte, account, ulid string) []byte {
	payload := "vulos-device-cert:v1:" +
		base64.RawURLEncoding.EncodeToString(devicePub) + ":" + account + ":" + ulid
	return ed25519.Sign(caPriv, []byte(payload))
}

// enrollStub mimics the CP /enroll/start + /enroll/poll endpoints. It returns
// authorization_pending once, then approves with a CA-signed cert.
func enrollStub(t *testing.T, caPriv ed25519.PrivateKey, account, ulid string) *httptest.Server {
	t.Helper()
	var devicePub []byte
	var polls int32
	mux := http.NewServeMux()
	mux.HandleFunc("/enroll/start", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			DevicePubKey string `json:"device_pubkey"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		devicePub, _ = base64.StdEncoding.DecodeString(req.DevicePubKey)
		json.NewEncoder(w).Encode(map[string]any{
			"device_code": "dev-code", "user_code": "ABCD-1234",
			"verification_uri": "https://vulos.org/activate", "interval": 1, "expires_in": 60,
		})
	})
	mux.HandleFunc("/enroll/poll", func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&polls, 1) == 1 {
			json.NewEncoder(w).Encode(map[string]string{"error": "authorization_pending"})
			return
		}
		cert := caSignCert(caPriv, devicePub, account, ulid)
		json.NewEncoder(w).Encode(map[string]any{
			"ulid": ulid, "account_id": account, "device_cert": cert, // []byte → base64
		})
	})
	return httptest.NewServer(mux)
}

func TestEnrollAndLoad(t *testing.T) {
	caPub, caPriv, _ := ed25519.GenerateKey(rand.Reader)
	const account, ulid = "acct-123", "01ARZ3NDEKTSV4RRFFQ69G5FAV"
	srv := enrollStub(t, caPriv, account, ulid)
	defer srv.Close()

	dir := t.TempDir()
	e := New(srv.URL, dir, xorSealer{})
	e.intervalOverride = 2 * time.Millisecond

	var gotCode string
	id, err := e.Enroll(context.Background(), func(uc, uri string) { gotCode = uc })
	if err != nil {
		t.Fatalf("Enroll: %v", err)
	}
	if gotCode != "ABCD-1234" {
		t.Fatalf("notify user_code = %q", gotCode)
	}
	if id.ULID != ulid || id.Account != account {
		t.Fatalf("identity mismatch: %+v", id)
	}

	// The cert verifies against the CA over the exact owner-attested payload.
	cert, pub, ok := id.DeviceCert()
	if !ok {
		t.Fatal("DeviceCert not ok after enroll")
	}
	payload := "vulos-device-cert:v1:" + base64.RawURLEncoding.EncodeToString(pub) + ":" + account + ":" + ulid
	if !ed25519.Verify(caPub, []byte(payload), cert) {
		t.Fatal("cert does not verify against CA")
	}

	// The device key signs mint messages, verifiable with the cert's pubkey.
	sig, err := id.SignMint("integrations:token:google:" + ulid)
	if err != nil {
		t.Fatalf("SignMint: %v", err)
	}
	if !ed25519.Verify(pub, []byte("integrations:token:google:"+ulid), sig) {
		t.Fatal("mint signature does not verify")
	}

	// Load() recovers the same identity (unseals the key) from a fresh Enroller.
	id2, err := New(srv.URL, dir, xorSealer{}).Load()
	if err != nil || id2 == nil {
		t.Fatalf("Load: %v (id=%v)", err, id2)
	}
	if id2.ULID != ulid || string(id2.Cert) != string(id.Cert) {
		t.Fatal("loaded identity mismatch")
	}
	sig2, _ := id2.SignMint("m")
	if !ed25519.Verify(id2.Pub, []byte("m"), sig2) {
		t.Fatal("loaded key cannot sign")
	}
}

func TestPollSlowDownWidens(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{"error": "slow_down"})
	}))
	defer srv.Close()
	e := New(srv.URL, t.TempDir(), xorSealer{})
	res, retry, err := e.poll(context.Background(), "dev-code")
	if err != nil || res != nil {
		t.Fatalf("slow_down poll: res=%v err=%v", res, err)
	}
	// Must return a POSITIVE INCREMENT (the loop adds it to the current interval),
	// never a fixed value that could shrink an already-larger cadence.
	if retry != 5*time.Second {
		t.Fatalf("slow_down retry = %v, want +5s increment", retry)
	}
}

func TestLoadNotEnrolled(t *testing.T) {
	id, err := New("http://unused", t.TempDir(), xorSealer{}).Load()
	if err != nil || id != nil {
		t.Fatalf("want (nil,nil) when not enrolled, got (%v,%v)", id, err)
	}
}
