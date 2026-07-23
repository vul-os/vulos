package fleetid

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"testing"
	"time"

	"vulos/backend/services/peering"
)

// ─── Test fixtures ─────────────────────────────────────────────────────────────

// box is a test identity: an Ed25519 keypair and its Vula ID.
type box struct {
	priv   ed25519.PrivateKey
	vulaID string
}

func newBox(t *testing.T) box {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	return box{priv: priv, vulaID: peering.EncodeVulaID(pub)}
}

// testRoster is THIS box's own verified roster. It maps Vula ID → rostered key
// and, optionally, a revoked set (exercising the RevocationOracle path).
type testRoster struct {
	members map[string]ed25519.PublicKey
	revoked map[string]bool
}

func newRoster(boxes ...box) *testRoster {
	r := &testRoster{members: map[string]ed25519.PublicKey{}, revoked: map[string]bool{}}
	for _, b := range boxes {
		r.members[b.vulaID] = b.priv.Public().(ed25519.PublicKey)
	}
	return r
}

func (r *testRoster) IsRostered(vulaID string) (ed25519.PublicKey, bool) {
	k, ok := r.members[vulaID]
	return k, ok
}

func (r *testRoster) IsRevoked(vulaID string) bool { return r.revoked[vulaID] }

func hash(s string) []byte {
	h := sha256.Sum256([]byte(s))
	return h[:]
}

// vouch builds a valid signed vouch cert from voucher for subject over payload.
func vouch(t *testing.T, voucher box, action, subjectID string, payload []byte, now time.Time) VouchCert {
	t.Helper()
	c, err := NewVouchCert(voucher.priv, action, subjectID, payload, now)
	if err != nil {
		t.Fatalf("NewVouchCert: %v", err)
	}
	return *c
}

// ─── Happy path ────────────────────────────────────────────────────────────────

func TestHappyPath_ThreeDistinctVouchers_Pass(t *testing.T) {
	subject := newBox(t)
	v1, v2, v3 := newBox(t), newBox(t), newBox(t)
	roster := newRoster(v1, v2, v3) // subject need not be rostered to be a subject
	now := time.Now().UTC()
	pl := hash("enroll-device-XYZ")

	certs := []VouchCert{
		vouch(t, v1, ActionDeviceEnroll, subject.vulaID, pl, now),
		vouch(t, v2, ActionDeviceEnroll, subject.vulaID, pl, now),
		vouch(t, v3, ActionDeviceEnroll, subject.vulaID, pl, now),
	}

	res, err := VerifyQuorum(ActionDeviceEnroll, subject.vulaID, pl, certs, roster, 2, now)
	if err != nil {
		t.Fatalf("expected pass, got err: %v", err)
	}
	if !res.OK || res.DistinctValid != 3 {
		t.Fatalf("expected OK with 3 distinct valid, got OK=%v distinct=%d", res.OK, res.DistinctValid)
	}
	if len(res.Counted) != 3 {
		t.Fatalf("expected 3 counted, got %v", res.Counted)
	}
	if len(res.Dropped) != 0 {
		t.Fatalf("expected 0 dropped, got %v", res.Dropped)
	}
}

// ─── The hard rule: self-vouch ─────────────────────────────────────────────────

func TestSelfVouch_RejectedEvenWithValidSignature(t *testing.T) {
	subject := newBox(t)
	// subject signs a vouch FOR ITSELF with its own real key — a perfectly valid
	// signature. It must never count.
	roster := newRoster(subject) // even if the subject is somehow in its own roster
	now := time.Now().UTC()
	pl := hash("ssh-backdoor-request")

	selfCert := vouch(t, subject, ActionSSHRecovery, subject.vulaID, pl, now)
	// Sanity: the signature really is valid against the subject's own key.
	if err := verifyCanonical(selfCert, selfCert.Sig, subject.priv.Public().(ed25519.PublicKey)); err != nil {
		t.Fatalf("precondition: self-cert signature should be valid: %v", err)
	}

	res, err := VerifyQuorum(ActionSSHRecovery, subject.vulaID, pl, []VouchCert{selfCert}, roster, 2, now)
	if !errors.Is(err, ErrInsufficientQuorum) {
		t.Fatalf("self-vouch must not reach quorum; got err=%v", err)
	}
	if res.DistinctValid != 0 {
		t.Fatalf("self-vouch must contribute 0; got %d", res.DistinctValid)
	}
	if len(res.Dropped) != 1 || res.Dropped[0].Reason != DropSelfVouch {
		t.Fatalf("expected 1 drop with self-vouch reason, got %v", res.Dropped)
	}
}

func TestSelfVouch_CannotTipQuorum(t *testing.T) {
	// One genuine other voucher + a self-vouch must NOT reach threshold 2.
	subject := newBox(t)
	v1 := newBox(t)
	roster := newRoster(subject, v1)
	now := time.Now().UTC()
	pl := hash("identity-recovery")

	certs := []VouchCert{
		vouch(t, v1, ActionIdentityRecovery, subject.vulaID, pl, now),
		vouch(t, subject, ActionIdentityRecovery, subject.vulaID, pl, now), // self
	}
	res, err := VerifyQuorum(ActionIdentityRecovery, subject.vulaID, pl, certs, roster, 2, now)
	if !errors.Is(err, ErrInsufficientQuorum) {
		t.Fatalf("self-vouch must not tip quorum; got err=%v", err)
	}
	if res.DistinctValid != 1 {
		t.Fatalf("only v1 should count; got %d", res.DistinctValid)
	}
}

// ─── Distinct dedupe ───────────────────────────────────────────────────────────

func TestIdenticalCertsFromOneVoucher_CountAsOne(t *testing.T) {
	subject := newBox(t)
	v1, v2 := newBox(t), newBox(t)
	roster := newRoster(v1, v2)
	now := time.Now().UTC()
	pl := hash("payload")

	c1 := vouch(t, v1, ActionDeviceEnroll, subject.vulaID, pl, now)
	certs := []VouchCert{c1, c1, c1, c1, c1} // five identical from v1

	res, err := VerifyQuorum(ActionDeviceEnroll, subject.vulaID, pl, certs, roster, 2, now)
	if !errors.Is(err, ErrInsufficientQuorum) {
		t.Fatalf("5 identical from ONE voucher must count as 1 (< threshold 2); got err=%v", err)
	}
	if res.DistinctValid != 1 {
		t.Fatalf("expected distinct 1, got %d", res.DistinctValid)
	}
	dupes := 0
	for _, d := range res.Dropped {
		if d.Reason == DropDuplicate {
			dupes++
		}
	}
	if dupes != 4 {
		t.Fatalf("expected 4 duplicate drops, got %d (%v)", dupes, res.Dropped)
	}

	// Distinct second voucher then tips it to 2.
	certs = append(certs, vouch(t, v2, ActionDeviceEnroll, subject.vulaID, pl, now))
	res, err = VerifyQuorum(ActionDeviceEnroll, subject.vulaID, pl, certs, roster, 2, now)
	if err != nil || !res.OK || res.DistinctValid != 2 {
		t.Fatalf("two distinct vouchers should pass; OK=%v distinct=%d err=%v", res.OK, res.DistinctValid, err)
	}
}

// ─── Roster / revocation / signature drops ──────────────────────────────────────

func TestUnrosteredVoucher_Dropped(t *testing.T) {
	subject := newBox(t)
	v1, v2 := newBox(t), newBox(t)
	outsider := newBox(t)
	roster := newRoster(v1, v2) // outsider NOT rostered
	now := time.Now().UTC()
	pl := hash("p")

	certs := []VouchCert{
		vouch(t, v1, ActionSSHRecovery, subject.vulaID, pl, now),
		vouch(t, outsider, ActionSSHRecovery, subject.vulaID, pl, now),
	}
	res, err := VerifyQuorum(ActionSSHRecovery, subject.vulaID, pl, certs, roster, 2, now)
	if !errors.Is(err, ErrInsufficientQuorum) {
		t.Fatalf("unrostered voucher must not count; got err=%v", err)
	}
	if res.DistinctValid != 1 {
		t.Fatalf("only rostered v1 counts; got %d", res.DistinctValid)
	}
	if !hasDrop(res, DropUnrostered) {
		t.Fatalf("expected an unrostered drop, got %v", res.Dropped)
	}
}

func TestRevokedVoucher_Dropped(t *testing.T) {
	subject := newBox(t)
	v1, v2 := newBox(t), newBox(t)
	roster := newRoster(v1, v2)
	roster.revoked[v2.vulaID] = true // v2 revoked
	now := time.Now().UTC()
	pl := hash("p")

	certs := []VouchCert{
		vouch(t, v1, ActionDeviceEnroll, subject.vulaID, pl, now),
		vouch(t, v2, ActionDeviceEnroll, subject.vulaID, pl, now),
	}
	res, err := VerifyQuorum(ActionDeviceEnroll, subject.vulaID, pl, certs, roster, 2, now)
	if !errors.Is(err, ErrInsufficientQuorum) {
		t.Fatalf("revoked voucher must not count; got err=%v", err)
	}
	if res.DistinctValid != 1 || !hasDrop(res, DropRevoked) {
		t.Fatalf("expected 1 valid + a revoked drop; distinct=%d dropped=%v", res.DistinctValid, res.Dropped)
	}
}

func TestBadSignature_Dropped(t *testing.T) {
	subject := newBox(t)
	v1, v2 := newBox(t), newBox(t)
	roster := newRoster(v1, v2)
	now := time.Now().UTC()
	pl := hash("p")

	c1 := vouch(t, v1, ActionDeviceEnroll, subject.vulaID, pl, now)
	tampered := vouch(t, v2, ActionDeviceEnroll, subject.vulaID, pl, now)
	// Flip the signature bytes.
	raw, _ := base64.RawURLEncoding.DecodeString(tampered.Sig)
	raw[0] ^= 0xFF
	tampered.Sig = base64.RawURLEncoding.EncodeToString(raw)

	res, err := VerifyQuorum(ActionDeviceEnroll, subject.vulaID, pl, []VouchCert{c1, tampered}, roster, 2, now)
	if !errors.Is(err, ErrInsufficientQuorum) {
		t.Fatalf("bad signature must not count; got err=%v", err)
	}
	if res.DistinctValid != 1 || !hasDrop(res, DropBadSignature) {
		t.Fatalf("expected 1 valid + bad-signature drop; distinct=%d dropped=%v", res.DistinctValid, res.Dropped)
	}
}

func TestForgedRosterKeyMismatch_Dropped(t *testing.T) {
	// A cert claims a rostered voucher's Vula ID but the roster's key for that ID
	// is a DIFFERENT key (impersonation attempt). Signature would verify against
	// the claimed/embedded key but not the roster's authoritative key → dropped.
	subject := newBox(t)
	v1 := newBox(t)
	// Roster maps v1's Vula ID to a totally different public key.
	other := newBox(t)
	roster := &testRoster{
		members: map[string]ed25519.PublicKey{v1.vulaID: other.priv.Public().(ed25519.PublicKey)},
		revoked: map[string]bool{},
	}
	now := time.Now().UTC()
	pl := hash("p")
	c := vouch(t, v1, ActionSSHRecovery, subject.vulaID, pl, now)

	res, err := VerifyQuorum(ActionSSHRecovery, subject.vulaID, pl, []VouchCert{c}, roster, 2, now)
	if !errors.Is(err, ErrInsufficientQuorum) {
		t.Fatalf("roster-key mismatch must not count; got err=%v", err)
	}
	if !hasDrop(res, DropRosterKeyMismatch) {
		t.Fatalf("expected roster-key-mismatch drop, got %v", res.Dropped)
	}
}

// ─── Binding: wrong action / subject / payload ─────────────────────────────────

func TestWrongAction_Dropped(t *testing.T) {
	subject := newBox(t)
	v1, v2 := newBox(t), newBox(t)
	roster := newRoster(v1, v2)
	now := time.Now().UTC()
	pl := hash("p")

	// v2's cert is for a DIFFERENT action than the request.
	certs := []VouchCert{
		vouch(t, v1, ActionIdentityRecovery, subject.vulaID, pl, now),
		vouch(t, v2, ActionDeviceEnroll, subject.vulaID, pl, now),
	}
	res, _ := VerifyQuorum(ActionIdentityRecovery, subject.vulaID, pl, certs, roster, 2, now)
	if res.DistinctValid != 1 || !hasDrop(res, DropWrongAction) {
		t.Fatalf("wrong-action cert must not count; distinct=%d dropped=%v", res.DistinctValid, res.Dropped)
	}
}

func TestWrongSubject_Dropped_ReplayForDifferentSubjectFails(t *testing.T) {
	// A compromised box replays a genuine cert that vouched for ANOTHER subject.
	subjectA := newBox(t)
	subjectB := newBox(t)
	v1, v2 := newBox(t), newBox(t)
	roster := newRoster(v1, v2)
	now := time.Now().UTC()
	pl := hash("p")

	// Both certs were legitimately issued for subjectB; attacker replays them to
	// authorize subjectA.
	replayed := []VouchCert{
		vouch(t, v1, ActionSSHRecovery, subjectB.vulaID, pl, now),
		vouch(t, v2, ActionSSHRecovery, subjectB.vulaID, pl, now),
	}
	res, err := VerifyQuorum(ActionSSHRecovery, subjectA.vulaID, pl, replayed, roster, 2, now)
	if !errors.Is(err, ErrInsufficientQuorum) {
		t.Fatalf("replay for different subject must fail; got err=%v", err)
	}
	if res.DistinctValid != 0 {
		t.Fatalf("no cert should count for subjectA; got %d", res.DistinctValid)
	}
	if drops := countDrops(res, DropWrongSubject); drops != 2 {
		t.Fatalf("expected 2 wrong-subject drops, got %d (%v)", drops, res.Dropped)
	}
}

func TestWrongPayloadHash_Dropped(t *testing.T) {
	subject := newBox(t)
	v1, v2 := newBox(t), newBox(t)
	roster := newRoster(v1, v2)
	now := time.Now().UTC()
	plWant := hash("the-real-payload")
	plOther := hash("attacker-payload")

	certs := []VouchCert{
		vouch(t, v1, ActionDeviceEnroll, subject.vulaID, plWant, now),
		vouch(t, v2, ActionDeviceEnroll, subject.vulaID, plOther, now), // bound to other payload
	}
	res, _ := VerifyQuorum(ActionDeviceEnroll, subject.vulaID, plWant, certs, roster, 2, now)
	if res.DistinctValid != 1 || !hasDrop(res, DropWrongPayload) {
		t.Fatalf("wrong-payload cert must not count; distinct=%d dropped=%v", res.DistinctValid, res.Dropped)
	}
}

// ─── Freshness ─────────────────────────────────────────────────────────────────

func TestExpiredVouch_Dropped(t *testing.T) {
	subject := newBox(t)
	v1, v2 := newBox(t), newBox(t)
	roster := newRoster(v1, v2)
	now := time.Now().UTC()
	pl := hash("p")

	stale := vouch(t, v1, ActionDeviceEnroll, subject.vulaID, pl, now.Add(-VouchMaxAge-time.Minute))
	fresh := vouch(t, v2, ActionDeviceEnroll, subject.vulaID, pl, now)

	res, err := VerifyQuorum(ActionDeviceEnroll, subject.vulaID, pl, []VouchCert{stale, fresh}, roster, 2, now)
	if !errors.Is(err, ErrInsufficientQuorum) {
		t.Fatalf("expired vouch must not count; got err=%v", err)
	}
	if res.DistinctValid != 1 || !hasDrop(res, DropExpired) {
		t.Fatalf("expected 1 valid + expired drop; distinct=%d dropped=%v", res.DistinctValid, res.Dropped)
	}
}

func TestFutureVouch_Dropped(t *testing.T) {
	subject := newBox(t)
	v1 := newBox(t)
	roster := newRoster(v1)
	now := time.Now().UTC()
	pl := hash("p")

	future := vouch(t, v1, ActionDeviceEnroll, subject.vulaID, pl, now.Add(ClockSkew+time.Minute))
	res, _ := VerifyQuorum(ActionDeviceEnroll, subject.vulaID, pl, []VouchCert{future}, roster, 2, now)
	if !hasDrop(res, DropNotYetValid) {
		t.Fatalf("future-dated vouch must be dropped as not-yet-valid; got %v", res.Dropped)
	}
}

// ─── Threshold ─────────────────────────────────────────────────────────────────

func TestThresholdEnforced_TwoPassesOneFails(t *testing.T) {
	subject := newBox(t)
	v1, v2 := newBox(t), newBox(t)
	roster := newRoster(v1, v2)
	now := time.Now().UTC()
	pl := hash("p")

	// One valid voucher at threshold 2 → fail.
	res, err := VerifyQuorum(ActionDeviceEnroll, subject.vulaID, pl,
		[]VouchCert{vouch(t, v1, ActionDeviceEnroll, subject.vulaID, pl, now)}, roster, 2, now)
	if !errors.Is(err, ErrInsufficientQuorum) || res.OK {
		t.Fatalf("1 valid at threshold 2 must fail; OK=%v err=%v", res.OK, err)
	}

	// Two valid distinct vouchers at threshold 2 → pass.
	res, err = VerifyQuorum(ActionDeviceEnroll, subject.vulaID, pl, []VouchCert{
		vouch(t, v1, ActionDeviceEnroll, subject.vulaID, pl, now),
		vouch(t, v2, ActionDeviceEnroll, subject.vulaID, pl, now),
	}, roster, 2, now)
	if err != nil || !res.OK {
		t.Fatalf("2 valid at threshold 2 must pass; OK=%v err=%v", res.OK, err)
	}
}

func TestThresholdFloor_CallerPassingOneIsRaisedToTwo(t *testing.T) {
	subject := newBox(t)
	v1, v2 := newBox(t), newBox(t)
	roster := newRoster(v1, v2)
	now := time.Now().UTC()
	pl := hash("p")

	// Caller passes threshold 1 — must be raised to 2. One valid voucher fails.
	res, err := VerifyQuorum(ActionSSHRecovery, subject.vulaID, pl,
		[]VouchCert{vouch(t, v1, ActionSSHRecovery, subject.vulaID, pl, now)}, roster, 1, now)
	if res.Threshold != MinThreshold {
		t.Fatalf("threshold floor not applied; effective threshold=%d", res.Threshold)
	}
	if !errors.Is(err, ErrInsufficientQuorum) || res.OK {
		t.Fatalf("1 valid at caller-threshold 1 (floored to 2) must fail; OK=%v err=%v", res.OK, err)
	}

	// And two distinct valid pass despite caller passing 1.
	res, err = VerifyQuorum(ActionSSHRecovery, subject.vulaID, pl, []VouchCert{
		vouch(t, v1, ActionSSHRecovery, subject.vulaID, pl, now),
		vouch(t, v2, ActionSSHRecovery, subject.vulaID, pl, now),
	}, roster, 1, now)
	if err != nil || !res.OK || res.Threshold != 2 {
		t.Fatalf("2 distinct valid at floored threshold 2 must pass; OK=%v threshold=%d err=%v", res.OK, res.Threshold, err)
	}
}

func TestThresholdFloor_ZeroAlsoRaisedToTwo(t *testing.T) {
	subject := newBox(t)
	v1 := newBox(t)
	roster := newRoster(v1)
	now := time.Now().UTC()
	pl := hash("p")
	res, err := VerifyQuorum(ActionSSHRecovery, subject.vulaID, pl,
		[]VouchCert{vouch(t, v1, ActionSSHRecovery, subject.vulaID, pl, now)}, roster, 0, now)
	if res.Threshold != 2 || !errors.Is(err, ErrInsufficientQuorum) {
		t.Fatalf("threshold 0 must be raised to 2 and 1 voucher must fail; threshold=%d err=%v", res.Threshold, err)
	}
}

// ─── Small-fleet policy ────────────────────────────────────────────────────────

func TestSmallFleet_ZeroOtherBoxes_InsufficientQuorum(t *testing.T) {
	subject := newBox(t)
	roster := newRoster() // empty roster: a 1-box fleet
	now := time.Now().UTC()
	pl := hash("p")

	res, err := VerifyQuorum(ActionIdentityRecovery, subject.vulaID, pl, nil, roster, 2, now)
	if !errors.Is(err, ErrInsufficientQuorum) || res.OK {
		t.Fatalf("1-box fleet must return insufficient-quorum; OK=%v err=%v", res.OK, err)
	}
	if res.DistinctValid != 0 {
		t.Fatalf("expected 0 distinct valid, got %d", res.DistinctValid)
	}
}

func TestSmallFleet_OneOtherBox_CannotMeetQuorum(t *testing.T) {
	// A 2-box fleet: even a perfect vouch from the single OTHER box cannot meet the
	// floor of 2 — by design the caller must fall back to the mnemonic anchor.
	subject := newBox(t)
	v1 := newBox(t)
	roster := newRoster(v1)
	now := time.Now().UTC()
	pl := hash("p")

	res, err := VerifyQuorum(ActionIdentityRecovery, subject.vulaID, pl,
		[]VouchCert{vouch(t, v1, ActionIdentityRecovery, subject.vulaID, pl, now)}, roster, 2, now)
	if !errors.Is(err, ErrInsufficientQuorum) || res.OK {
		t.Fatalf("2-box fleet cannot meet quorum; OK=%v err=%v", res.OK, err)
	}
	if res.DistinctValid != 1 {
		t.Fatalf("the single other box's vouch is valid but 1 < 2; got distinct=%d", res.DistinctValid)
	}
}

// ─── Misuse ────────────────────────────────────────────────────────────────────

func TestInvalidRequest_NotAFallback(t *testing.T) {
	subject := newBox(t)
	roster := newRoster()
	now := time.Now().UTC()
	pl := hash("p")

	cases := []struct {
		name    string
		action  string
		subject string
		payload []byte
		roster  Roster
	}{
		{"unknown action", "reboot", subject.vulaID, pl, roster},
		{"empty action", "", subject.vulaID, pl, roster},
		{"bad subject", ActionDeviceEnroll, "not-a-vula-id", pl, roster},
		{"empty payload", ActionDeviceEnroll, subject.vulaID, nil, roster},
		{"nil roster", ActionDeviceEnroll, subject.vulaID, pl, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := VerifyQuorum(tc.action, tc.subject, tc.payload, nil, tc.roster, 2, now)
			if !errors.Is(err, ErrInvalidRequest) {
				t.Fatalf("expected ErrInvalidRequest, got %v", err)
			}
			if errors.Is(err, ErrInsufficientQuorum) {
				t.Fatalf("misuse must NOT masquerade as insufficient-quorum (would trigger silent fallback)")
			}
		})
	}
}

// ─── Mixed adversarial soup ────────────────────────────────────────────────────

func TestMixedAdversarial_OnlyGenuineDistinctCount(t *testing.T) {
	// A realistic attack bundle: a self-vouch, an unrostered outsider, a revoked
	// peer, a wrong-action cert, an expired cert, a bad signature, three duplicates
	// of one genuine voucher, plus TWO genuine distinct vouchers. Only the two
	// genuine distinct vouchers count → passes at threshold 2, and every dropped
	// cert is attributed.
	subject := newBox(t)
	good1, good2 := newBox(t), newBox(t)
	revokedBox := newBox(t)
	outsider := newBox(t)
	now := time.Now().UTC()
	pl := hash("payload")

	roster := newRoster(good1, good2, revokedBox)
	roster.revoked[revokedBox.vulaID] = true

	bad := vouch(t, good2, ActionSSHRecovery, subject.vulaID, pl, now)
	raw, _ := base64.RawURLEncoding.DecodeString(bad.Sig)
	raw[5] ^= 0xFF
	bad.Sig = base64.RawURLEncoding.EncodeToString(raw)

	certs := []VouchCert{
		vouch(t, subject, ActionSSHRecovery, subject.vulaID, pl, now),                             // self → drop
		vouch(t, outsider, ActionSSHRecovery, subject.vulaID, pl, now),                            // unrostered → drop
		vouch(t, revokedBox, ActionSSHRecovery, subject.vulaID, pl, now),                          // revoked → drop
		vouch(t, good1, ActionDeviceEnroll, subject.vulaID, pl, now),                              // wrong action → drop
		vouch(t, good1, ActionSSHRecovery, subject.vulaID, pl, now.Add(-VouchMaxAge-time.Minute)), // expired → drop
		bad, // bad signature → drop
		vouch(t, good1, ActionSSHRecovery, subject.vulaID, pl, now), // GENUINE good1
		vouch(t, good1, ActionSSHRecovery, subject.vulaID, pl, now), // dup of good1 → drop
		vouch(t, good1, ActionSSHRecovery, subject.vulaID, pl, now), // dup of good1 → drop
		vouch(t, good2, ActionSSHRecovery, subject.vulaID, pl, now), // GENUINE good2
	}

	res, err := VerifyQuorum(ActionSSHRecovery, subject.vulaID, pl, certs, roster, 2, now)
	if err != nil || !res.OK {
		t.Fatalf("two genuine distinct vouchers should pass; OK=%v err=%v dropped=%v", res.OK, err, res.Dropped)
	}
	if res.DistinctValid != 2 {
		t.Fatalf("expected exactly 2 distinct valid (good1, good2); got %d (%v)", res.DistinctValid, res.Counted)
	}
	// Confirm each expected drop reason appeared.
	for _, want := range []string{DropSelfVouch, DropUnrostered, DropRevoked, DropWrongAction, DropExpired, DropBadSignature, DropDuplicate} {
		if !hasDrop(res, want) {
			t.Fatalf("expected a %q drop, got %v", want, res.Dropped)
		}
	}
}

// ─── helpers ───────────────────────────────────────────────────────────────────

func hasDrop(res Result, reason string) bool {
	for _, d := range res.Dropped {
		if d.Reason == reason {
			return true
		}
	}
	return false
}

func countDrops(res Result, reason string) int {
	n := 0
	for _, d := range res.Dropped {
		if d.Reason == reason {
			n++
		}
	}
	return n
}
