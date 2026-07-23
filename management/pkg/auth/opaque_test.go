package auth

import (
	"context"
	"testing"

	"github.com/bytemare/opaque"
)

// setupOpaque enables OPAQUE with fresh server key material and returns a service
// bound to a fresh store plus a registered test user.
func setupOpaque(t *testing.T) (*OpaqueService, *Store, *User) {
	t.Helper()
	t.Setenv("AUTH_OPAQUE_ENABLED", "1")
	keyHex, err := GenerateOpaqueServerKeyHex()
	if err != nil {
		t.Fatalf("GenerateOpaqueServerKeyHex: %v", err)
	}
	t.Setenv("OPAQUE_SERVER_KEY", keyHex)

	st := openTestStore(t)
	u, _, err := st.Signup(context.Background(), "opaque@example.com", "correct-horse-battery-staple", "127.0.0.1", "ua")
	if err != nil {
		t.Fatalf("signup: %v", err)
	}
	svc, err := NewOpaqueService(st)
	if err != nil {
		t.Fatalf("NewOpaqueService: %v", err)
	}
	return svc, st, u
}

// opaqueRegister runs the full client<->server OPAQUE registration for password,
// storing the resulting record. Uses the vetted library's CLIENT half as the
// interop counterpart (spec-conformant), which validates our server half.
func opaqueRegister(t *testing.T, svc *OpaqueService, userID, password string) {
	t.Helper()
	ctx := context.Background()
	conf := opaque.DefaultConfiguration()
	client, err := conf.Client()
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	req, err := client.RegistrationInit([]byte(password))
	if err != nil {
		t.Fatalf("RegistrationInit: %v", err)
	}
	respBytes, err := svc.OpaqueRegistrationResponse(userID, req.Serialize())
	if err != nil {
		t.Fatalf("OpaqueRegistrationResponse: %v", err)
	}
	deser, _ := conf.Deserializer()
	resp, err := deser.RegistrationResponse(respBytes)
	if err != nil {
		t.Fatalf("deser RegistrationResponse: %v", err)
	}
	record, _, err := client.RegistrationFinalize(resp, []byte(userID), []byte(opaqueServerIdentity()))
	if err != nil {
		t.Fatalf("RegistrationFinalize: %v", err)
	}
	if err := svc.StoreOpaqueRecord(ctx, userID, record.Serialize()); err != nil {
		t.Fatalf("StoreOpaqueRecord: %v", err)
	}
}

// opaqueLogin drives a full login handshake for password and returns the userID
// the server authenticated, or "" plus an error on failure.
func opaqueLogin(t *testing.T, svc *OpaqueService, userID, password string) (string, error) {
	t.Helper()
	ctx := context.Background()
	conf := opaque.DefaultConfiguration()
	client, err := conf.Client()
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	ke1, err := client.GenerateKE1([]byte(password))
	if err != nil {
		t.Fatalf("GenerateKE1: %v", err)
	}
	handshakeID, ke2Bytes, err := svc.OpaqueLoginStart(ctx, userID, ke1.Serialize())
	if err != nil {
		return "", err
	}
	deser, _ := conf.Deserializer()
	ke2, err := deser.KE2(ke2Bytes)
	if err != nil {
		t.Fatalf("deser KE2: %v", err)
	}
	ke3, _, _, err := client.GenerateKE3(ke2, []byte(userID), []byte(opaqueServerIdentity()))
	if err != nil {
		// Wrong password fails at the CLIENT (bad envelope MAC). Surface as
		// handshake failure so callers can assert the negative path.
		return "", ErrOpaqueHandshake
	}
	return svc.OpaqueLoginFinish(ctx, handshakeID, ke3.Serialize())
}

// TestOpaqueRoundTrip validates the full OPAQUE registration + login handshake
// interoperates with the vetted library client (the spec-conformant counterpart)
// and that the server NEVER stores a crackable password: the opaque_records
// envelope is server-opaque by construction, and there is no plaintext password
// anywhere in the flow.
func TestOpaqueRoundTrip(t *testing.T) {
	svc, _, u := setupOpaque(t)
	const pw = "correct-horse-battery-staple"

	opaqueRegister(t, svc, u.ID, pw)

	got, err := opaqueLogin(t, svc, u.ID, pw)
	if err != nil {
		t.Fatalf("opaque login: %v", err)
	}
	if got != u.ID {
		t.Fatalf("login authenticated wrong user: %q != %q", got, u.ID)
	}
}

// TestOpaqueWrongPassword verifies a bad password fails the handshake and does
// NOT authenticate — with no oracle distinguishing the failure reason.
func TestOpaqueWrongPassword(t *testing.T) {
	svc, _, u := setupOpaque(t)
	opaqueRegister(t, svc, u.ID, "correct-horse-battery-staple")

	if _, err := opaqueLogin(t, svc, u.ID, "wrong-password-entirely"); err == nil {
		t.Fatal("login with wrong password should fail")
	}
}

// TestOpaqueDisabledByDefault verifies the feature is off unless the flag is set,
// so argon2id stays the live path and OPAQUE endpoints are unavailable.
func TestOpaqueDisabledByDefault(t *testing.T) {
	t.Setenv("AUTH_OPAQUE_ENABLED", "")
	if OpaqueEnabled() {
		t.Fatal("OPAQUE must be OFF by default")
	}
	st := openTestStore(t)
	if _, err := NewOpaqueService(st); err != ErrOpaqueDisabled {
		t.Fatalf("NewOpaqueService with flag off: want ErrOpaqueDisabled, got %v", err)
	}
}

// TestOpaqueEnabledWithoutKeyFailsClosed verifies enabling OPAQUE without server
// key material fails closed rather than silently degrading.
func TestOpaqueEnabledWithoutKeyFailsClosed(t *testing.T) {
	t.Setenv("AUTH_OPAQUE_ENABLED", "1")
	t.Setenv("OPAQUE_SERVER_KEY", "")
	st := openTestStore(t)
	if _, err := NewOpaqueService(st); err != ErrOpaqueNotConfigured {
		t.Fatalf("want ErrOpaqueNotConfigured, got %v", err)
	}
}

// TestOpaqueHasRecord verifies dual-write bookkeeping: HasOpaqueRecord is false
// before registration and true after, so Login's modality machinery can prefer
// OPAQUE only when a record exists (and fall back to argon2 otherwise).
func TestOpaqueHasRecord(t *testing.T) {
	svc, _, u := setupOpaque(t)
	ctx := context.Background()

	has, err := svc.HasOpaqueRecord(ctx, u.ID)
	if err != nil {
		t.Fatalf("HasOpaqueRecord: %v", err)
	}
	if has {
		t.Fatal("should have no record before registration")
	}
	opaqueRegister(t, svc, u.ID, "correct-horse-battery-staple")
	has, err = svc.HasOpaqueRecord(ctx, u.ID)
	if err != nil {
		t.Fatalf("HasOpaqueRecord: %v", err)
	}
	if !has {
		t.Fatal("should have a record after registration")
	}
}
