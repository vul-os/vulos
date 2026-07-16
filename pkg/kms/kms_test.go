// kms_test.go — unit tests for COMPLY-04 BYO KMS package.
//
// Tests use only MemStore and SymmetricProvider (pure-Go, no I/O).
// SQL smoke-test uses an in-memory SQLite database via cpdb.
package kms

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/vul-os/vulos-management/pkg/cpdb"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func testKEK(t *testing.T) []byte {
	t.Helper()
	kek := make([]byte, 32)
	for i := range kek {
		kek[i] = byte(i + 1)
	}
	return kek
}

func testKey(t *testing.T) []byte {
	t.Helper()
	k := make([]byte, 32)
	for i := range k {
		k[i] = byte(0xab)
	}
	return k
}

func testSymProvider(t *testing.T) *SymmetricProvider {
	t.Helper()
	p, err := NewSymmetricProvider(testKey(t))
	if err != nil {
		t.Fatalf("NewSymmetricProvider: %v", err)
	}
	return p
}

// ---------------------------------------------------------------------------
// DEK generation
// ---------------------------------------------------------------------------

func TestGenerateDEK_length(t *testing.T) {
	dek, err := GenerateDEK()
	if err != nil {
		t.Fatalf("GenerateDEK: %v", err)
	}
	if len(dek) != 32 {
		t.Fatalf("expected 32 bytes, got %d", len(dek))
	}
}

func TestGenerateDEK_unique(t *testing.T) {
	a, _ := GenerateDEK()
	b, _ := GenerateDEK()
	if bytes.Equal(a, b) {
		t.Fatal("two DEKs must be distinct")
	}
}

// ---------------------------------------------------------------------------
// SymmetricProvider
// ---------------------------------------------------------------------------

func TestSymmetricProvider_WrapUnwrap(t *testing.T) {
	p := testSymProvider(t)
	ctx := context.Background()

	dek, err := GenerateDEK()
	if err != nil {
		t.Fatal(err)
	}
	wrapped, err := p.WrapDEK(ctx, dek)
	if err != nil {
		t.Fatalf("WrapDEK: %v", err)
	}
	got, err := p.UnwrapDEK(ctx, wrapped)
	if err != nil {
		t.Fatalf("UnwrapDEK: %v", err)
	}
	if !bytes.Equal(got, dek) {
		t.Fatal("unwrapped DEK does not match original")
	}
}

func TestSymmetricProvider_WrapIsNonDeterministic(t *testing.T) {
	p := testSymProvider(t)
	ctx := context.Background()
	dek, _ := GenerateDEK()

	w1, _ := p.WrapDEK(ctx, dek)
	w2, _ := p.WrapDEK(ctx, dek)
	// AES-GCM with random nonce: two wrappings of the same DEK must differ.
	if bytes.Equal(w1, w2) {
		t.Fatal("wrap must be non-deterministic (random nonce)")
	}
}

func TestSymmetricProvider_BadKey(t *testing.T) {
	_, err := NewSymmetricProvider([]byte("tooshort"))
	if err == nil {
		t.Fatal("expected error for short key")
	}
}

func TestSymmetricProvider_FromHex(t *testing.T) {
	p, err := NewSymmetricProviderFromHex(strings.Repeat("ab", 32))
	if err != nil {
		t.Fatalf("NewSymmetricProviderFromHex: %v", err)
	}
	ctx := context.Background()
	dek, _ := GenerateDEK()
	wrapped, err := p.WrapDEK(ctx, dek)
	if err != nil {
		t.Fatal(err)
	}
	got, err := p.UnwrapDEK(ctx, wrapped)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, dek) {
		t.Fatal("round-trip via hex key failed")
	}
}

// ---------------------------------------------------------------------------
// Envelope Encrypt / Decrypt
// ---------------------------------------------------------------------------

func TestEnvelope_RoundTrip(t *testing.T) {
	p := testSymProvider(t)
	ctx := context.Background()
	plain := []byte("vulos cannot read your data")

	ct, wrappedDEK, err := Encrypt(ctx, p, plain)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	got, err := Decrypt(ctx, p, ct, wrappedDEK)
	if err != nil {
		t.Fatalf("Decrypt: %v", err)
	}
	if !bytes.Equal(got, plain) {
		t.Fatalf("decrypted %q, want %q", got, plain)
	}
}

func TestEnvelope_CiphertextDiffersFromPlaintext(t *testing.T) {
	p := testSymProvider(t)
	ctx := context.Background()
	plain := []byte("sensitive customer data")

	ct, _, _ := Encrypt(ctx, p, plain)
	if strings.Contains(ct, string(plain)) {
		t.Fatal("ciphertext must not contain plaintext")
	}
}

func TestEnvelope_NonDeterministic(t *testing.T) {
	p := testSymProvider(t)
	ctx := context.Background()
	plain := []byte("same plaintext")

	ct1, _, _ := Encrypt(ctx, p, plain)
	ct2, _, _ := Encrypt(ctx, p, plain)
	if ct1 == ct2 {
		t.Fatal("two encryptions of same plaintext must produce different ciphertext")
	}
}

func TestEnvelope_TamperDetection(t *testing.T) {
	p := testSymProvider(t)
	ctx := context.Background()
	ct, wdek, _ := Encrypt(ctx, p, []byte("test"))
	// Tamper with one hex char.
	tampered := ct[:len(ct)-2] + "ff"
	if tampered == ct {
		tampered = ct[:len(ct)-2] + "00"
	}
	_, err := Decrypt(ctx, p, tampered, wdek)
	if err == nil {
		t.Fatal("expected error on tampered ciphertext")
	}
}

// ---------------------------------------------------------------------------
// MemStore
// ---------------------------------------------------------------------------

func TestMemStore_ConfigRoundTrip(t *testing.T) {
	m := NewMemStore()
	ctx := context.Background()

	_, err := m.GetConfig(ctx, "acct1")
	if err != ErrUnknownAccount {
		t.Fatalf("expected ErrUnknownAccount, got %v", err)
	}

	cfg := Config{
		AccountID:            "acct1",
		Tier:                 "pro",
		Kind:                 KindSymmetric,
		EncryptedKeyMaterial: "deadbeef",
		KEKVersion:           1,
	}
	if err := m.PutConfig(ctx, cfg); err != nil {
		t.Fatalf("PutConfig: %v", err)
	}
	got, err := m.GetConfig(ctx, "acct1")
	if err != nil {
		t.Fatalf("GetConfig: %v", err)
	}
	if got.AccountID != "acct1" || got.Tier != "pro" {
		t.Fatalf("unexpected config: %+v", got)
	}
}

func TestMemStore_DEKRoundTrip(t *testing.T) {
	m := NewMemStore()
	ctx := context.Background()

	d := DEKRecord{
		ID:         "dek-001",
		AccountID:  "acct1",
		ObjectRef:  "bucket/foo",
		WrappedDEK: []byte("wrappedblob"),
		KEKVersion: 1,
	}
	if err := m.PutDEK(ctx, d); err != nil {
		t.Fatalf("PutDEK: %v", err)
	}
	got, err := m.GetDEK(ctx, "dek-001")
	if err != nil {
		t.Fatalf("GetDEK: %v", err)
	}
	if got.AccountID != "acct1" || !bytes.Equal(got.WrappedDEK, d.WrappedDEK) {
		t.Fatalf("unexpected DEK: %+v", got)
	}
}

func TestMemStore_GetDEKByRef(t *testing.T) {
	m := NewMemStore()
	ctx := context.Background()

	m.PutDEK(ctx, DEKRecord{ID: "d1", AccountID: "a1", ObjectRef: "b/k", WrappedDEK: []byte("w1"), KEKVersion: 1})
	m.PutDEK(ctx, DEKRecord{ID: "d2", AccountID: "a1", ObjectRef: "b/k", WrappedDEK: []byte("w2"), KEKVersion: 1})

	got, err := m.GetDEKByRef(ctx, "a1", "b/k")
	if err != nil {
		t.Fatalf("GetDEKByRef: %v", err)
	}
	// Should return the most-recent; both have same time so just verify found.
	if got.AccountID != "a1" {
		t.Fatalf("unexpected account: %s", got.AccountID)
	}
}

func TestMemStore_RevokeDEK(t *testing.T) {
	m := NewMemStore()
	ctx := context.Background()

	m.PutDEK(ctx, DEKRecord{ID: "d1", AccountID: "a1", ObjectRef: "b/k", WrappedDEK: []byte("w")})
	if err := m.RevokeDEK(ctx, "d1"); err != nil {
		t.Fatalf("RevokeDEK: %v", err)
	}
	_, err := m.GetDEK(ctx, "d1")
	if err != ErrKeyRevoked {
		t.Fatalf("expected ErrKeyRevoked, got %v", err)
	}
}

func TestMemStore_UpdateWrappedDEK(t *testing.T) {
	m := NewMemStore()
	ctx := context.Background()

	m.PutDEK(ctx, DEKRecord{ID: "d1", AccountID: "a1", ObjectRef: "ref", WrappedDEK: []byte("old"), KEKVersion: 1})
	if err := m.UpdateWrappedDEK(ctx, "d1", []byte("new"), 2); err != nil {
		t.Fatalf("UpdateWrappedDEK: %v", err)
	}
	got, _ := m.GetDEK(ctx, "d1")
	if !bytes.Equal(got.WrappedDEK, []byte("new")) || got.KEKVersion != 2 {
		t.Fatalf("update not reflected: %+v", got)
	}
}

func TestMemStore_DeleteConfig_CascadesDEKs(t *testing.T) {
	m := NewMemStore()
	ctx := context.Background()

	m.PutConfig(ctx, Config{AccountID: "a1", Kind: KindSymmetric})
	m.PutDEK(ctx, DEKRecord{ID: "d1", AccountID: "a1", ObjectRef: "r"})
	m.PutDEK(ctx, DEKRecord{ID: "d2", AccountID: "a1", ObjectRef: "r2"})

	if err := m.DeleteConfig(ctx, "a1"); err != nil {
		t.Fatalf("DeleteConfig: %v", err)
	}
	_, err := m.GetConfig(ctx, "a1")
	if err != ErrUnknownAccount {
		t.Fatal("config should be gone")
	}
	deks, err := m.ListDEKs(ctx, "a1", true)
	if err != nil {
		t.Fatal(err)
	}
	if len(deks) != 0 {
		t.Fatalf("expected 0 DEKs after delete, got %d", len(deks))
	}
}

// ---------------------------------------------------------------------------
// EncryptKeyMaterial / DecryptKeyMaterial
// ---------------------------------------------------------------------------

func TestEncryptDecryptKeyMaterial(t *testing.T) {
	kek := testKEK(t)
	material := "customer-secret-token-xyz"

	enc, err := EncryptKeyMaterial(kek, material)
	if err != nil {
		t.Fatalf("EncryptKeyMaterial: %v", err)
	}
	if enc == material {
		t.Fatal("encrypted material must differ from plaintext")
	}
	dec, err := DecryptKeyMaterial(kek, enc)
	if err != nil {
		t.Fatalf("DecryptKeyMaterial: %v", err)
	}
	if dec != material {
		t.Fatalf("got %q, want %q", dec, material)
	}
}

func TestKEKFromHex(t *testing.T) {
	kek, err := KEKFromHex(strings.Repeat("01", 32))
	if err != nil {
		t.Fatal(err)
	}
	if len(kek) != 32 {
		t.Fatal("expected 32 bytes")
	}
	// Empty is dev mode, not an error.
	kek2, err := KEKFromHex("")
	if err != nil || kek2 != nil {
		t.Fatalf("empty KEK should return nil, nil; got %v %v", kek2, err)
	}
}

// ---------------------------------------------------------------------------
// Service — tier gate
// ---------------------------------------------------------------------------

type mockTierSource struct{ tier string }

func (m *mockTierSource) AccountTier(_ context.Context, _ string) (string, error) {
	return m.tier, nil
}

func TestService_TierGate_Free(t *testing.T) {
	svc := &Service{
		Store: NewMemStore(),
		KEK:   testKEK(t),
		Tiers: &mockTierSource{tier: "free"},
	}
	ctx := context.Background()

	err := svc.ConfigureKMS(ctx, "acct-free", "free", KindSymmetric, "", strings.Repeat("aa", 32))
	if err != ErrTierRequired {
		t.Fatalf("expected ErrTierRequired for free tier, got %v", err)
	}
}

func TestService_TierGate_Pro(t *testing.T) {
	svc := &Service{
		Store: NewMemStore(),
		KEK:   testKEK(t),
		Tiers: &mockTierSource{tier: "pro"},
	}
	ctx := context.Background()

	err := svc.ConfigureKMS(ctx, "acct-pro", "pro", KindSymmetric, "", strings.Repeat("ab", 32))
	if err != nil {
		t.Fatalf("expected success for pro tier, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// Service — NewEncryptedDEK + DecryptWithDEK round-trip
// ---------------------------------------------------------------------------

func TestService_EncryptDecrypt_RoundTrip(t *testing.T) {
	svc := &Service{
		Store: NewMemStore(),
		KEK:   testKEK(t),
		Tiers: &mockTierSource{tier: "pro"},
	}
	ctx := context.Background()

	keyHex := strings.Repeat("cd", 32)
	if err := svc.ConfigureKMS(ctx, "acct1", "pro", KindSymmetric, "", keyHex); err != nil {
		t.Fatalf("ConfigureKMS: %v", err)
	}

	plain := []byte("vulos cannot read your data — COMPLY-04")
	ct, dekID, err := svc.NewEncryptedDEK(ctx, "acct1", "bucket/obj", plain)
	if err != nil {
		t.Fatalf("NewEncryptedDEK: %v", err)
	}
	if ct == "" || dekID == "" {
		t.Fatal("empty ct or dekID")
	}

	got, err := svc.DecryptWithDEK(ctx, "acct1", dekID, ct)
	if err != nil {
		t.Fatalf("DecryptWithDEK: %v", err)
	}
	if !bytes.Equal(got, plain) {
		t.Fatalf("decrypted %q, want %q", got, plain)
	}
}

func TestService_DecryptWithDEK_WrongAccount(t *testing.T) {
	svc := &Service{
		Store: NewMemStore(),
		Tiers: nil, // dev mode
	}
	ctx := context.Background()

	p := testSymProvider(t)
	ct, wdek, _ := Encrypt(ctx, p, []byte("data"))
	_ = ct
	svc.Store.PutDEK(ctx, DEKRecord{
		ID:         "dek1",
		AccountID:  "acct-owner",
		ObjectRef:  "ref",
		WrappedDEK: wdek,
		KEKVersion: 1,
	})
	// acct-other must not be able to decrypt acct-owner's DEK.
	_, err := svc.DecryptWithDEK(ctx, "acct-other", "dek1", ct)
	if err != ErrUnknownKey {
		t.Fatalf("expected ErrUnknownKey for wrong account, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// Service — RotateKEK
// ---------------------------------------------------------------------------

func TestService_RotateKEK(t *testing.T) {
	ctx := context.Background()
	m := NewMemStore()

	// Two different symmetric keys (old and new).
	oldKey := make([]byte, 32)
	for i := range oldKey {
		oldKey[i] = 0x11
	}
	newKey := make([]byte, 32)
	for i := range newKey {
		newKey[i] = 0x22
	}
	oldP, _ := NewSymmetricProvider(oldKey)
	newP, _ := NewSymmetricProvider(newKey)

	// Store DEKs wrapped under oldP.
	for i := 0; i < 3; i++ {
		dek, _ := GenerateDEK()
		wdek, _ := oldP.WrapDEK(ctx, dek)
		m.PutDEK(ctx, DEKRecord{
			ID:         "dek-" + string(rune('0'+i)),
			AccountID:  "a1",
			ObjectRef:  "ref",
			WrappedDEK: wdek,
			KEKVersion: 1,
		})
	}

	svc := &Service{Store: m}
	result, err := svc.RotateKEK(ctx, "a1", oldP, newP, 2)
	if err != nil {
		t.Fatalf("RotateKEK: %v", err)
	}
	if result.Rotated != 3 {
		t.Fatalf("expected 3 rotated, got %d (failed=%d, skipped=%d)",
			result.Rotated, result.Failed, result.Skipped)
	}

	// Verify each DEK can now be unwrapped with newP (not oldP).
	deks, _ := m.ListDEKs(ctx, "a1", false)
	for _, d := range deks {
		if d.KEKVersion != 2 {
			t.Errorf("DEK %s has version %d, want 2", d.ID, d.KEKVersion)
		}
		plain, err := newP.UnwrapDEK(ctx, d.WrappedDEK)
		if err != nil {
			t.Errorf("DEK %s: UnwrapDEK with new key: %v", d.ID, err)
		}
		if len(plain) != 32 {
			t.Errorf("DEK %s: expected 32-byte plain DEK", d.ID)
		}
	}
}

func TestService_RotateKEK_SkipsAlreadyAtVersion(t *testing.T) {
	ctx := context.Background()
	m := NewMemStore()
	p, _ := NewSymmetricProvider(make([]byte, 32))

	dek, _ := GenerateDEK()
	wdek, _ := p.WrapDEK(ctx, dek)
	m.PutDEK(ctx, DEKRecord{
		ID:         "d1",
		AccountID:  "a1",
		ObjectRef:  "r",
		WrappedDEK: wdek,
		KEKVersion: 2, // already at new version
	})

	svc := &Service{Store: m}
	res, err := svc.RotateKEK(ctx, "a1", p, p, 2)
	if err != nil {
		t.Fatal(err)
	}
	if res.Skipped != 1 || res.Rotated != 0 {
		t.Fatalf("expected 1 skipped, got rotated=%d skipped=%d", res.Rotated, res.Skipped)
	}
}

// ---------------------------------------------------------------------------
// SQLStore smoke test
// ---------------------------------------------------------------------------

func TestSQLStore_BasicSmoke(t *testing.T) {
	db, err := cpdb.OpenSQLiteDSN(":memory:")
	if err != nil {
		t.Fatalf("cpdb.OpenSQLiteDSN: %v", err)
	}
	s, err := Open(db)
	if err != nil {
		db.Close()
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	ctx := context.Background()

	cfg := Config{
		AccountID:            "sql-acct",
		Tier:                 "pro",
		Kind:                 KindSymmetric,
		EncryptedKeyMaterial: "deadbeef",
		KEKVersion:           1,
	}
	if err := s.PutConfig(ctx, cfg); err != nil {
		t.Fatalf("PutConfig: %v", err)
	}
	got, err := s.GetConfig(ctx, "sql-acct")
	if err != nil {
		t.Fatalf("GetConfig: %v", err)
	}
	if got.Tier != "pro" {
		t.Fatalf("tier: got %q, want pro", got.Tier)
	}

	d := DEKRecord{
		ID:         "dek-sql-1",
		AccountID:  "sql-acct",
		ObjectRef:  "bucket/key",
		WrappedDEK: []byte("wrappedsqlblob"),
		KEKVersion: 1,
	}
	if err := s.PutDEK(ctx, d); err != nil {
		t.Fatalf("PutDEK: %v", err)
	}
	gotDEK, err := s.GetDEK(ctx, "dek-sql-1")
	if err != nil {
		t.Fatalf("GetDEK: %v", err)
	}
	if !bytes.Equal(gotDEK.WrappedDEK, d.WrappedDEK) {
		t.Fatal("wrapped DEK mismatch")
	}

	// Revoke and verify.
	if err := s.RevokeDEK(ctx, "dek-sql-1"); err != nil {
		t.Fatalf("RevokeDEK: %v", err)
	}
	_, err = s.GetDEK(ctx, "dek-sql-1")
	if err != ErrKeyRevoked {
		t.Fatalf("expected ErrKeyRevoked, got %v", err)
	}
}
