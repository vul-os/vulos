package osdist

// security_surface_test.go — is_security/severity/notes at the osdist verifier.
//
// This package owns the ONE definition of the rules (ValidateSecuritySurface)
// that `cmd/sign sign-manifest`, ParseAndVerify and the services/ota client all
// apply to the same bytes. Three independently maintained copies of a signing
// rule is how this manifest's signed surface came to disagree with its signer in
// the first place (b4d05d09), so the rules are tested here, once, and the other
// two ends are tested for calling them.

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// securityManifest is sampleManifest as a genuinely-critical security release.
func securityManifest() StableManifest {
	m := sampleManifest()
	m.IsSecurity = true
	m.Severity = SeverityCritical
	m.Notes = "Fixes a remote authentication bypass in the box login path."
	return m
}

// ─── The surface round-trips ──────────────────────────────────────────────────

// TestParseAndVerify_SecuritySurfaceRoundTrips — a signed security release
// verifies and reads back unchanged.
func TestParseAndVerify_SecuritySurfaceRoundTrips(t *testing.T) {
	pub, priv := genKeyPair(t)
	m := securityManifest()
	sig := signManifest(t, &m, priv)

	got, err := ParseAndVerify(marshalManifest(t, m), sig, pub, 0)
	if err != nil {
		t.Fatalf("a correctly signed security release was rejected: %v", err)
	}
	if !got.IsSecurity || got.Severity != SeverityCritical || got.Notes != m.Notes {
		t.Errorf("severity surface did not survive: is_security=%v severity=%q notes=%q",
			got.IsSecurity, got.Severity, got.Notes)
	}
}

// TestCanonical_SevenKeyDocumentUnchanged is the compatibility guarantee at this
// verifier: a manifest that sets none of the severity fields must canonicalise
// to the EXACT seven-key bytes it did before the surface existed, or every
// signature already issued stops verifying.
//
// The expectation is a hard-coded literal, not a re-derivation.
func TestCanonical_SevenKeyDocumentUnchanged(t *testing.T) {
	const want = `{"channel":"stable","latest":"v08","min_epoch":3,` +
		`"path":"os/v08/os-core.squashfs","released_at":"2026-05-20T09:00:00Z",` +
		`"roothash":"abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890",` +
		`"size":734003200}`

	m := sampleManifest()
	got, err := m.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Fatalf("a manifest with no severity fields now canonicalises to\n  %s\nwant\n  %s\n"+
			"every signature already issued over the seven-key shape has just been invalidated",
			got, want)
	}
}

// TestParseAndVerify_ZeroValuedSeverityFieldsAreInert documents the corollary of
// `omitempty` that looks like a hole and is not.
//
// An attacker CAN append `"is_security": false` / `"severity": ""` /
// `"notes": ""` to a signed seven-key document without breaking the signature,
// because those canonicalise away. That is sound: the canonical form is what is
// authenticated, and a document sharing a canonical form has the same meaning.
// Explicitly-present zero and absent are the same statement — "this release says
// nothing about severity" — so the accepted result must be identical.
//
// Any value that CHANGES the meaning changes the canonical bytes; that direction
// is TestParseAndVerify_SecurityFieldsAreSigned below.
func TestParseAndVerify_ZeroValuedSeverityFieldsAreInert(t *testing.T) {
	pub, priv := genKeyPair(t)
	m := sampleManifest()
	sig := signManifest(t, &m, priv)

	var doc map[string]any
	if err := json.Unmarshal(marshalManifest(t, m), &doc); err != nil {
		t.Fatal(err)
	}
	doc["is_security"] = false
	doc["severity"] = ""
	doc["notes"] = ""
	tampered, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}

	got, err := ParseAndVerify(tampered, sig, pub, 0)
	if err != nil {
		t.Fatalf("appending zero-valued severity fields broke a good signature: %v", err)
	}
	if got.IsSecurity || got.Severity != "" || got.Notes != "" {
		t.Errorf("zero-valued fields were read as a statement: %+v", got)
	}
}

// TestParseAndVerify_SecurityFieldsAreSigned — the three fields must really be
// inside the signature, or they are appendable in transit and the whole exercise
// was pointless. Sign, edit the served document, leave the signature alone.
//
// Each case keeps the document STRUCTURALLY VALID so that the signature check is
// the gate doing the refusing — clearing is_security drops severity with it,
// because "severity on a release not marked is_security" would otherwise be
// refused by the structural gate and mask what is under test. The assertion on
// ErrMalformed enforces that isolation.
func TestParseAndVerify_SecurityFieldsAreSigned(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(map[string]any)
	}{
		{"is_security cleared", func(d map[string]any) {
			d["is_security"] = false
			delete(d, "severity")
		}},
		{"severity downgraded", func(d map[string]any) { d["severity"] = SeverityLow }},
		{"notes rewritten", func(d map[string]any) {
			d["notes"] = "Routine maintenance release, nothing urgent."
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pub, priv := genKeyPair(t)
			m := securityManifest()
			sig := signManifest(t, &m, priv)

			var doc map[string]any
			if err := json.Unmarshal(marshalManifest(t, m), &doc); err != nil {
				t.Fatal(err)
			}
			tc.edit(doc)
			tampered, err := json.Marshal(doc)
			if err != nil {
				t.Fatal(err)
			}

			if _, err := ParseAndVerify(tampered, sig, pub, 0); !errors.Is(err, ErrBadSignature) {
				t.Fatalf("%s in transit was accepted (err = %v) — the field is not inside "+
					"the signed surface", tc.name, err)
			} else if errors.Is(err, ErrMalformed) {
				t.Errorf("the structural gate refused this before the signature check could, "+
					"masking the gate under test: %v", err)
			}
		})
	}
}

// ─── A valid signature is not a licence to render anything ───────────────────

// TestParseAndVerify_RefusesSignedButUnrenderableSurface — every manifest here is
// signed CORRECTLY and must still be refused. A signature proves which key wrote
// a string, not that the string is safe to put in front of the box owner beside
// a "Download & stage update" button.
//
// The refusal must NOT be ErrBadSignature: that would mean the fixture, not the
// gate, did the work.
func TestParseAndVerify_RefusesSignedButUnrenderableSurface(t *testing.T) {
	for _, tc := range []struct {
		name      string
		mutate    func(*StableManifest)
		wantInErr string
	}{
		{"a link the owner is told to follow", func(m *StableManifest) {
			m.Notes = "Apply immediately: https://vulos-security.example/patch"
		}, "://"},
		{"a scheme in any case", func(m *StableManifest) {
			m.Notes = "Apply immediately: HTTPS://vulos-security.example/patch"
		}, "://"},
		{"a bare host", func(m *StableManifest) {
			m.Notes = "Details at www.vulos-security.example"
		}, "www."},
		{"notes longer than a notification body", func(m *StableManifest) {
			m.Notes = strings.Repeat("A", MaxNotesBytes+1)
		}, "the limit is"},
		{"a newline flooding the panel", func(m *StableManifest) {
			m.Notes = "Security fix.\n\n\n\n\nCall +1-555-0100 to confirm."
		}, "non-printing"},
		{"a tab", func(m *StableManifest) { m.Notes = "Security\tfix." }, "non-printing"},
		{"a bidi override rewriting the text", func(m *StableManifest) {
			m.Notes = "Security fix ‮gnihsihp‬."
		}, "non-printing"},
		{"a zero-width joiner", func(m *StableManifest) {
			m.Notes = "Security‍fix."
		}, "non-printing"},
		{"a severity outside the closed set", func(m *StableManifest) {
			m.Severity = "APOCALYPTIC — ACT NOW"
		}, "requires severity"},
		{"is_security with no severity", func(m *StableManifest) { m.Severity = "" }, "requires severity"},
		{"severity on a release that fixes no vulnerability", func(m *StableManifest) {
			m.IsSecurity, m.Severity = false, SeverityCritical
		}, "not marked is_security"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pub, priv := genKeyPair(t)
			m := securityManifest()
			tc.mutate(&m)
			sig := signManifest(t, &m, priv)

			_, err := ParseAndVerify(marshalManifest(t, m), sig, pub, 0)
			if !errors.Is(err, ErrMalformed) {
				t.Fatalf("a correctly SIGNED manifest carrying %s was accepted (err = %v)", tc.name, err)
			}
			if errors.Is(err, ErrBadSignature) {
				t.Fatalf("the fixture produced a bad signature — this is not exercising the "+
					"severity-surface gate at all: %v", err)
			}
			if !errors.Is(err, ErrSecuritySurface) {
				t.Errorf("expected ErrSecuritySurface underneath, got: %v", err)
			}
			if !strings.Contains(err.Error(), tc.wantInErr) {
				t.Errorf("the refusal should name the problem (%q), got: %v", tc.wantInErr, err)
			}
		})
	}
}

// TestParseAndVerify_AcceptsLegitimateNotes keeps the table above from "passing"
// by refusing everything. These are the strings a real release writes, and every
// documented severity value.
func TestParseAndVerify_AcceptsLegitimateNotes(t *testing.T) {
	for _, notes := range []string{
		"",
		"Fixes CVE-2026-1234 in the boot verifier.",
		"Security fix — apply at your convenience. No action needed beyond staging.",
		"Corrige une faille d'authentification. Mise à jour recommandée.",
		strings.Repeat("A", MaxNotesBytes), // exactly at the bound, not over it
	} {
		pub, priv := genKeyPair(t)
		m := securityManifest()
		m.Notes = notes
		sig := signManifest(t, &m, priv)
		if _, err := ParseAndVerify(marshalManifest(t, m), sig, pub, 0); err != nil {
			t.Errorf("a legitimate release note was refused (%q): %v", notes, err)
		}
	}

	for _, s := range Severities {
		pub, priv := genKeyPair(t)
		m := securityManifest()
		m.Severity = s
		sig := signManifest(t, &m, priv)
		got, err := ParseAndVerify(marshalManifest(t, m), sig, pub, 0)
		if err != nil {
			t.Errorf("the documented severity %q was refused: %v", s, err)
			continue
		}
		if got.Severity != s {
			t.Errorf("severity = %q, want %q", got.Severity, s)
		}
	}

	// ...and a plain, non-security release with an ordinary note.
	pub, priv := genKeyPair(t)
	m := sampleManifest()
	m.Notes = "Faster boot, and a fix for the Wi-Fi setup screen."
	sig := signManifest(t, &m, priv)
	if _, err := ParseAndVerify(marshalManifest(t, m), sig, pub, 0); err != nil {
		t.Errorf("an ordinary release note on a non-security release was refused: %v", err)
	}
}
