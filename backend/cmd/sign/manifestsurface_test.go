package main

// manifestsurface_test.go — the signer's half of the "three definitions move
// together" gate.
//
// ManifestPayload IS the signed surface of stable.json. Two device-side
// definitions must describe the same JSON keys — osdist.StableManifest and
// services/ota.Manifest — and when they did not, the result was not a warning:
// ota carried is_security/severity/notes that this signer had no flag for, so
// the first manifest to set one would have grown a key no signature covered and
// no manifest could ever have verified again (b4d05d09).
//
// services/ota's TestManifestSignedSurfaceMatchesSigner pins the same ten keys
// from the box side, but it can only TRANSCRIBE this struct's field list —
// cmd/sign is package main and cannot be imported. So a change made only here
// would slip past it. This file closes that direction: it asserts the key list
// from the signer's side AND cross-checks ManifestPayload against
// osdist.StableManifest by canonicalising both, which is the comparison that
// cannot be satisfied by editing one file.

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"vulos/backend/services/osdist"
	"vulos/backend/services/signing"
)

// signedManifestKeySet is the complete set of keys the release key signs over a
// stable.json. Written as a literal on purpose: deriving it from the struct
// under test would move in lockstep with any change and prove nothing.
var signedManifestKeySet = map[string]struct{}{
	"channel": {}, "latest": {}, "min_epoch": {}, "path": {},
	"released_at": {}, "roothash": {}, "size": {},
	"is_security": {}, "severity": {}, "notes": {},
}

// fullyPopulatedPayload is a ManifestPayload with EVERY field non-zero, so that
// `omitempty` cannot hide a field from the canonical key set.
func fullyPopulatedPayload() ManifestPayload {
	return ManifestPayload{
		Channel:    "stable",
		Latest:     "v09",
		MinEpoch:   3,
		Path:       "os/v09/os-core.squashfs",
		ReleasedAt: "2026-05-20T09:00:00Z",
		RootHash:   "deadbeef",
		Size:       734003200,
		IsSecurity: true,
		Severity:   osdist.SeverityCritical,
		Notes:      "Fixes a remote authentication bypass in the login path.",
	}
}

// canonicalKeys returns the top-level key set of signing.Canonical(v) — i.e.
// exactly the keys a signature over v covers, after omitempty has had its say.
func canonicalKeys(t *testing.T, v any) map[string]struct{} {
	t.Helper()
	b, err := signing.Canonical(v)
	if err != nil {
		t.Fatalf("canonical: %v", err)
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatalf("unmarshal canonical bytes: %v", err)
	}
	keys := map[string]struct{}{}
	for k := range doc {
		keys[k] = struct{}{}
	}
	return keys
}

// TestManifestPayloadMatchesTheVerifiers is the cross-check that no single-file
// edit can satisfy: the bytes this signer produces and the bytes
// osdist.StableManifest describes must carry the same keys.
func TestManifestPayloadMatchesTheVerifiers(t *testing.T) {
	signer := canonicalKeys(t, fullyPopulatedPayload())
	if !reflect.DeepEqual(signedManifestKeySet, signer) {
		t.Errorf("cmd/sign ManifestPayload signs %v, want %v", signer, signedManifestKeySet)
	}

	verifier := canonicalKeys(t, osdist.StableManifest{
		Channel:    "stable",
		Latest:     "v09",
		MinEpoch:   3,
		Path:       "os/v09/os-core.squashfs",
		RootHash:   "deadbeef",
		Size:       734003200,
		IsSecurity: true,
		Severity:   osdist.SeverityCritical,
		Notes:      "Fixes a remote authentication bypass in the login path.",
	})
	if !reflect.DeepEqual(signer, verifier) {
		t.Errorf("the signer covers %v but osdist.StableManifest describes %v — "+
			"a key on one side and not the other is either an unsigned field the box "+
			"reads, or a signature no box can reproduce", signer, verifier)
	}
}

// TestSignManifestBackCompatSevenKeyDocument is the compatibility decision,
// measured. A release that sets none of the severity fields must produce the
// EXACT seven-key document earlier releases signed, so every signature already
// issued keeps verifying without a re-signing ceremony.
//
// The expectation is a hard-coded literal. Drop `omitempty` from any severity
// field and this fails here, at the signer, rather than silently on boxes in the
// field.
func TestSignManifestBackCompatSevenKeyDocument(t *testing.T) {
	const want = `{"channel":"stable","latest":"v08","min_epoch":3,` +
		`"path":"os/v08/os-core.squashfs","released_at":"2026-05-20T09:00:00Z",` +
		`"roothash":"deadbeef","size":734003200}`

	got, err := signing.Canonical(ManifestPayload{
		Channel:    "stable",
		Latest:     "v08",
		MinEpoch:   3,
		Path:       "os/v08/os-core.squashfs",
		ReleasedAt: "2026-05-20T09:00:00Z",
		RootHash:   "deadbeef",
		Size:       734003200,
	})
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Fatalf("a manifest with no severity fields now signs\n  %s\nwant\n  %s\n"+
			"every signature already issued over the seven-key shape has just been invalidated",
			got, want)
	}
}

// TestSignedManifestVerifiesThroughOsdist walks the whole shape end to end: this
// signer's canonical bytes ARE the published document, and the device-side
// verifier accepts them and reads back the severity surface unchanged.
//
// This is the test that would have caught the original defect. It cannot pass
// unless signer and verifier agree on every key AND on the byte encoding.
func TestSignedManifestVerifiesThroughOsdist(t *testing.T) {
	relPub, relPriv := genKeyPair(t)
	payload := fullyPopulatedPayload()

	doc, err := signing.Canonical(payload)
	if err != nil {
		t.Fatal(err)
	}
	sig, err := SignManifest(relPriv, "release-test", payload)
	if err != nil {
		t.Fatalf("SignManifest refused a valid security release: %v", err)
	}

	m, err := osdist.ParseAndVerify(doc, sig.SigBytes, relPub, 0)
	if err != nil {
		t.Fatalf("osdist refused a manifest this signer just produced: %v", err)
	}
	if !m.IsSecurity || m.Severity != osdist.SeverityCritical || m.Notes != payload.Notes {
		t.Errorf("severity surface did not survive the round trip: is_security=%v severity=%q notes=%q",
			m.IsSecurity, m.Severity, m.Notes)
	}

	// And the compatibility direction: a plain release still verifies too.
	plain := ManifestPayload{
		Channel: "stable", Latest: "v08", MinEpoch: 3,
		Path: "os/v08/os-core.squashfs", ReleasedAt: "2026-05-20T09:00:00Z",
		RootHash: "deadbeef", Size: 734003200,
	}
	plainDoc, err := signing.Canonical(plain)
	if err != nil {
		t.Fatal(err)
	}
	plainSig, err := SignManifest(relPriv, "release-test", plain)
	if err != nil {
		t.Fatal(err)
	}
	pm, err := osdist.ParseAndVerify(plainDoc, plainSig.SigBytes, relPub, 0)
	if err != nil {
		t.Fatalf("osdist refused a seven-key manifest: %v", err)
	}
	if pm.IsSecurity || pm.Severity != "" || pm.Notes != "" {
		t.Errorf("a manifest that says nothing about severity was read as %+v", pm)
	}
}

// TestSignManifestRefusesUnrenderableSurface — the signer will not produce an
// artifact every box in the field would refuse, and will not sign a string that
// must never be put in front of the box owner.
//
// The link cases are the ones that matter. `notes` is used verbatim as the body
// of a high-priority push notification sent to the box owner; a signature proves
// which key wrote it, not that following it is safe.
func TestSignManifestRefusesUnrenderableSurface(t *testing.T) {
	_, relPriv := genKeyPair(t)

	for _, tc := range []struct {
		name      string
		mutate    func(*ManifestPayload)
		wantInErr string
	}{
		{"security without severity", func(p *ManifestPayload) {
			p.IsSecurity, p.Severity = true, ""
		}, "requires severity"},
		{"severity outside the closed set", func(p *ManifestPayload) {
			p.IsSecurity, p.Severity = true, "apocalyptic"
		}, "requires severity"},
		{"severity without security", func(p *ManifestPayload) {
			p.IsSecurity, p.Severity = false, osdist.SeverityCritical
		}, "not marked is_security"},
		{"notes carrying a URL", func(p *ManifestPayload) {
			p.Notes = "Apply now via https://vulos-security.example/patch"
		}, "://"},
		{"notes carrying a bare host", func(p *ManifestPayload) {
			p.Notes = "Details at www.vulos-security.example"
		}, "www."},
		{"notes over the length bound", func(p *ManifestPayload) {
			p.Notes = strings.Repeat("A", osdist.MaxNotesBytes+1)
		}, "the limit is"},
		{"notes with a newline", func(p *ManifestPayload) {
			p.Notes = "Security fix.\nAlso: call +1-555-0100 to confirm."
		}, "non-printing"},
		{"notes with a bidi override", func(p *ManifestPayload) {
			p.Notes = "Security fix ‮gnihsihp‬."
		}, "non-printing"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := fullyPopulatedPayload()
			tc.mutate(&p)
			_, err := SignManifest(relPriv, "release-test", p)
			if err == nil {
				t.Fatalf("SignManifest signed %s", tc.name)
			}
			if !strings.Contains(err.Error(), tc.wantInErr) {
				t.Errorf("refusal should name the problem (%q), got: %v", tc.wantInErr, err)
			}
		})
	}
}

// TestSignManifestAcceptsEverySeverity — without this the table above could
// "pass" by refusing everything, which is the shape of a gate that checks
// nothing.
func TestSignManifestAcceptsEverySeverity(t *testing.T) {
	_, relPriv := genKeyPair(t)
	for _, s := range osdist.Severities {
		p := fullyPopulatedPayload()
		p.Severity = s
		if _, err := SignManifest(relPriv, "release-test", p); err != nil {
			t.Errorf("SignManifest refused the documented severity %q: %v", s, err)
		}
	}
	// ...and a plain release with an ordinary note.
	p := fullyPopulatedPayload()
	p.IsSecurity, p.Severity = false, ""
	p.Notes = "Faster boot, and a fix for the Wi-Fi setup screen."
	if _, err := SignManifest(relPriv, "release-test", p); err != nil {
		t.Errorf("SignManifest refused an ordinary release note: %v", err)
	}
}

// TestSignManifestSignsTheSeverityFields — a signature that did not actually
// cover is_security/severity/notes would leave them appendable in transit, which
// is the whole defect this change exists to close. Sign, flip one field, and the
// signature must no longer verify.
func TestSignManifestSignsTheSeverityFields(t *testing.T) {
	relPub, relPriv := genKeyPair(t)
	payload := fullyPopulatedPayload()
	sig, err := SignManifest(relPriv, "release-test", payload)
	if err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name   string
		mutate func(*ManifestPayload)
	}{
		{"is_security cleared", func(p *ManifestPayload) { p.IsSecurity, p.Severity = false, "" }},
		{"severity downgraded", func(p *ManifestPayload) { p.Severity = osdist.SeverityLow }},
		{"notes rewritten", func(p *ManifestPayload) { p.Notes = "Routine maintenance release." }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tampered := payload
			tc.mutate(&tampered)
			ok, err := VerifyManifest(relPub, tampered, sig)
			if err != nil {
				t.Fatal(err)
			}
			if ok {
				t.Errorf("the signature still verified with %s — that field is not inside "+
					"the signed surface and is appendable in transit", tc.name)
			}
		})
	}
}
