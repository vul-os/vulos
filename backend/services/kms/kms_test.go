package kms

import (
	"context"
	"encoding/hex"
	"testing"
)

func testStorageKEK() []byte {
	return make([]byte, 32) // all-zeros dev key, same posture as devFallbackStorageKEK
}

func newTestService(t *testing.T) *Service {
	t.Helper()
	store, err := OpenStore("")
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return NewService(store, testStorageKEK())
}

// ---------------------------------------------------------------------------
// Envelope crypto (SymmetricProvider round trip)
// ---------------------------------------------------------------------------

func TestEnvelope_EncryptDecrypt_RoundTrip(t *testing.T) {
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}
	p, err := NewSymmetricProvider(key)
	if err != nil {
		t.Fatalf("NewSymmetricProvider: %v", err)
	}
	ctx := context.Background()
	plaintext := []byte("the box cannot read this without the owner's KEK")

	ciphertext, wrapped, err := Encrypt(ctx, p, plaintext)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	if ciphertext == "" || len(wrapped) == 0 {
		t.Fatalf("Encrypt produced empty output")
	}

	got, err := Decrypt(ctx, p, ciphertext, wrapped)
	if err != nil {
		t.Fatalf("Decrypt: %v", err)
	}
	if string(got) != string(plaintext) {
		t.Fatalf("round trip mismatch: got %q want %q", got, plaintext)
	}
}

func TestEnvelope_Decrypt_WrongKeyFailsClosed(t *testing.T) {
	key1 := make([]byte, 32)
	key2 := make([]byte, 32)
	key2[0] = 1
	p1, _ := NewSymmetricProvider(key1)
	p2, _ := NewSymmetricProvider(key2)
	ctx := context.Background()

	ciphertext, wrapped, err := Encrypt(ctx, p1, []byte("secret"))
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	if _, err := Decrypt(ctx, p2, ciphertext, wrapped); err == nil {
		t.Fatalf("Decrypt with wrong provider must fail, got nil error")
	}
}

func TestNewSymmetricProviderFromHex_RejectsWrongLength(t *testing.T) {
	if _, err := NewSymmetricProviderFromHex(hex.EncodeToString([]byte("too-short"))); err == nil {
		t.Fatalf("expected error for non-32-byte key")
	}
}

// ---------------------------------------------------------------------------
// SQLStore CRUD
// ---------------------------------------------------------------------------

func TestSQLStore_ConfigCRUD(t *testing.T) {
	ctx := context.Background()
	store, err := OpenStore("")
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer store.Close()

	if _, err := store.GetConfig(ctx); err != ErrNotConfigured {
		t.Fatalf("GetConfig before PutConfig: got %v, want ErrNotConfigured", err)
	}

	cfg := Config{Kind: KindSymmetric, EncryptedKeyMaterial: "deadbeef", KEKVersion: 1}
	if err := store.PutConfig(ctx, cfg); err != nil {
		t.Fatalf("PutConfig: %v", err)
	}
	got, err := store.GetConfig(ctx)
	if err != nil {
		t.Fatalf("GetConfig: %v", err)
	}
	if got.Kind != KindSymmetric || got.EncryptedKeyMaterial != "deadbeef" || got.KEKVersion != 1 {
		t.Fatalf("GetConfig mismatch: %+v", got)
	}

	// Upsert (rotation persists over the singleton row).
	cfg2 := Config{Kind: KindHTTP, Endpoint: "https://vault.example", KEKVersion: 2}
	if err := store.PutConfig(ctx, cfg2); err != nil {
		t.Fatalf("PutConfig (upsert): %v", err)
	}
	got2, err := store.GetConfig(ctx)
	if err != nil {
		t.Fatalf("GetConfig after upsert: %v", err)
	}
	if got2.Kind != KindHTTP || got2.Endpoint != "https://vault.example" || got2.KEKVersion != 2 {
		t.Fatalf("GetConfig after upsert mismatch: %+v", got2)
	}

	if err := store.DeleteConfig(ctx); err != nil {
		t.Fatalf("DeleteConfig: %v", err)
	}
	if _, err := store.GetConfig(ctx); err != ErrNotConfigured {
		t.Fatalf("GetConfig after delete: got %v, want ErrNotConfigured", err)
	}
}

func TestSQLStore_DEKLifecycle(t *testing.T) {
	ctx := context.Background()
	store, err := OpenStore("")
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer store.Close()

	d := DEKRecord{ID: "dek-1", ObjectRef: "files/a.txt", WrappedDEK: []byte("wrapped"), KEKVersion: 1}
	if err := store.PutDEK(ctx, d); err != nil {
		t.Fatalf("PutDEK: %v", err)
	}

	got, err := store.GetDEK(ctx, "dek-1")
	if err != nil {
		t.Fatalf("GetDEK: %v", err)
	}
	if got.ObjectRef != "files/a.txt" || string(got.WrappedDEK) != "wrapped" {
		t.Fatalf("GetDEK mismatch: %+v", got)
	}

	byRef, err := store.GetDEKByRef(ctx, "files/a.txt")
	if err != nil || byRef.ID != "dek-1" {
		t.Fatalf("GetDEKByRef: %v %+v", err, byRef)
	}

	if err := store.UpdateWrappedDEK(ctx, "dek-1", []byte("rewrapped"), 2); err != nil {
		t.Fatalf("UpdateWrappedDEK: %v", err)
	}
	got2, _ := store.GetDEK(ctx, "dek-1")
	if string(got2.WrappedDEK) != "rewrapped" || got2.KEKVersion != 2 {
		t.Fatalf("UpdateWrappedDEK did not persist: %+v", got2)
	}

	if err := store.RevokeDEK(ctx, "dek-1"); err != nil {
		t.Fatalf("RevokeDEK: %v", err)
	}
	if _, err := store.GetDEK(ctx, "dek-1"); err != ErrKeyRevoked {
		t.Fatalf("GetDEK after revoke: got %v, want ErrKeyRevoked", err)
	}

	list, err := store.ListDEKs(ctx, false)
	if err != nil {
		t.Fatalf("ListDEKs: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("ListDEKs(includeRevoked=false) should exclude revoked, got %d", len(list))
	}
	listAll, err := store.ListDEKs(ctx, true)
	if err != nil || len(listAll) != 1 {
		t.Fatalf("ListDEKs(includeRevoked=true): %v len=%d", err, len(listAll))
	}

	if _, err := store.GetDEK(ctx, "nonexistent"); err != ErrUnknownKey {
		t.Fatalf("GetDEK unknown: got %v, want ErrUnknownKey", err)
	}
}

// ---------------------------------------------------------------------------
// Service: register → wrap → unwrap → rotate → revoke
// ---------------------------------------------------------------------------

func TestService_ConfigureThenAlreadyConfigured(t *testing.T) {
	ctx := context.Background()
	svc := newTestService(t)

	st, err := svc.Status(ctx)
	if err != nil || st.Configured {
		t.Fatalf("Status before Configure: %+v err=%v", st, err)
	}

	key := hex.EncodeToString(make([]byte, 32))
	cfg, generated, err := svc.Configure(ctx, KindSymmetric, "", key)
	if err != nil {
		t.Fatalf("Configure: %v", err)
	}
	if generated != "" {
		t.Fatalf("Configure with explicit key material must not generate one")
	}
	if cfg.Kind != KindSymmetric || cfg.KEKVersion != 1 {
		t.Fatalf("Configure returned unexpected config: %+v", cfg)
	}

	if _, _, err := svc.Configure(ctx, KindSymmetric, "", key); err != ErrAlreadyConfigured {
		t.Fatalf("second Configure: got %v, want ErrAlreadyConfigured", err)
	}
}

func TestService_Configure_GeneratesKeyWhenOmitted(t *testing.T) {
	ctx := context.Background()
	svc := newTestService(t)

	_, generated, err := svc.Configure(ctx, KindSymmetric, "", "")
	if err != nil {
		t.Fatalf("Configure: %v", err)
	}
	if generated == "" {
		t.Fatalf("expected a generated key when key material omitted")
	}
	if decoded, derr := hex.DecodeString(generated); derr != nil || len(decoded) != 32 {
		t.Fatalf("generated key is not a 32-byte hex string: %q (err=%v)", generated, derr)
	}
}

func TestService_WrapUnwrap_RoundTrip(t *testing.T) {
	ctx := context.Background()
	svc := newTestService(t)
	if _, _, err := svc.Configure(ctx, KindSymmetric, "", ""); err != nil {
		t.Fatalf("Configure: %v", err)
	}

	plaintext := []byte("owner-only readable")
	ciphertext, dekID, err := svc.WrapData(ctx, "files/secret.txt", plaintext)
	if err != nil {
		t.Fatalf("WrapData: %v", err)
	}
	if ciphertext == "" || dekID == "" {
		t.Fatalf("WrapData produced empty output")
	}

	got, err := svc.UnwrapData(ctx, dekID, ciphertext)
	if err != nil {
		t.Fatalf("UnwrapData: %v", err)
	}
	if string(got) != string(plaintext) {
		t.Fatalf("round trip mismatch: got %q want %q", got, plaintext)
	}

	st, err := svc.Status(ctx)
	if err != nil || st.DEKCount != 1 {
		t.Fatalf("Status DEKCount: %+v err=%v", st, err)
	}
}

func TestService_RevokedDEK_CannotUnwrap(t *testing.T) {
	ctx := context.Background()
	svc := newTestService(t)
	if _, _, err := svc.Configure(ctx, KindSymmetric, "", ""); err != nil {
		t.Fatalf("Configure: %v", err)
	}
	ciphertext, dekID, err := svc.WrapData(ctx, "obj", []byte("data"))
	if err != nil {
		t.Fatalf("WrapData: %v", err)
	}
	if err := svc.RevokeDEK(ctx, dekID); err != nil {
		t.Fatalf("RevokeDEK: %v", err)
	}
	if _, err := svc.UnwrapData(ctx, dekID, ciphertext); err != ErrKeyRevoked {
		t.Fatalf("UnwrapData after revoke: got %v, want ErrKeyRevoked", err)
	}
}

func TestService_RotateKEK_PreservesDecryptability(t *testing.T) {
	ctx := context.Background()
	svc := newTestService(t)
	if _, _, err := svc.Configure(ctx, KindSymmetric, "", ""); err != nil {
		t.Fatalf("Configure: %v", err)
	}

	plaintext := []byte("rotate me safely")
	ciphertext, dekID, err := svc.WrapData(ctx, "obj", plaintext)
	if err != nil {
		t.Fatalf("WrapData: %v", err)
	}

	result, generated, err := svc.RotateKEK(ctx, KindSymmetric, "", "")
	if err != nil {
		t.Fatalf("RotateKEK: %v", err)
	}
	if generated == "" {
		t.Fatalf("expected a generated key from RotateKEK")
	}
	if result.Rotated != 1 || result.Failed != 0 || result.NewVersion != 2 {
		t.Fatalf("unexpected RotationResult: %+v", result)
	}

	// The SAME ciphertext must still decrypt after rotation — only the DEK
	// wrapping changed, never the data.
	got, err := svc.UnwrapData(ctx, dekID, ciphertext)
	if err != nil {
		t.Fatalf("UnwrapData after rotation: %v", err)
	}
	if string(got) != string(plaintext) {
		t.Fatalf("post-rotation round trip mismatch: got %q want %q", got, plaintext)
	}

	st, err := svc.Status(ctx)
	if err != nil || st.Config.KEKVersion != 2 {
		t.Fatalf("Status after rotation: %+v err=%v", st, err)
	}
}

func TestService_RotateKEK_WithoutConfigureFails(t *testing.T) {
	ctx := context.Background()
	svc := newTestService(t)
	if _, _, err := svc.RotateKEK(ctx, KindSymmetric, "", ""); err != ErrNotConfigured {
		t.Fatalf("RotateKEK without Configure: got %v, want ErrNotConfigured", err)
	}
}

func TestService_HTTPProvider_ConfigureAndStatusNeverLeaksToken(t *testing.T) {
	ctx := context.Background()
	svc := newTestService(t)
	cfg, _, err := svc.Configure(ctx, KindHTTP, "https://vault.example/v1/transit", "s3cr3t-bearer-token")
	if err != nil {
		t.Fatalf("Configure: %v", err)
	}
	// The field is json:"-" so it never serialises to an API response; here we
	// confirm the STORED copy really is encrypted (not equal to the plaintext
	// token, and non-empty since a token was supplied).
	if cfg.EncryptedKeyMaterial == "" || cfg.EncryptedKeyMaterial == "s3cr3t-bearer-token" {
		t.Fatalf("key material was not encrypted at rest: %q", cfg.EncryptedKeyMaterial)
	}
}
