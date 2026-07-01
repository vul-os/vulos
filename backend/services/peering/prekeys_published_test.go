package peering

import (
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// pkPublishBundle builds a valid client-published PUBLIC bundle (signed prekey +
// n one-time prekeys) for the given identity. It mirrors what fabric.js/prekeys.js
// produce: the signed prekey public is signed by the Ed25519 identity key, and all
// keys are PUBLIC X25519 points. It returns the bundle and, separately, the OPK
// PRIVATE scalars so a test can assert those never appear in any host response.
func pkPublishBundle(t *testing.T, idPriv ed25519.PrivateKey, idVulaID string, n int) (*PreKeyBundlePublic, map[string][]byte) {
	t.Helper()
	_, spkPub, err := genX25519()
	if err != nil {
		t.Fatalf("genX25519 spk: %v", err)
	}
	b := &PreKeyBundlePublic{
		IdentityVulaID: idVulaID,
		SignedPreKey: SignedPreKeyPublic{
			ID:  newKeyID(),
			Pub: spkPub,
			Sig: ed25519.Sign(idPriv, spkPub),
		},
	}
	priv := map[string][]byte{}
	for i := 0; i < n; i++ {
		p, pub, err := genX25519()
		if err != nil {
			t.Fatalf("genX25519 opk: %v", err)
		}
		id := newKeyID()
		b.OneTimePreKeys = append(b.OneTimePreKeys, OneTimePreKeyPublic{ID: id, Pub: pub})
		priv[id] = p
	}
	return b, priv
}

// TestPublishedBundle_ClaimDepletesToNull is the round-trip: publish a browser
// peer's public bundle, then two distinct single-use claims deplete the pool, and a
// third claim returns a nil OPK (signed-prekey-only fallback, never reuse).
func TestPublishedBundle_ClaimDepletesToNull(t *testing.T) {
	alicePriv, aliceID := pkIdentity(t)
	store := NewPublishedBundleStore(0, 0)
	bundle, _ := pkPublishBundle(t, alicePriv, aliceID, 2)
	if err := store.Publish(bundle); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	c1, ok := store.Claim(aliceID)
	if !ok || c1.OneTimePreKey == nil {
		t.Fatalf("claim 1 must yield an OPK: ok=%v c=%+v", ok, c1)
	}
	c2, ok := store.Claim(aliceID)
	if !ok || c2.OneTimePreKey == nil {
		t.Fatalf("claim 2 must yield an OPK: ok=%v c=%+v", ok, c2)
	}
	if c1.OneTimePreKey.ID == c2.OneTimePreKey.ID {
		t.Fatal("two claims returned the SAME OPK — no per-sender forward secrecy")
	}
	// Signed prekey present on both and verifies against the identity.
	vb := &PreKeyBundlePublic{IdentityVulaID: aliceID, SignedPreKey: c1.SignedPreKey}
	if err := vb.VerifySignedPreKey(); err != nil {
		t.Fatalf("claimed signed prekey does not verify: %v", err)
	}
	// Pool exhausted → nil OPK, still ok=true (signed-prekey-only fallback).
	c3, ok := store.Claim(aliceID)
	if !ok {
		t.Fatal("published identity must remain claimable after exhaustion")
	}
	if c3.OneTimePreKey != nil {
		t.Fatal("exhausted published pool must return a nil one_time_prekey")
	}
}

// TestPublishedBundle_ForgedRejected: a bundle whose signed prekey is NOT signed by
// the claimed identity (forged/unsigned) is rejected and nothing is stored.
func TestPublishedBundle_ForgedRejected(t *testing.T) {
	_, aliceID := pkIdentity(t)      // victim identity
	attackerPriv, _ := pkIdentity(t) // attacker's key
	store := NewPublishedBundleStore(0, 0)

	// Attacker publishes a bundle CLAIMING to be alice but signs with their own key.
	forged, _ := pkPublishBundle(t, attackerPriv, aliceID, 1)
	if err := store.Publish(forged); err == nil {
		t.Fatal("forged bundle (wrong signer) must be rejected")
	}
	if store.Has(aliceID) {
		t.Fatal("rejected forged bundle must not be stored")
	}

	// An unsigned bundle (empty sig) is likewise rejected.
	unsigned, _ := pkPublishBundle(t, attackerPriv, aliceID, 1)
	unsigned.SignedPreKey.Sig = nil
	if err := store.Publish(unsigned); err == nil {
		t.Fatal("unsigned bundle must be rejected")
	}
}

// TestPublishHandler_HTTP exercises the wire endpoints end-to-end and asserts NO
// private key is ever serialized in a claim response.
func TestPublishHandler_HTTP(t *testing.T) {
	// Host has its OWN identity/store; alice is a REMOTE browser peer.
	bobPriv, bobID := pkIdentity(t)
	ownStore, err := NewPreKeyStore(t.TempDir(), bobID, bobPriv, 2)
	if err != nil {
		t.Fatalf("NewPreKeyStore: %v", err)
	}
	published := NewPublishedBundleStore(0, 0)
	mux := http.NewServeMux()
	RegisterPreKeyHandlers(mux, ownStore, published)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	alicePriv, aliceID := pkIdentity(t)
	bundle, opkPriv := pkPublishBundle(t, alicePriv, aliceID, 2)

	// PUBLISH.
	pubBody, _ := json.Marshal(bundle)
	pres, err := http.Post(srv.URL+"/api/peering/prekeys/publish", "application/json", bytes.NewReader(pubBody))
	if err != nil {
		t.Fatalf("publish post: %v", err)
	}
	if pres.StatusCode != http.StatusOK {
		t.Fatalf("publish status = %d, want 200", pres.StatusCode)
	}
	pres.Body.Close()

	claim := func(id string) ([]byte, int) {
		body, _ := json.Marshal(map[string]string{"identity_vula_id": id})
		resp, err := http.Post(srv.URL+"/api/peering/prekeys/claim", "application/json", bytes.NewReader(body))
		if err != nil {
			t.Fatalf("claim post: %v", err)
		}
		defer resp.Body.Close()
		raw := new(bytes.Buffer)
		raw.ReadFrom(resp.Body)
		return raw.Bytes(), resp.StatusCode
	}

	// Claim alice's PUBLISHED bundle twice: distinct single-use OPKs.
	raw1, code1 := claim(aliceID)
	if code1 != http.StatusOK {
		t.Fatalf("claim published: code=%d body=%s", code1, raw1)
	}
	var cb1 ClaimedBundle
	if err := json.Unmarshal(raw1, &cb1); err != nil {
		t.Fatalf("decode claim1: %v", err)
	}
	if cb1.IdentityVulaID != aliceID || cb1.OneTimePreKey == nil {
		t.Fatalf("published claim must return alice + an OPK: %+v", cb1)
	}
	raw2, _ := claim(aliceID)
	var cb2 ClaimedBundle
	json.Unmarshal(raw2, &cb2)
	if cb2.OneTimePreKey == nil || cb2.OneTimePreKey.ID == cb1.OneTimePreKey.ID {
		t.Fatal("second published claim must return a DIFFERENT OPK")
	}

	// Third claim: pool exhausted → null OPK, still 200.
	raw3, code3 := claim(aliceID)
	if code3 != http.StatusOK {
		t.Fatalf("exhausted claim code=%d", code3)
	}
	if bytes.Contains(raw3, []byte(`"one_time_prekey":{`)) {
		t.Fatalf("exhausted claim must serialize null one_time_prekey, got %s", raw3)
	}

	// Unpublished identity → 404 (no crash).
	_, otherID := pkIdentity(t)
	if _, code := claim(otherID); code != http.StatusNotFound {
		t.Fatalf("unpublished identity claim: code=%d, want 404", code)
	}

	// CLOUD-BLIND: no OPK PRIVATE scalar ever appears in any claim response.
	for id, priv := range opkPriv {
		b64 := jsonB64(priv)
		for _, raw := range [][]byte{raw1, raw2, raw3} {
			if bytes.Contains(raw, []byte(b64)) {
				t.Fatalf("OPK private scalar for %s leaked in a claim response", id)
			}
		}
	}
	// The host's OWN identity path still works (regression).
	if _, code := claim(bobID); code != http.StatusOK {
		t.Fatalf("own-identity claim regressed: code=%d", code)
	}
}

// TestPublishedBundle_NoPrivateBytesStored confirms the published store retains
// ZERO of the input private scalars: it only ever received public structs.
func TestPublishedBundle_NoPrivateBytesStored(t *testing.T) {
	alicePriv, aliceID := pkIdentity(t)
	store := NewPublishedBundleStore(0, 0)
	bundle, opkPriv := pkPublishBundle(t, alicePriv, aliceID, 3)
	if err := store.Publish(bundle); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	// Serialize the entire stored entry's public material and confirm no private
	// scalar is present anywhere in it.
	entry := store.byIdentity[aliceID]
	blob, _ := json.Marshal(struct {
		S SignedPreKeyPublic
		O map[string]OneTimePreKeyPublic
	}{entry.signedPreKey, entry.opks})
	for id, priv := range opkPriv {
		if bytes.Contains(blob, []byte(jsonB64(priv))) {
			t.Fatalf("private scalar for OPK %s found in stored bundle", id)
		}
	}
	// Sanity: the store rejected nothing and holds the public keys.
	if store.OneTimePreKeyCount(aliceID) != 3 {
		t.Fatalf("expected 3 stored public OPKs, got %d", store.OneTimePreKeyCount(aliceID))
	}
}

// jsonB64 renders a byte slice the way encoding/json marshals a []byte field
// (standard base64, quoted), so a substring search matches on-wire encoding.
func jsonB64(b []byte) string {
	out, _ := json.Marshal(b)
	// strip surrounding quotes
	return string(bytes.Trim(out, `"`))
}
