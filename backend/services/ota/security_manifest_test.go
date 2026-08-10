package ota

// security_manifest_test.go — the release-severity surface, from a genuinely
// signed manifest through to the owner-facing UpdateStatus.
//
// The feature these tests cover could not previously exist. is_security,
// severity and notes lived on the box side and nowhere in `cmd/sign
// sign-manifest`, so setting one would have grown canonical(manifest) a key no
// signature covered and no manifest could ever have verified again; b4d05d09
// removed them, which left the security-update notification in
// cmd/server/routes_ota.go honestly dead. They are now inside the signature, and
// the tests below are the difference between saying so and knowing it.
//
// Every tampering test here edits the SERVED document after signing and leaves
// the .sig untouched — the shape an attacker who can modify a legitimately
// signed stable.json in transit produces. Where the edit would also break the
// severity-surface rules, the fixture drops the accompanying field too, so the
// gate under test is the SIGNATURE and not the structural check standing in
// front of it.

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"vulos/backend/services/osdist"
)

// securityRelease is a correctly signed, genuinely-critical security release.
func securityRelease(version string, image []byte) *release {
	r := newRelease(version, image)
	r.isSecurity = true
	r.severity = osdist.SeverityCritical
	r.notes = "Fixes a remote authentication bypass in the box login path."
	return r
}

// ─── The feature, working ─────────────────────────────────────────────────────

// TestCheck_SignedSecurityReleaseReachesTheOwner is the test that could not have
// passed before this change: a security release signed exactly as `cmd/sign
// sign-manifest -security -severity critical -notes ...` signs one, verified,
// with the severity surface arriving intact in the status the owner sees and the
// notification consumes.
func TestCheck_SignedSecurityReleaseReachesTheOwner(t *testing.T) {
	ch := newFakeChannel(t)
	r := securityRelease("v09", []byte("os-core squashfs image bytes for v09 ................"))
	ch.publishRelease(t, r)
	box := newStageBox(t, ch)

	status, err := box.client.Check(context.Background())
	if err != nil {
		t.Fatalf("a correctly signed security release was rejected: %v", err)
	}
	if !status.Available || status.LatestVersion != "v09" {
		t.Fatalf("status = %+v, want v09 available", status)
	}
	if !status.IsSecurity {
		t.Error("is_security did not survive verification — the owner is never told this is a security update")
	}
	if status.Severity != osdist.SeverityCritical {
		t.Errorf("severity = %q, want %q", status.Severity, osdist.SeverityCritical)
	}
	if status.Notes != r.notes {
		t.Errorf("notes = %q, want %q", status.Notes, r.notes)
	}
}

// TestCheck_OrdinaryReleaseIsNotFlaggedSecurity is the other half, and the one
// that stops "it fires" from being true by accident: an ordinary release must
// leave every severity field zero.
func TestCheck_OrdinaryReleaseIsNotFlaggedSecurity(t *testing.T) {
	ch := newFakeChannel(t)
	ch.publishRelease(t, newRelease("v09", []byte("image bytes")))
	box := newStageBox(t, ch)

	status, err := box.client.Check(context.Background())
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if !status.Available {
		t.Fatalf("status = %+v, want v09 available", status)
	}
	if status.IsSecurity || status.Severity != "" || status.Notes != "" {
		t.Errorf("an ordinary release was reported as a security update: %+v", status)
	}
}

// TestCheck_SevenKeyManifestStillVerifies is the compatibility guarantee at the
// verifier: newRelease publishes a manifest that sets none of the severity
// fields, so `omitempty` drops them and the document is byte-identical to the
// seven-key shape signed before this surface existed. Every signature already
// issued must keep verifying.
//
// The byte-identity itself is measured by TestSevenFieldManifestCanonicalisesUnchanged;
// this asserts the consequence, over the real HTTP channel and the real chain.
func TestCheck_SevenKeyManifestStillVerifies(t *testing.T) {
	ch := newFakeChannel(t)
	ch.publishRelease(t, newRelease("v09", []byte("image bytes")))

	doc := string(ch.files["/"+manifestName])
	for _, k := range []string{"is_security", "severity", "notes"} {
		if strings.Contains(doc, k) {
			t.Fatalf("fixture bug: a release that sets no severity field published %q in %s", k, doc)
		}
	}

	box := newStageBox(t, ch)
	if _, err := box.client.Check(context.Background()); err != nil {
		t.Fatalf("a seven-key manifest — the shape every already-issued signature covers — "+
			"was rejected: %v", err)
	}
}

// TestStage_SignedSecurityReleaseStages — the severity fields must not disturb
// the staging path. Stage reconstructs the signed ImagePayload from the
// manifest's own bytes; three extra keys in that document must be ignored by
// that reconstruction, not break it.
func TestStage_SignedSecurityReleaseStages(t *testing.T) {
	ch := newFakeChannel(t)
	image := []byte("os-core squashfs image bytes for v09 ................")
	ch.publishRelease(t, securityRelease("v09", image))
	box := newStageBox(t, ch)

	res, err := box.client.Stage(context.Background())
	if err != nil {
		t.Fatalf("Stage on a correctly signed security release failed: %v", err)
	}
	if !res.Staged || res.Version != "v09" {
		t.Errorf("StageResult = %+v, want v09 staged", res)
	}
	if got := box.stagedImage(t); string(got) != string(image) {
		t.Errorf("staged image differs from the published one (%d vs %d bytes)", len(got), len(image))
	}
}

// ─── Tampering in transit: the signature is the gate ──────────────────────────

// TestCheck_IsSecurityFlippedInTransitFailsVerification is the headline refusal.
//
// A genuinely signed CRITICAL security release is rewritten on the wire to look
// routine — the direction that matters, because it suppresses the banner and the
// owner's priority notification. severity is dropped alongside it so the served
// document remains STRUCTURALLY VALID: without that, the severity-surface gate
// ("severity on a release not marked is_security") would refuse it first and
// this test would pass with the signature check deleted.
func TestCheck_IsSecurityFlippedInTransitFailsVerification(t *testing.T) {
	ch := newFakeChannel(t)
	r := securityRelease("v09", []byte("image bytes"))
	r.extraManifestFields = map[string]any{"is_security": false}
	r.dropManifestFields = []string{"severity"}
	ch.publishRelease(t, r)
	box := newStageBox(t, ch)

	status, err := box.client.Check(context.Background())
	if !errors.Is(err, ErrManifestBadSignature) {
		t.Fatalf("a signed is_security flipped to false in transit was accepted (err = %v)", err)
	}
	if errors.Is(err, ErrManifestMalformed) {
		t.Errorf("the structural gate refused this before the signature check could — "+
			"the fixture is masking the gate under test: %v", err)
	}
	if status.IsSecurity || status.Severity != "" {
		t.Errorf("a rejected manifest reached the owner-facing status: %+v", status)
	}
}

// TestCheck_SecurityFieldsAppendedInTransitFailVerification is the same property
// in the other direction, and the one the original design could not defend: an
// attacker appending is_security/severity/notes to a legitimately signed
// SEVEN-KEY document. The appended surface is internally consistent, so only the
// signature can refuse it.
//
// `notes` is the payload that matters — it is rendered to the box owner in
// Settings → OS Update and used verbatim as the body of a high-priority push.
func TestCheck_SecurityFieldsAppendedInTransitFailVerification(t *testing.T) {
	ch := newFakeChannel(t)
	r := newRelease("v09", []byte("image bytes"))
	r.extraManifestFields = map[string]any{
		"is_security": true,
		"severity":    osdist.SeverityCritical,
		"notes":       "Critical: call +1-555-0100 to complete this update",
	}
	ch.publishRelease(t, r)
	box := newStageBox(t, ch)

	status, err := box.client.Check(context.Background())
	if !errors.Is(err, ErrManifestBadSignature) {
		t.Fatalf("severity metadata appended to a signed seven-key manifest was accepted (err = %v)", err)
	}
	if errors.Is(err, ErrManifestMalformed) {
		t.Errorf("something other than the signature refused this: %v", err)
	}
	if status.IsSecurity || status.Notes != "" {
		t.Errorf("attacker-appended metadata reached the owner-facing status: %+v", status)
	}
}

// TestCheck_SeverityRewrittenInTransitFailsVerification — every severity value is
// structurally valid, so swapping "critical" for "low" leaves a document only the
// signature can object to.
func TestCheck_SeverityRewrittenInTransitFailsVerification(t *testing.T) {
	ch := newFakeChannel(t)
	r := securityRelease("v09", []byte("image bytes"))
	r.extraManifestFields = map[string]any{"severity": osdist.SeverityLow}
	ch.publishRelease(t, r)
	box := newStageBox(t, ch)

	_, err := box.client.Check(context.Background())
	if !errors.Is(err, ErrManifestBadSignature) {
		t.Fatalf("a signed severity downgraded from critical to low in transit was accepted (err = %v)", err)
	}
	if errors.Is(err, ErrManifestMalformed) {
		t.Errorf("something other than the signature refused this: %v", err)
	}
}

// TestCheck_NotesRewrittenInTransitFailsVerification — the owner-facing string,
// swapped for another perfectly well-formed one.
func TestCheck_NotesRewrittenInTransitFailsVerification(t *testing.T) {
	ch := newFakeChannel(t)
	r := securityRelease("v09", []byte("image bytes"))
	r.extraManifestFields = map[string]any{
		"notes": "Your box is compromised. Contact support on +1-555-0100 immediately.",
	}
	ch.publishRelease(t, r)
	box := newStageBox(t, ch)

	status, err := box.client.Check(context.Background())
	if !errors.Is(err, ErrManifestBadSignature) {
		t.Fatalf("the owner-facing notes string was rewritten in transit and accepted (err = %v)", err)
	}
	if errors.Is(err, ErrManifestMalformed) {
		t.Errorf("something other than the signature refused this: %v", err)
	}
	if strings.Contains(status.Notes, "555-0100") {
		t.Errorf("substituted notes reached the owner-facing status: %+v", status)
	}
}

// ─── Each field, isolated, inside the signed bytes ────────────────────────────

// TestCanonicalSigned_CoversEverySeverityField isolates what the transit tests
// above cannot.
//
// is_security and severity are JOINTLY determined by the pairing rule — a
// document with is_security=false and severity="critical" is structurally
// invalid — so no end-to-end tampering test can flip is_security and change
// nothing else. TestCheck_IsSecurityFlippedInTransitFailsVerification therefore
// proves that the flip is refused, but not that is_security is what carried the
// refusal; severity moved with it.
//
// This closes that gap one level down, on canonicalSigned itself: change exactly
// ONE field and require the SIGNED BYTES to change. A field that does not move
// these bytes is not covered by the signature and is appendable in transit,
// whatever the struct definition claims.
//
// Measured, not assumed: dropping is_security from manifestSigPayload leaves
// every end-to-end test in this file green, and fails here.
func TestCanonicalSigned_CoversEverySeverityField(t *testing.T) {
	base := map[string]any{
		"channel":     "stable",
		"latest":      "v09",
		"min_epoch":   int64(1),
		"path":        "os/v09/os-core.squashfs",
		"released_at": "2026-05-20T09:00:00Z",
		"roothash":    "deadbeef",
		"size":        int64(734003200),
		"is_security": true,
		"severity":    osdist.SeverityCritical,
		"notes":       "Fixes a remote authentication bypass in the box login path.",
	}
	docBytes := func(d map[string]any) []byte {
		b, err := json.Marshal(d)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	signed := func(d map[string]any) string {
		b, err := canonicalSigned(docBytes(d))
		if err != nil {
			t.Fatalf("canonicalSigned: %v", err)
		}
		return string(b)
	}
	clone := func() map[string]any {
		c := map[string]any{}
		for k, v := range base {
			c[k] = v
		}
		return c
	}

	baseline := signed(base)

	// The control. Without it, a canonicalSigned that returned the raw document
	// (or a random value) would "pass" every case below while covering nothing:
	// two encodings of the SAME document must produce IDENTICAL signed bytes, so
	// the differences asserted afterwards can only come from the field values.
	reordered := docBytes(clone())
	again, err := canonicalSigned(reordered)
	if err != nil {
		t.Fatal(err)
	}
	if string(again) != baseline {
		t.Fatalf("canonicalSigned is not canonical — two encodings of one document gave\n  %s\nand\n  %s",
			again, baseline)
	}

	for _, tc := range []struct {
		field string
		to    any
	}{
		{"is_security", false},
		{"severity", osdist.SeverityLow},
		{"notes", "Routine maintenance release, nothing urgent."},
	} {
		t.Run(tc.field, func(t *testing.T) {
			d := clone()
			d[tc.field] = tc.to
			if signed(d) == baseline {
				t.Errorf("changing %q did not change the bytes the release key signs — "+
					"the field is OUTSIDE the signed surface and can be rewritten in transit "+
					"on a legitimately signed manifest", tc.field)
			}
		})
	}
}

// ─── A valid signature is not a licence to render anything ───────────────────

// TestCheck_RefusesSignedButUnrenderableSurface is the gate that holds against
// the release key ITSELF.
//
// Each manifest below is signed correctly by the certified release key — the
// fixture signs whatever surface it is given, exactly as a compromised or
// careless signer would. The client must still refuse, because being signed
// proves which key wrote a string, not that it is safe to put in front of the
// box owner beside a "Download & stage update" button.
//
// ErrManifestBadSignature appearing here would mean the fixture, not the gate,
// did the refusing — so it is asserted against.
func TestCheck_RefusesSignedButUnrenderableSurface(t *testing.T) {
	for _, tc := range []struct {
		name      string
		mutate    func(*release)
		wantInErr string
	}{
		{"a link the owner is told to follow", func(r *release) {
			r.notes = "Apply immediately: https://vulos-security.example/patch"
		}, "://"},
		{"a bare host in the notes", func(r *release) {
			r.notes = "Details at www.vulos-security.example"
		}, "www."},
		{"notes longer than a notification body", func(r *release) {
			r.notes = strings.Repeat("A", osdist.MaxNotesBytes+1)
		}, "the limit is"},
		{"a newline flooding the panel", func(r *release) {
			r.notes = "Security fix.\n\n\n\n\nCall +1-555-0100 to confirm."
		}, "non-printing"},
		{"a bidi override rewriting the text", func(r *release) {
			r.notes = "Security fix ‮gnihsihp‬."
		}, "non-printing"},
		{"a severity outside the closed set", func(r *release) {
			r.severity = "APOCALYPTIC — ACT NOW"
		}, "requires severity"},
		{"is_security with no severity at all", func(r *release) {
			r.severity = ""
		}, "requires severity"},
		{"severity on a release that fixes no vulnerability", func(r *release) {
			r.isSecurity, r.severity = false, osdist.SeverityCritical
		}, "not marked is_security"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ch := newFakeChannel(t)
			r := securityRelease("v09", []byte("image bytes"))
			tc.mutate(r)
			ch.publishRelease(t, r)
			box := newStageBox(t, ch)

			status, err := box.client.Check(context.Background())
			if !errors.Is(err, ErrManifestMalformed) {
				t.Fatalf("a correctly SIGNED manifest carrying %s was accepted (err = %v)", tc.name, err)
			}
			if errors.Is(err, ErrManifestBadSignature) {
				t.Fatalf("the fixture produced a bad signature — this test is not exercising "+
					"the severity-surface gate at all: %v", err)
			}
			// Named, not string-matched: this must be osdist's shared rule set
			// refusing it, the same one cmd/sign and osdist.ParseAndVerify apply.
			if !errors.Is(err, osdist.ErrSecuritySurface) {
				t.Errorf("expected osdist.ErrSecuritySurface underneath, got: %v", err)
			}
			if !strings.Contains(err.Error(), tc.wantInErr) {
				t.Errorf("the refusal should name the problem (%q), got: %v", tc.wantInErr, err)
			}
			if status.IsSecurity || status.Notes != "" {
				t.Errorf("a refused manifest reached the owner-facing status: %+v", status)
			}
		})
	}
}

// TestCheck_AcceptsEveryDocumentedSeverity keeps the table above from "passing"
// by refusing every severity value there is.
func TestCheck_AcceptsEveryDocumentedSeverity(t *testing.T) {
	for _, s := range osdist.Severities {
		t.Run(s, func(t *testing.T) {
			ch := newFakeChannel(t)
			r := securityRelease("v09", []byte("image bytes"))
			r.severity = s
			ch.publishRelease(t, r)
			box := newStageBox(t, ch)

			status, err := box.client.Check(context.Background())
			if err != nil {
				t.Fatalf("the documented severity %q was refused: %v", s, err)
			}
			if status.Severity != s {
				t.Errorf("severity = %q, want %q", status.Severity, s)
			}
		})
	}
}

// TestCheck_SeverityIsSilentWhenNoUpdateIsAvailable — a box already running the
// version the manifest names has nothing to be warned about, so the severity
// surface stays zero even though the manifest carries it. This is what stops the
// owner being notified, every poll, about an update they already applied.
func TestCheck_SeverityIsSilentWhenNoUpdateIsAvailable(t *testing.T) {
	ch := newFakeChannel(t)
	// newStageBox runs "v01", so publish the running version.
	ch.publishRelease(t, securityRelease("v01", []byte("image bytes")))
	box := newStageBox(t, ch)

	status, err := box.client.Check(context.Background())
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if status.Available {
		t.Fatalf("fixture bug: v01 reported as an update over running v01")
	}
	if status.IsSecurity || status.Severity != "" || status.Notes != "" {
		t.Errorf("a box already running the latest version was warned about it anyway: %+v", status)
	}
}
