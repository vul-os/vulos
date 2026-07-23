package enroll

import (
	"context"
	"strings"
	"testing"
	"time"
)

// ─────────────────────────────────────────────────────────────────────────────
// Helpers
// ─────────────────────────────────────────────────────────────────────────────

func makeGrant(ttl time.Duration) EnrollGrant {
	now := time.Now().UTC()
	return EnrollGrant{
		DeviceCode:      genDeviceCode(),
		UserCode:        GenUserCode(),
		VerificationURI: "https://vulos.org/activate",
		Interval:        5,
		ExpiresIn:       int(ttl.Seconds()),
		ExpiresAt:       now.Add(ttl),
	}
}

// freshPubKey returns a valid 32-byte ed25519 public key from a MemCertSigner.
func freshPubKey(t *testing.T) []byte {
	t.Helper()
	s := NewMemCertSigner()
	return s.PublicKeyBytes()
}

// ─────────────────────────────────────────────────────────────────────────────
// AC: StartGrant → ApproveGrant → PollGrant returns EnrollResult
// ─────────────────────────────────────────────────────────────────────────────

func TestMemStore_HappyPath(t *testing.T) {
	ctx := context.Background()
	store := NewMemStore()
	signer := NewMemCertSigner()

	pubKey := signer.PublicKeyBytes()
	g := makeGrant(15 * time.Minute)

	// StartGrant
	if err := store.StartGrant(ctx, g, pubKey); err != nil {
		t.Fatalf("StartGrant: %v", err)
	}

	// PollGrant before approve → ErrAuthPending
	_, err := store.PollGrant(ctx, g.DeviceCode)
	if err != ErrAuthPending {
		t.Fatalf("PollGrant before approve: want ErrAuthPending, got %v", err)
	}

	// Approve: sign a cert
	const accountID = "01ACCOUNTULID0000000000000"
	const ulid = "01ARZ3NDEKTSV4RRFFQ69G5FAV"
	cert, err := signer.SignDeviceCert(pubKey, accountID, ulid)
	if err != nil {
		t.Fatalf("SignDeviceCert: %v", err)
	}
	if err := store.ApproveGrant(ctx, g.UserCode, accountID, ulid, cert); err != nil {
		t.Fatalf("ApproveGrant: %v", err)
	}

	// PollGrant after approve → EnrollResult
	result, err := store.PollGrant(ctx, g.DeviceCode)
	if err != nil {
		t.Fatalf("PollGrant after approve: %v", err)
	}
	if result.ULID != ulid {
		t.Errorf("ULID: want %q, got %q", ulid, result.ULID)
	}
	if result.AccountID != accountID {
		t.Errorf("AccountID: want %q, got %q", accountID, result.AccountID)
	}
	if len(result.DeviceCert) == 0 {
		t.Error("DeviceCert is empty")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// AC: PollGrant before approve → ErrAuthPending (standalone)
// ─────────────────────────────────────────────────────────────────────────────

func TestMemStore_PollBeforeApprove(t *testing.T) {
	ctx := context.Background()
	store := NewMemStore()
	g := makeGrant(15 * time.Minute)

	if err := store.StartGrant(ctx, g, freshPubKey(t)); err != nil {
		t.Fatalf("StartGrant: %v", err)
	}
	_, err := store.PollGrant(ctx, g.DeviceCode)
	if err != ErrAuthPending {
		t.Fatalf("want ErrAuthPending, got %v", err)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// AC: ErrExpired on expired grant
// ─────────────────────────────────────────────────────────────────────────────

func TestMemStore_Expired(t *testing.T) {
	ctx := context.Background()
	store := NewMemStore()
	// Grant that expired 1 second ago.
	g := makeGrant(-1 * time.Second)

	if err := store.StartGrant(ctx, g, freshPubKey(t)); err != nil {
		t.Fatalf("StartGrant: %v", err)
	}
	_, err := store.PollGrant(ctx, g.DeviceCode)
	if err != ErrExpired {
		t.Fatalf("want ErrExpired, got %v", err)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// AC: ErrAlreadyConsumed after first successful poll
// ─────────────────────────────────────────────────────────────────────────────

func TestMemStore_AlreadyConsumed(t *testing.T) {
	ctx := context.Background()
	store := NewMemStore()
	signer := NewMemCertSigner()
	pubKey := signer.PublicKeyBytes()
	g := makeGrant(15 * time.Minute)

	if err := store.StartGrant(ctx, g, pubKey); err != nil {
		t.Fatalf("StartGrant: %v", err)
	}

	const accountID = "01ACCOUNTULID0000000000000"
	const ulid = "01ARZ3NDEKTSV4RRFFQ69G5FAV"
	cert, _ := signer.SignDeviceCert(pubKey, accountID, ulid)
	if err := store.ApproveGrant(ctx, g.UserCode, accountID, ulid, cert); err != nil {
		t.Fatalf("ApproveGrant: %v", err)
	}

	// First poll succeeds.
	if _, err := store.PollGrant(ctx, g.DeviceCode); err != nil {
		t.Fatalf("first PollGrant: %v", err)
	}
	// Second poll → ErrAlreadyConsumed.
	_, err := store.PollGrant(ctx, g.DeviceCode)
	if err != ErrAlreadyConsumed {
		t.Fatalf("second PollGrant: want ErrAlreadyConsumed, got %v", err)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// AC: DenyGrant → PollGrant returns ErrAccessDenied
// ─────────────────────────────────────────────────────────────────────────────

func TestMemStore_Deny(t *testing.T) {
	ctx := context.Background()
	store := NewMemStore()
	g := makeGrant(15 * time.Minute)

	if err := store.StartGrant(ctx, g, freshPubKey(t)); err != nil {
		t.Fatalf("StartGrant: %v", err)
	}
	if err := store.DenyGrant(ctx, g.UserCode); err != nil {
		t.Fatalf("DenyGrant: %v", err)
	}
	_, err := store.PollGrant(ctx, g.DeviceCode)
	if err != ErrAccessDenied {
		t.Fatalf("want ErrAccessDenied, got %v", err)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// AC: ErrUnknownDeviceCode / ErrUnknownUserCode
// ─────────────────────────────────────────────────────────────────────────────

func TestMemStore_UnknownCodes(t *testing.T) {
	ctx := context.Background()
	store := NewMemStore()

	_, err := store.PollGrant(ctx, "nosuchcode")
	if err != ErrUnknownDeviceCode {
		t.Errorf("PollGrant unknown: want ErrUnknownDeviceCode, got %v", err)
	}

	err = store.ApproveGrant(ctx, "XXXX-9999", "acc", "ulid", nil)
	if err != ErrUnknownUserCode {
		t.Errorf("ApproveGrant unknown: want ErrUnknownUserCode, got %v", err)
	}

	err = store.DenyGrant(ctx, "XXXX-9999")
	if err != ErrUnknownUserCode {
		t.Errorf("DenyGrant unknown: want ErrUnknownUserCode, got %v", err)
	}

	_, err = store.GetByUserCode(ctx, "XXXX-9999")
	if err != ErrUnknownUserCode {
		t.Errorf("GetByUserCode unknown: want ErrUnknownUserCode, got %v", err)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// AC: ErrInvalidPubKey
// ─────────────────────────────────────────────────────────────────────────────

func TestMemStore_InvalidPubKey(t *testing.T) {
	ctx := context.Background()
	store := NewMemStore()
	g := makeGrant(15 * time.Minute)

	err := store.StartGrant(ctx, g, []byte("tooshort"))
	if err != ErrInvalidPubKey {
		t.Fatalf("want ErrInvalidPubKey, got %v", err)
	}
}

func TestMemCertSigner_InvalidPubKey(t *testing.T) {
	signer := NewMemCertSigner()
	_, err := signer.SignDeviceCert([]byte("bad"), "acc", "ulid")
	if err != ErrInvalidPubKey {
		t.Fatalf("want ErrInvalidPubKey, got %v", err)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// AC: GenUserCode 100 samples have no ambiguous chars (0 O 1 l I)
// ─────────────────────────────────────────────────────────────────────────────

func TestGenUserCode_NoAmbiguousChars(t *testing.T) {
	ambiguous := "0O1lI"
	for i := 0; i < 100; i++ {
		code := GenUserCode()
		// Format: XXXX-XXXX (9 chars)
		if len(code) != 9 {
			t.Errorf("sample %d: unexpected length %d: %q", i, len(code), code)
			continue
		}
		if code[4] != '-' {
			t.Errorf("sample %d: missing dash separator: %q", i, code)
		}
		for _, ch := range code {
			if ch == '-' {
				continue
			}
			if strings.ContainsRune(ambiguous, ch) {
				t.Errorf("sample %d: ambiguous char %q in code %q", i, ch, code)
			}
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// AC: GetByUserCode
// ─────────────────────────────────────────────────────────────────────────────

func TestMemStore_GetByUserCode(t *testing.T) {
	ctx := context.Background()
	store := NewMemStore()
	g := makeGrant(15 * time.Minute)

	if err := store.StartGrant(ctx, g, freshPubKey(t)); err != nil {
		t.Fatalf("StartGrant: %v", err)
	}
	got, err := store.GetByUserCode(ctx, g.UserCode)
	if err != nil {
		t.Fatalf("GetByUserCode: %v", err)
	}
	if got.UserCode != g.UserCode {
		t.Errorf("UserCode: want %q, got %q", g.UserCode, got.UserCode)
	}
	if got.DeviceCode != g.DeviceCode {
		t.Errorf("DeviceCode: want %q, got %q", g.DeviceCode, got.DeviceCode)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// AC: ApproveGrant on expired grant → ErrExpired
// ─────────────────────────────────────────────────────────────────────────────

func TestMemStore_ApproveExpired(t *testing.T) {
	ctx := context.Background()
	store := NewMemStore()
	g := makeGrant(-1 * time.Second)

	if err := store.StartGrant(ctx, g, freshPubKey(t)); err != nil {
		t.Fatalf("StartGrant: %v", err)
	}
	err := store.ApproveGrant(ctx, g.UserCode, "acc", "ulid", nil)
	if err != ErrExpired {
		t.Fatalf("want ErrExpired, got %v", err)
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Compile-time interface assertions (belt-and-suspenders, beyond var _ checks)
// ─────────────────────────────────────────────────────────────────────────────

func TestInterfaceConformance(t *testing.T) {
	var _ Store = (*MemStore)(nil)
	var _ CertSigner = (*MemCertSigner)(nil)
}
