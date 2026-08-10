package ota

// epoch_revocation_test.go — SIGN-04/SIGNING.md § Minimum Trusted Epoch: proof
// that bumping -min-epoch actually REVOKES.
//
// The mechanism was inert. AcceptEpoch only ever CHECKED the floor, and
// RaiseTo/BumpFloor had no caller anywhere outside their own unit tests — this
// package's doc comment even stated it as a design property. A device therefore
// initialised at floor 0 and stayed there for its whole life: every min_epoch
// >= 0 passed, and issuing a release cert with a higher -min-epoch revoked
// nothing. The tests below drive the real Client against a real HTTP channel
// and assert the property the roadmap promises: a box that has accepted epoch N
// refuses a cert at epoch N-1, across process restarts.

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"vulos/backend/services/signing"
)

// ─── Channel fixture ──────────────────────────────────────────────────────────

// fakeChannel serves release-cert.json / stable.json / stable.json.sig exactly
// as a real update channel does, signed with keys the test controls.
type fakeChannel struct {
	srv      *httptest.Server
	rootPriv ed25519.PrivateKey
	relPriv  ed25519.PrivateKey
	relPub   ed25519.PublicKey

	files map[string][]byte
}

func newFakeChannel(t *testing.T) *fakeChannel {
	t.Helper()
	_, rootPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	relPub, relPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ch := &fakeChannel{rootPriv: rootPriv, relPriv: relPriv, relPub: relPub, files: map[string][]byte{}}
	ch.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, ok := ch.files[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(body)
	}))
	t.Cleanup(ch.srv.Close)
	return ch
}

// anchorPath writes the channel's ROOT public key as the box's baked anchor.
func (ch *fakeChannel) anchorPath(t *testing.T, dir string) string {
	t.Helper()
	pub := ch.rootPriv.Public().(ed25519.PublicKey)
	path := filepath.Join(dir, "trust-anchor.pub")
	if err := os.WriteFile(path, []byte(base64.StdEncoding.EncodeToString(pub)+"\n"), 0o444); err != nil {
		t.Fatal(err)
	}
	return path
}

// publish installs a root-signed release cert at certEpoch and a release-signed
// manifest at manifestEpoch advertising version.
func (ch *fakeChannel) publish(t *testing.T, certEpoch, manifestEpoch int64, version string) {
	t.Helper()

	cert, err := signing.IssueReleaseCert(ch.rootPriv, ch.relPub, "release-test",
		time.Now().Add(365*24*time.Hour), certEpoch)
	if err != nil {
		t.Fatalf("IssueReleaseCert: %v", err)
	}
	certJSON, err := json.Marshal(cert)
	if err != nil {
		t.Fatal(err)
	}
	ch.files["/release-cert.json"] = certJSON

	manifest := Manifest{
		Channel:    "stable",
		Latest:     version,
		MinEpoch:   manifestEpoch,
		RootHash:   hex.EncodeToString(make([]byte, 32)),
		Size:       1024,
		ReleasedAt: time.Now().UTC().Truncate(time.Second),
		Path:       "os/" + version + "/os-core.squashfs",
	}
	manifestJSON, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := signing.Canonical(&manifest)
	if err != nil {
		t.Fatal(err)
	}
	sigFile, err := signing.MarshalSig(signing.Signature{
		Algorithm: signing.AlgorithmID,
		KeyID:     "release-test",
		SigBytes:  signing.Sign(ch.relPriv, canonical),
	})
	if err != nil {
		t.Fatal(err)
	}
	ch.files["/stable.json"] = manifestJSON
	ch.files["/stable.json.sig"] = sigFile
}

// newBox builds a Client whose epoch floor persists at epochPath — the same
// file across restarts, like a real box.
func newBox(t *testing.T, ch *fakeChannel, anchor, epochPath string) *Client {
	t.Helper()
	store, err := signing.NewEpochStore(epochPath)
	if err != nil {
		t.Fatalf("NewEpochStore: %v", err)
	}
	client, err := NewClient(ClientConfig{
		ChannelURL:     ch.srv.URL,
		AnchorPath:     anchor,
		EpochStore:     store,
		RunningVersion: "1.0.0",
		HTTPClient:     ch.srv.Client(),
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return client
}

// ─── The headline ─────────────────────────────────────────────────────────────

// TestEpochRevocation_AcceptedEpochRefusesEarlierCert is the property the whole
// mechanism exists for: after a box accepts a cert at epoch N, a cert at N-1 is
// refused. Before the floor-raising surface existed this test could not pass —
// the floor never left 0.
func TestEpochRevocation_AcceptedEpochRefusesEarlierCert(t *testing.T) {
	dir := t.TempDir()
	ch := newFakeChannel(t)
	anchor := ch.anchorPath(t, dir)
	epochPath := filepath.Join(dir, "epoch-floor.json")

	// The root retires everything below epoch 5 and publishes 2.0.0.
	ch.publish(t, 5, 5, "2.0.0")
	box := newBox(t, ch, anchor, epochPath)

	if _, err := box.Check(context.Background()); err != nil {
		t.Fatalf("first check (epoch 5) failed: %v", err)
	}

	// Seeing it must have RAISED the floor — "the highest epoch it has ever
	// seen" (roadmap/SIGNING.md).
	store, err := signing.NewEpochStore(epochPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := store.Current(); got != 5 {
		t.Fatalf("epoch floor after accepting a cert at epoch 5: got %d, want 5 — "+
			"the floor never rose, so -min-epoch revokes nothing", got)
	}

	// Now the attacker replays the RETIRED cert (epoch 4, and the release key
	// it authorises may well be the compromised one).
	ch.publish(t, 4, 4, "1.9.0-rollback")

	status, err := box.Check(context.Background())
	if err == nil {
		t.Fatal("REVOCATION FAILURE: a cert at epoch 4 was accepted by a box whose floor is 5")
	}
	if !errors.Is(err, ErrEpochRejected) {
		t.Errorf("expected ErrEpochRejected, got: %v", err)
	}
	if status.Available && status.LatestVersion == "1.9.0-rollback" {
		t.Error("the rolled-back version was reported as an available update")
	}

	// And the refusal must not have disturbed the floor.
	store2, err := signing.NewEpochStore(epochPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := store2.Current(); got != 5 {
		t.Errorf("floor after a refused rollback: got %d, want 5", got)
	}
}

// TestEpochRevocation_FloorSurvivesRestart pins that the raise is persisted,
// not merely held in memory: a fresh Client over the same file still refuses.
func TestEpochRevocation_FloorSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	ch := newFakeChannel(t)
	anchor := ch.anchorPath(t, dir)
	epochPath := filepath.Join(dir, "epoch-floor.json")

	ch.publish(t, 7, 7, "3.0.0")
	if _, err := newBox(t, ch, anchor, epochPath).Check(context.Background()); err != nil {
		t.Fatalf("check at epoch 7: %v", err)
	}

	// Reboot: brand-new Client and EpochStore over the same on-disk floor.
	ch.publish(t, 6, 6, "2.9.0")
	if _, err := newBox(t, ch, anchor, epochPath).Check(context.Background()); err == nil {
		t.Fatal("REVOCATION FAILURE: after a restart the box accepted a cert at epoch 6 with floor 7")
	}
}

// TestEpochRevocation_RetiredCertRefusedOnItsOwn isolates the CERT-side gate.
// The headline test above would still pass with that gate deleted, because its
// rolled-back manifest carries the same low epoch and the manifest check would
// catch it. Here the retired cert (epoch 4) is paired with a manifest claiming
// the current epoch, so only a check of the CERT's own min_epoch refuses it —
// which is what matters, since the cert is what authorises the release key that
// revocation is retiring.
func TestEpochRevocation_RetiredCertRefusedOnItsOwn(t *testing.T) {
	dir := t.TempDir()
	ch := newFakeChannel(t)
	anchor := ch.anchorPath(t, dir)
	epochPath := filepath.Join(dir, "epoch-floor.json")

	ch.publish(t, 5, 5, "2.0.0")
	box := newBox(t, ch, anchor, epochPath)
	if _, err := box.Check(context.Background()); err != nil {
		t.Fatalf("first check (epoch 5): %v", err)
	}

	// Retired cert, but the manifest it signs claims a current epoch.
	ch.publish(t, 4, 5, "1.9.0-rollback")
	if _, err := box.Check(context.Background()); err == nil {
		t.Fatal("REVOCATION FAILURE: a RETIRED release cert (epoch 4, floor 5) was accepted " +
			"because only the manifest's epoch was being checked")
	}
}

// TestEpochRevocation_ManifestCannotRaiseTheFloor pins the design decision: the
// floor follows the ROOT-signed cert, never the release-key-signed manifest. If
// the manifest could raise it, a compromised release key — the exact thing
// revocation exists to retire — could publish a huge min_epoch and permanently
// brick the update path, since the floor never falls.
func TestEpochRevocation_ManifestCannotRaiseTheFloor(t *testing.T) {
	dir := t.TempDir()
	ch := newFakeChannel(t)
	anchor := ch.anchorPath(t, dir)
	epochPath := filepath.Join(dir, "epoch-floor.json")

	// Cert says epoch 2; the manifest (release-key signed) claims 9_000_000.
	ch.publish(t, 2, 9_000_000, "2.0.0")
	if _, err := newBox(t, ch, anchor, epochPath).Check(context.Background()); err != nil {
		t.Fatalf("check: %v", err)
	}

	store, err := signing.NewEpochStore(epochPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := store.Current(); got != 2 {
		t.Fatalf("floor followed the RELEASE-key-signed manifest: got %d, want 2 (the root-signed cert)", got)
	}
}

// TestEpochRevocation_ForgedCertRaisesNothing — a cert not signed by the baked
// root must move nothing, even when it carries a high min_epoch. The raise is
// gated on the same root signature as everything else.
func TestEpochRevocation_ForgedCertRaisesNothing(t *testing.T) {
	dir := t.TempDir()
	ch := newFakeChannel(t)
	anchor := ch.anchorPath(t, dir)
	epochPath := filepath.Join(dir, "epoch-floor.json")

	// Re-sign the cert with an unrelated "root" key.
	_, attackerRoot, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	realRoot := ch.rootPriv
	ch.rootPriv = attackerRoot
	ch.publish(t, 999, 999, "9.9.9")
	ch.rootPriv = realRoot

	if _, err := newBox(t, ch, anchor, epochPath).Check(context.Background()); err == nil {
		t.Fatal("a cert signed by an unrelated key was accepted")
	}

	store, err := signing.NewEpochStore(epochPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := store.Current(); got != 0 {
		t.Fatalf("a forged cert moved the epoch floor to %d", got)
	}
}
