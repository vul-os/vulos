package pairing

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// genDeviceKey returns a fresh ECDSA P-256 device public key as standard-base64
// PKIX DER, plus its pairing DeviceID fingerprint (the SAME scheme
// devicekey.Fingerprint uses).
func genDeviceKey(t *testing.T) (b64, fingerprint string) {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	der, err := x509.MarshalPKIXPublicKey(&priv.PublicKey)
	if err != nil {
		t.Fatalf("marshal pkix: %v", err)
	}
	h := sha256.Sum256(der)
	return base64.StdEncoding.EncodeToString(der), hex.EncodeToString(h[:])
}

// ─── Issue + Claim: the happy path ────────────────────────────────────────────

func TestIssueClaim_NewDeviceEnrolledWithItsOwnKey(t *testing.T) {
	dir := t.TempDir()
	keyB64, fp := genDeviceKey(t)

	ticket, err := Issue(dir, "issuer-fingerprint-abc", "Laptop", 0)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if ticket.Token == "" || ticket.Sig == "" {
		t.Fatal("issued ticket must carry a token and a signature")
	}
	if !strings.HasPrefix(ticket.ShortCode, "VULOS-") {
		t.Errorf("short code = %q, want VULOS- prefix", ticket.ShortCode)
	}

	res, err := Claim(dir, ticket.Token, "", keyB64)
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if res.Status != "enrolled" {
		t.Errorf("status = %q, want enrolled", res.Status)
	}
	if res.DeviceID != fp {
		t.Errorf("DeviceID = %q, want the submitted key's fingerprint %q", res.DeviceID, fp)
	}
	if res.IssuerDeviceID != "issuer-fingerprint-abc" {
		t.Errorf("IssuerDeviceID = %q, want issuer-fingerprint-abc", res.IssuerDeviceID)
	}
	if res.ApprovalSig == "" {
		t.Error("expected a box-signed enrolment attestation")
	}
	if !res.RequiresPassphrase {
		t.Error("RequiresPassphrase must always be true — pairing never transmits the passphrase")
	}
	// The device name falls back to the ticket's when the claim omits it.
	dev, ok, err := GetPairedDevice(dir, fp)
	if err != nil || !ok {
		t.Fatalf("GetPairedDevice: ok=%v err=%v", ok, err)
	}
	if dev.Name != "Laptop" {
		t.Errorf("device name = %q, want Laptop (ticket fallback)", dev.Name)
	}
	if dev.PublicKey != keyB64 {
		t.Errorf("recorded public key does not match the submitted key")
	}
}

// ─── Single-use ────────────────────────────────────────────────────────────────

func TestClaim_SingleUse(t *testing.T) {
	dir := t.TempDir()
	keyB64, _ := genDeviceKey(t)
	ticket, err := Issue(dir, "issuer", "", 0)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if _, err := Claim(dir, ticket.Token, "", keyB64); err != nil {
		t.Fatalf("first Claim: %v", err)
	}
	// A second redemption of the same token must fail — it was consumed.
	if _, err := Claim(dir, ticket.Token, "", keyB64); err != ErrInvalid {
		t.Fatalf("second Claim err = %v, want ErrInvalid", err)
	}
}

// ─── TTL expiry ──────────────────────────────────────────────────────────────────

func TestClaim_ExpiredTicketRejected(t *testing.T) {
	dir := t.TempDir()
	keyB64, _ := genDeviceKey(t)
	ticket, err := Issue(dir, "issuer", "", time.Nanosecond)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	time.Sleep(2 * time.Millisecond) // ensure the ticket is past its TTL
	if _, err := Claim(dir, ticket.Token, "", keyB64); err != ErrExpired {
		t.Fatalf("Claim err = %v, want ErrExpired", err)
	}
	// The dead ticket is dropped; a repeat is now simply unknown.
	if _, err := Claim(dir, ticket.Token, "", keyB64); err != ErrInvalid {
		t.Fatalf("post-expiry Claim err = %v, want ErrInvalid", err)
	}
}

// ─── Forged / unknown token ──────────────────────────────────────────────────────

func TestClaim_UnknownTokenRejected(t *testing.T) {
	dir := t.TempDir()
	keyB64, _ := genDeviceKey(t)
	// Never issued anything; any token is unknown.
	if _, err := Claim(dir, "totally-made-up-token", "", keyB64); err != ErrInvalid {
		t.Fatalf("Claim err = %v, want ErrInvalid", err)
	}
}

// ─── Tampering ───────────────────────────────────────────────────────────────────

func TestVerifyTicket_DetectsTampering(t *testing.T) {
	dir := t.TempDir()
	ticket, err := Issue(dir, "issuer", "Phone", 0)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if err := VerifyTicket(dir, ticket); err != nil {
		t.Fatalf("genuine ticket should verify: %v", err)
	}
	cases := []struct {
		name string
		mut  func(*Ticket)
	}{
		{"token", func(tk *Ticket) { tk.Token += "x" }},
		{"issuer", func(tk *Ticket) { tk.IssuerDeviceID = "someone-else" }},
		{"expiry", func(tk *Ticket) { tk.ExpiresAt = tk.ExpiresAt.Add(time.Hour) }},
		{"device_name", func(tk *Ticket) { tk.DeviceName = "Attacker" }},
		{"sig", func(tk *Ticket) {
			tk.Sig = base64.RawURLEncoding.EncodeToString([]byte("not a real signature at all!!"))
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			bad := *ticket
			c.mut(&bad)
			if err := VerifyTicket(dir, &bad); err != ErrTampered {
				t.Fatalf("tampered(%s) VerifyTicket = %v, want ErrTampered", c.name, err)
			}
		})
	}
}

func TestClaim_TamperedStoredTicketRejected(t *testing.T) {
	dir := t.TempDir()
	keyB64, _ := genDeviceKey(t)
	ticket, err := Issue(dir, "issuer", "orig-name", 0)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	// Hand-edit the on-disk tickets file: change a signed field WITHOUT
	// re-signing (an attacker with file access cannot forge the box signature).
	path := ticketsPath(dir)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read tickets: %v", err)
	}
	var db map[string]Ticket
	if err := json.Unmarshal(raw, &db); err != nil {
		t.Fatalf("unmarshal tickets: %v", err)
	}
	tk := db[ticket.Token]
	tk.DeviceName = "attacker-injected"
	db[ticket.Token] = tk
	out, _ := json.MarshalIndent(db, "", "  ")
	if err := os.WriteFile(path, out, 0600); err != nil {
		t.Fatalf("write tickets: %v", err)
	}

	if _, err := Claim(dir, ticket.Token, "", keyB64); err != ErrTampered {
		t.Fatalf("Claim of a hand-edited ticket = %v, want ErrTampered", err)
	}
	// The corrupt entry is dropped; a retry is unknown.
	if _, err := Claim(dir, ticket.Token, "", keyB64); err != ErrInvalid {
		t.Fatalf("retry after tamper = %v, want ErrInvalid", err)
	}
}

// ─── Self-pair refusal (the pairing analogue of self-vouch exclusion) ──────────

func TestClaim_SelfPairRefused(t *testing.T) {
	dir := t.TempDir()
	// The claiming device's own fingerprint is stamped as the issuer, i.e. the
	// device is trying to approve itself.
	keyB64, fp := genDeviceKey(t)
	ticket, err := Issue(dir, fp, "", 0)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if _, err := Claim(dir, ticket.Token, "", keyB64); err != ErrSelfPair {
		t.Fatalf("self-pair Claim = %v, want ErrSelfPair", err)
	}
	// A self-pair must NOT burn the ticket: a genuinely different device can
	// still redeem it.
	otherB64, otherFP := genDeviceKey(t)
	res, err := Claim(dir, ticket.Token, "", otherB64)
	if err != nil {
		t.Fatalf("legitimate Claim after refused self-pair: %v", err)
	}
	if res.DeviceID != otherFP {
		t.Errorf("enrolled the wrong device: got %q want %q", res.DeviceID, otherFP)
	}
}

// ─── Malformed device key ─────────────────────────────────────────────────────────

func TestClaim_BadDeviceKeyRejected(t *testing.T) {
	dir := t.TempDir()
	ticket, err := Issue(dir, "issuer", "", 0)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	shortKey := base64.StdEncoding.EncodeToString([]byte("too-short"))
	for _, bad := range []string{"", "   ", "!!!not base64!!!", shortKey} {
		if _, err := Claim(dir, ticket.Token, "", bad); err != ErrBadDeviceKey {
			t.Fatalf("Claim(pubkey=%q) = %v, want ErrBadDeviceKey", bad, err)
		}
	}
	// A malformed key must not consume the ticket — a good key can still claim.
	goodB64, _ := genDeviceKey(t)
	if _, err := Claim(dir, ticket.Token, "", goodB64); err != nil {
		t.Fatalf("Claim after malformed attempts: %v", err)
	}
}

// ─── No storage secret ever leaves the box ────────────────────────────────────────

func TestClaim_NeverReturnsStorageSecrets(t *testing.T) {
	dir := t.TempDir()
	// A realistic storage.json carrying the raw account secrets.
	storage := map[string]any{
		"bucket":     "my-fleet-bucket",
		"region":     "eu-west-1",
		"endpoint":   "https://s3.example.com",
		"use_ssl":    true,
		"access_key": "AKIA_SUPER_SECRET_ACCESS",
		"secret_key": "sk_this_must_never_leave_the_box",
	}
	sd, _ := json.Marshal(storage)
	if err := os.WriteFile(storagePath(dir), sd, 0600); err != nil {
		t.Fatalf("write storage.json: %v", err)
	}

	keyB64, _ := genDeviceKey(t)
	ticket, err := Issue(dir, "issuer", "", 0)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	res, err := Claim(dir, ticket.Token, "", keyB64)
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	// Non-secret hints ARE surfaced (so the device can find the bucket)...
	if res.Hints.Bucket != "my-fleet-bucket" || res.Hints.Region != "eu-west-1" {
		t.Errorf("expected non-secret connection hints, got %+v", res.Hints)
	}
	// ...but the raw account secrets must never appear anywhere in the result,
	// nor in the QR payload of the ticket.
	blob, _ := json.Marshal(res)
	for _, secret := range []string{"AKIA_SUPER_SECRET_ACCESS", "sk_this_must_never_leave_the_box", "access_key", "secret_key"} {
		if strings.Contains(string(blob), secret) {
			t.Fatalf("claim result leaked storage secret material: %q present in %s", secret, blob)
		}
	}
	qr := QRPayload(ticket, "https://s3.example.com", "box-1")
	for _, secret := range []string{"AKIA_SUPER_SECRET_ACCESS", "sk_this_must_never_leave_the_box"} {
		if strings.Contains(qr, secret) {
			t.Fatalf("QR payload leaked storage secret: %q", secret)
		}
	}
}

// ─── QR round-trip ───────────────────────────────────────────────────────────────

func TestQRPayload_RoundTripsAndVerifies(t *testing.T) {
	dir := t.TempDir()
	ticket, err := Issue(dir, "issuer-xyz", "Tablet", 0)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	payload := QRPayload(ticket, "https://box.example", "box-42")
	if !strings.HasPrefix(payload, "vulos://pair/v1?") {
		t.Fatalf("unexpected QR scheme: %q", payload)
	}
	parsed, err := ParseQR(payload)
	if err != nil {
		t.Fatalf("ParseQR: %v", err)
	}
	if parsed.Token != ticket.Token || parsed.Sig != ticket.Sig || parsed.IssuerDeviceID != ticket.IssuerDeviceID {
		t.Fatal("parsed ticket does not match the issued ticket")
	}
	if err := VerifyTicket(dir, parsed); err != nil {
		t.Fatalf("a ticket parsed from a genuine QR must verify: %v", err)
	}
	// A tampered QR (flip the issuer) must fail verification.
	bad := strings.Replace(payload, "issuer-xyz", "attacker", 1)
	badTicket, err := ParseQR(bad)
	if err != nil {
		t.Fatalf("ParseQR(tampered): %v", err)
	}
	if err := VerifyTicket(dir, badTicket); err != ErrTampered {
		t.Fatalf("tampered QR VerifyTicket = %v, want ErrTampered", err)
	}
}

func TestParseQR_RejectsWrongScheme(t *testing.T) {
	for _, bad := range []string{"https://evil.example/pair", "vulos://join/v1?token=x", "not a url at all %%%"} {
		if _, err := ParseQR(bad); err == nil {
			t.Fatalf("ParseQR(%q) should have failed", bad)
		}
	}
}

// ─── Authority key stability ─────────────────────────────────────────────────────

func TestAuthorityPublicKey_StableAcrossCalls(t *testing.T) {
	dir := t.TempDir()
	a, err := AuthorityPublicKey(dir)
	if err != nil {
		t.Fatalf("AuthorityPublicKey: %v", err)
	}
	b, err := AuthorityPublicKey(dir)
	if err != nil {
		t.Fatalf("AuthorityPublicKey (2): %v", err)
	}
	if a == "" || a != b {
		t.Fatalf("authority key not stable: %q vs %q", a, b)
	}
	// Persisted: a fresh process (new call, same dir) reuses the same key file.
	if _, err := os.Stat(authKeyPath(dir)); err != nil {
		t.Fatalf("authority key not persisted: %v", err)
	}
}

// ─── Device management surface ────────────────────────────────────────────────────

func TestDeviceManagement_ListMarkRevokedForget(t *testing.T) {
	dir := t.TempDir()
	k1, fp1 := genDeviceKey(t)
	k2, fp2 := genDeviceKey(t)

	for _, k := range []string{k1, k2} {
		tk, err := Issue(dir, "issuer", "", 0)
		if err != nil {
			t.Fatalf("Issue: %v", err)
		}
		if _, err := Claim(dir, tk.Token, "", k); err != nil {
			t.Fatalf("Claim: %v", err)
		}
	}

	devices, err := ListPairedDevices(dir)
	if err != nil || len(devices) != 2 {
		t.Fatalf("ListPairedDevices: n=%d err=%v", len(devices), err)
	}

	// MarkRevoked flips the flag; idempotent; unknown device errors.
	if err := MarkRevoked(dir, fp1); err != nil {
		t.Fatalf("MarkRevoked: %v", err)
	}
	if err := MarkRevoked(dir, fp1); err != nil {
		t.Fatalf("MarkRevoked (idempotent): %v", err)
	}
	if err := MarkRevoked(dir, "no-such-device"); err != ErrUnknownDevice {
		t.Fatalf("MarkRevoked(unknown) = %v, want ErrUnknownDevice", err)
	}
	dev, ok, _ := GetPairedDevice(dir, fp1)
	if !ok || !dev.Revoked || dev.RevokedAt == nil {
		t.Fatalf("device %s should be marked revoked, got %+v", fp1, dev)
	}

	// Forget removes only the local record.
	removed, err := ForgetPairedDevice(dir, fp2)
	if err != nil || !removed {
		t.Fatalf("ForgetPairedDevice: removed=%v err=%v", removed, err)
	}
	if _, ok, _ := GetPairedDevice(dir, fp2); ok {
		t.Fatalf("device %s should be gone after Forget", fp2)
	}
	if removed, _ := ForgetPairedDevice(dir, "no-such-device"); removed {
		t.Fatal("ForgetPairedDevice(unknown) should report not-removed")
	}
}

// ─── Concurrent single-use (run under -race) ───────────────────────────────────────

func TestClaim_ConcurrentSingleUse(t *testing.T) {
	dir := t.TempDir()
	ticket, err := Issue(dir, "issuer", "", 0)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	const n = 8
	keys := make([]string, n) // generate keys up front: t.Fatalf must not run in a goroutine
	for i := range keys {
		keys[i], _ = genDeviceKey(t)
	}

	var wg sync.WaitGroup
	var mu sync.Mutex
	successes := 0
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(keyB64 string) {
			defer wg.Done()
			if _, err := Claim(dir, ticket.Token, "", keyB64); err == nil {
				mu.Lock()
				successes++
				mu.Unlock()
			}
		}(keys[i])
	}
	wg.Wait()
	if successes != 1 {
		t.Fatalf("a single-use ticket was claimed %d times, want exactly 1", successes)
	}
}
