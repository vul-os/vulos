package auth

import (
	"bytes"
	"context"
	"testing"

	"github.com/bytemare/opaque"
)

// TestOpaqueServerNeverSeesPassword is the wedge-critical regression guard
// (IDENTITY-SERVICE §3.1). It asserts that across the ENTIRE OPAQUE registration
// + login handshake, the plaintext password never appears in ANY byte string the
// server receives from the client OR persists — closing the one residual seam
// where "central identity" could momentarily become "central access to content
// keys". If a future refactor accidentally routes the password (or a
// password-equivalent) to the server, this test fails.
//
// It uses a DISTINCTIVE password so a substring scan is meaningful, drives the
// full handshake with the vetted library's client half, and checks:
//   - the registration REQUEST bytes the server receives (RegistrationInit)
//   - the registration RECORD bytes the server stores (opaque_records.envelope)
//   - the login KE1 bytes the server receives
//   - the login KE3 bytes the server receives
//   - the persisted envelope re-read from the DB
func TestOpaqueServerNeverSeesPassword(t *testing.T) {
	const password = "ZZZ-unique-plaintext-passphrase-do-not-transit-42-QQQ"
	pwBytes := []byte(password)

	svc, st, u := setupOpaque(t)
	ctx := context.Background()
	conf := opaque.DefaultConfiguration()
	client, err := conf.Client()
	if err != nil {
		t.Fatalf("client: %v", err)
	}

	// ── Registration ──────────────────────────────────────────────────────────
	regReq, err := client.RegistrationInit(pwBytes)
	if err != nil {
		t.Fatalf("RegistrationInit: %v", err)
	}
	regReqBytes := regReq.Serialize()
	assertNoPassword(t, "registration request (server receives)", regReqBytes, pwBytes)

	respBytes, err := svc.OpaqueRegistrationResponse(u.ID, regReqBytes)
	if err != nil {
		t.Fatalf("OpaqueRegistrationResponse: %v", err)
	}
	deser, _ := conf.Deserializer()
	resp, err := deser.RegistrationResponse(respBytes)
	if err != nil {
		t.Fatalf("deser RegistrationResponse: %v", err)
	}
	record, _, err := client.RegistrationFinalize(resp, []byte(u.ID), []byte(opaqueServerIdentity()))
	if err != nil {
		t.Fatalf("RegistrationFinalize: %v", err)
	}
	recordBytes := record.Serialize()
	assertNoPassword(t, "registration record (server stores)", recordBytes, pwBytes)

	if err := svc.StoreOpaqueRecord(ctx, u.ID, recordBytes); err != nil {
		t.Fatalf("StoreOpaqueRecord: %v", err)
	}

	// The envelope as persisted must not contain the password either.
	var stored []byte
	if err := st.db.QueryRowContext(ctx,
		st.db.Rebind(`SELECT envelope FROM opaque_records WHERE user_id = ?`), u.ID,
	).Scan(&stored); err != nil {
		t.Fatalf("read stored envelope: %v", err)
	}
	assertNoPassword(t, "persisted opaque_records.envelope", stored, pwBytes)

	// ── Login ─────────────────────────────────────────────────────────────────
	// A fresh client instance: the registration run left OPRF blind state on the
	// previous one (the library forbids reusing it across protocol runs).
	loginClient, err := conf.Client()
	if err != nil {
		t.Fatalf("login client: %v", err)
	}
	ke1, err := loginClient.GenerateKE1(pwBytes)
	if err != nil {
		t.Fatalf("GenerateKE1: %v", err)
	}
	ke1Bytes := ke1.Serialize()
	assertNoPassword(t, "login KE1 (server receives)", ke1Bytes, pwBytes)

	handshakeID, ke2Bytes, err := svc.OpaqueLoginStart(ctx, u.ID, ke1Bytes)
	if err != nil {
		t.Fatalf("OpaqueLoginStart: %v", err)
	}
	ke2, err := deser.KE2(ke2Bytes)
	if err != nil {
		t.Fatalf("deser KE2: %v", err)
	}
	ke3, _, _, err := loginClient.GenerateKE3(ke2, []byte(u.ID), []byte(opaqueServerIdentity()))
	if err != nil {
		t.Fatalf("GenerateKE3: %v", err)
	}
	ke3Bytes := ke3.Serialize()
	assertNoPassword(t, "login KE3 (server receives)", ke3Bytes, pwBytes)

	// And the handshake actually authenticates (the property is only meaningful
	// if the server can STILL verify the password without seeing it).
	gotID, err := svc.OpaqueLoginFinish(ctx, handshakeID, ke3Bytes)
	if err != nil {
		t.Fatalf("OpaqueLoginFinish: %v", err)
	}
	if gotID != u.ID {
		t.Fatalf("authenticated %q, want %q", gotID, u.ID)
	}
}

// assertNoPassword fails if pw appears as a substring of data.
func assertNoPassword(t *testing.T, what string, data, pw []byte) {
	t.Helper()
	if bytes.Contains(data, pw) {
		t.Fatalf("PASSWORD LEAK: plaintext password found in %s (%d bytes)", what, len(data))
	}
}
