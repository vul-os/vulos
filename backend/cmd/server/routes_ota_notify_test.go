package main

// routes_ota_notify_test.go — the security-update notification, end to end.
//
// The claim in routes_ota.go's header — "a NEW security update
// (manifest.is_security ...) fires exactly ONE priority notification to the
// owner" — was untrue for the whole life of the file, and not by a little.
// `cmd/sign sign-manifest` signed a seven-field payload with no is_security in
// it, so ota.Check had nothing authenticated to set UpdateStatus.IsSecurity
// from and left it permanently false. onSecurityCheck's first line returned
// early on every poll of every box. Nothing tested it, so nothing said so.
//
// These tests run the REAL chain rather than a hand-made UpdateStatus, because a
// hand-made one would prove only that a struct literal with IsSecurity:true
// reaches the notifier — which was never in doubt, and is exactly the
// mock-shaped test that let the defect survive:
//
//	root key ─▶ release cert ─▶ release key ─▶ signed stable.json over HTTP
//	   └─ pinned as the box's baked anchor        │
//	                                             ▼
//	                             ota.Client.Check ─▶ UpdateStatus
//	                                             ▼
//	                             onSecurityCheck ─▶ notify.Service
//
// The manifest is built from osdist.StableManifest and signed over its own
// Canonical() bytes — the real published shape, not a local restatement of it.
// That the OTHER verifier (services/ota) then accepts those bytes is itself a
// cross-check between the two verification models.

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"vulos/backend/services/notify"
	"vulos/backend/services/osdist"
	"vulos/backend/services/ota"
	"vulos/backend/services/signing"
)

const otaTestOwner = "owner-user-id"

// otaNotifyChannel is a real update channel: a root key the box pins, a
// root-signed release cert, and a release-key-signed stable.json served over
// HTTP.
type otaNotifyChannel struct {
	srv      *httptest.Server
	rootPriv ed25519.PrivateKey
	relPriv  ed25519.PrivateKey
	relPub   ed25519.PublicKey
	files    map[string][]byte
}

func newOTANotifyChannel(t *testing.T) *otaNotifyChannel {
	t.Helper()
	_, rootPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	relPub, relPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ch := &otaNotifyChannel{rootPriv: rootPriv, relPriv: relPriv, relPub: relPub, files: map[string][]byte{}}
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

// publish signs m with the release key and serves it, alongside a root-signed
// cert authorising that key. tamper, when non-nil, rewrites the served document
// AFTER signing and leaves the .sig alone — an attacker on the wire.
func (ch *otaNotifyChannel) publish(t *testing.T, m osdist.StableManifest, tamper func(map[string]any)) {
	t.Helper()

	cert, err := signing.IssueReleaseCert(ch.rootPriv, ch.relPub, "release-test",
		time.Now().Add(365*24*time.Hour), 1)
	if err != nil {
		t.Fatal(err)
	}
	certJSON, err := json.Marshal(cert)
	if err != nil {
		t.Fatal(err)
	}
	ch.files["/release-cert.json"] = certJSON

	// The canonical bytes ARE the published document, so the signature covers
	// exactly what the channel hands over.
	doc, err := m.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	sigData, err := signing.MarshalSig(signing.Signature{
		Algorithm: signing.AlgorithmID,
		KeyID:     "release-test",
		SigBytes:  signing.Sign(ch.relPriv, doc),
	})
	if err != nil {
		t.Fatal(err)
	}

	served := doc
	if tamper != nil {
		var raw map[string]any
		if err := json.Unmarshal(doc, &raw); err != nil {
			t.Fatal(err)
		}
		tamper(raw)
		if served, err = json.Marshal(raw); err != nil {
			t.Fatal(err)
		}
	}
	ch.files["/stable.json"] = served
	ch.files["/stable.json.sig"] = sigData
}

// newOTANotifyClient builds a real ota.Client that pins ch's root key.
func newOTANotifyClient(t *testing.T, ch *otaNotifyChannel) *ota.Client {
	t.Helper()
	dir := t.TempDir()

	anchorPath := filepath.Join(dir, "trust-anchor.pub")
	rootPub := ch.rootPriv.Public().(ed25519.PublicKey)
	if err := os.WriteFile(anchorPath, []byte(base64.StdEncoding.EncodeToString(rootPub)+"\n"), 0o444); err != nil {
		t.Fatal(err)
	}
	store, err := signing.NewEpochStore(filepath.Join(dir, "epoch-floor.json"))
	if err != nil {
		t.Fatal(err)
	}
	client, err := ota.NewClient(ota.ClientConfig{
		ChannelURL: ch.srv.URL,
		AnchorPath: anchorPath,
		// A path that does not exist, so a channel serving no cert fails at the
		// fetch rather than silently reading whatever is installed on the machine
		// running the suite.
		ReleaseCertPath: filepath.Join(dir, "no-such-release-cert.json"),
		EpochStore:      store,
		RunningVersion:  "v01",
		HTTPClient:      ch.srv.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return client
}

// securityManifest is a genuinely-critical signed security release of version.
func otaSecurityManifest(version string) osdist.StableManifest {
	return osdist.StableManifest{
		Channel:    "stable",
		Latest:     version,
		MinEpoch:   1,
		RootHash:   "abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890",
		Size:       734003200,
		ReleasedAt: time.Date(2026, 5, 20, 9, 0, 0, 0, time.UTC),
		Path:       osdist.VersionPath(version),
		IsSecurity: true,
		Severity:   osdist.SeverityCritical,
		Notes:      "Fixes a remote authentication bypass in the box login path.",
	}
}

func otaPlainManifest(version string) osdist.StableManifest {
	m := otaSecurityManifest(version)
	m.IsSecurity, m.Severity, m.Notes = false, "", ""
	return m
}

// ownerNotifications returns the OS-update notifications the owner can see.
func ownerNotifications(svc *notify.Service) []notify.Notification {
	var out []notify.Notification
	for _, n := range svc.ListForUser(otaTestOwner, 0) {
		if n.Source == "os-update" {
			out = append(out, n)
		}
	}
	return out
}

// checkAndNotify runs one full poll: verify the channel, then hand the resulting
// status to the REAL onSecurityCheck callback.
func checkAndNotify(t *testing.T, client *ota.Client, onCheck func(ota.UpdateStatus)) ota.UpdateStatus {
	t.Helper()
	status, _ := client.Check(context.Background()) // error is folded into status.LastError
	onCheck(status)
	return status
}

// ─── It fires ─────────────────────────────────────────────────────────────────

// TestSecurityNotification_FiresForASignedSecurityRelease is the end-to-end
// claim, and the one that could not have passed before is_security was inside
// the signature: a genuinely signed critical release reaches the box owner as a
// single urgent, owner-targeted push.
func TestSecurityNotification_FiresForASignedSecurityRelease(t *testing.T) {
	ch := newOTANotifyChannel(t)
	m := otaSecurityManifest("v09")
	ch.publish(t, m, nil)

	svc := notify.New()
	onCheck := onSecurityCheck(svc, func() string { return otaTestOwner })
	status := checkAndNotify(t, newOTANotifyClient(t, ch), onCheck)

	if status.LastError != "" {
		t.Fatalf("verification failed, so the notification path was never reached: %s", status.LastError)
	}
	if !status.IsSecurity {
		t.Fatalf("a signed is_security did not reach UpdateStatus — status = %+v", status)
	}

	got := ownerNotifications(svc)
	if len(got) != 1 {
		t.Fatalf("owner received %d OS-update notifications, want exactly 1", len(got))
	}
	n := got[0]
	if n.Level != notify.LevelUrgent {
		t.Errorf("level = %q, want %q — a security update must outrank routine noise", n.Level, notify.LevelUrgent)
	}
	// SendNotification does NOT derive Priority from Level (only notify.Send
	// does), so an unset Priority is recorded as PriorityNormal — which is what
	// this alert was doing while its own comment claimed PriorityHigh. DND's
	// priority mode passed it anyway on the LevelUrgent fallback, so nothing
	// looked wrong; every consumer reading Priority saw a routine notification.
	if n.Priority != notify.PriorityHigh {
		t.Errorf("priority = %q, want %q — the security alert is being recorded as routine",
			n.Priority, notify.PriorityHigh)
	}
	if n.UserID != otaTestOwner {
		t.Errorf("user_id = %q, want the box owner %q — an untargeted notification is not web-pushed",
			n.UserID, otaTestOwner)
	}
	body, _ := n.Body.(string)
	if !strings.Contains(body, "v09") {
		t.Errorf("body does not name the version: %q", body)
	}
	if !strings.Contains(body, m.Notes) {
		t.Errorf("the SIGNED release notes did not reach the owner: body = %q, notes = %q", body, m.Notes)
	}
}

// TestSecurityNotification_FiresOncePerVersion — the anti-spam guard. A box polls
// every four hours; the owner must be told once, not thirty times a week.
func TestSecurityNotification_FiresOncePerVersion(t *testing.T) {
	ch := newOTANotifyChannel(t)
	ch.publish(t, otaSecurityManifest("v09"), nil)

	svc := notify.New()
	onCheck := onSecurityCheck(svc, func() string { return otaTestOwner })
	client := newOTANotifyClient(t, ch)

	for i := 0; i < 3; i++ {
		checkAndNotify(t, client, onCheck)
	}
	if got := ownerNotifications(svc); len(got) != 1 {
		t.Fatalf("three polls of the same security release produced %d notifications, want 1", len(got))
	}

	// A NEW security version must fire again — otherwise "once per version"
	// would be indistinguishable from "once ever", and the guard would silence
	// the next real vulnerability.
	ch.publish(t, otaSecurityManifest("v10"), nil)
	checkAndNotify(t, client, onCheck)
	if got := ownerNotifications(svc); len(got) != 2 {
		t.Fatalf("a NEW security version produced %d notifications in total, want 2", len(got))
	}
}

// ─── It does not fire ─────────────────────────────────────────────────────────

// TestSecurityNotification_SilentForAnOrdinaryRelease — an available but routine
// update is visible in the status endpoint and pushes nothing. Without this,
// "it fires" could be true because it fires for everything.
func TestSecurityNotification_SilentForAnOrdinaryRelease(t *testing.T) {
	ch := newOTANotifyChannel(t)
	ch.publish(t, otaPlainManifest("v09"), nil)

	svc := notify.New()
	onCheck := onSecurityCheck(svc, func() string { return otaTestOwner })
	status := checkAndNotify(t, newOTANotifyClient(t, ch), onCheck)

	if status.LastError != "" {
		t.Fatalf("an ordinary release failed verification: %s", status.LastError)
	}
	if !status.Available {
		t.Fatalf("v09 over running v01 was not reported as available: %+v", status)
	}
	if got := ownerNotifications(svc); len(got) != 0 {
		t.Fatalf("an ordinary release pushed %d notifications to the owner", len(got))
	}
}

// TestSecurityNotification_SilentWhenIsSecurityWasAppendedInTransit is the
// attack this whole change exists to make impossible.
//
// The channel serves a legitimately signed ORDINARY release with is_security,
// severity and notes appended on the wire — the .sig untouched. Before these
// fields were signed, that was a free channel into a high-priority push the box
// owner reads and acts on. Now the manifest fails verification, so no status is
// produced and nothing is sent.
func TestSecurityNotification_SilentWhenIsSecurityWasAppendedInTransit(t *testing.T) {
	ch := newOTANotifyChannel(t)
	ch.publish(t, otaPlainManifest("v09"), func(doc map[string]any) {
		doc["is_security"] = true
		doc["severity"] = osdist.SeverityCritical
		doc["notes"] = "Your box is compromised. Call +1-555-0100 immediately."
	})

	svc := notify.New()
	onCheck := onSecurityCheck(svc, func() string { return otaTestOwner })
	status := checkAndNotify(t, newOTANotifyClient(t, ch), onCheck)

	if status.LastError == "" {
		t.Fatal("a manifest with appended severity metadata VERIFIED — the fields are not " +
			"inside the signature")
	}
	if status.IsSecurity {
		t.Errorf("appended metadata reached the owner-facing status: %+v", status)
	}
	if got := ownerNotifications(svc); len(got) != 0 {
		t.Fatalf("an attacker appending is_security to a signed manifest pushed %d notifications "+
			"to the box owner: %+v", len(got), got)
	}
}

// TestSecurityNotification_SilentWhenTheSignatureIsForged — the same, for a
// manifest that was never signed by the certified release key at all. Belt and
// braces on the "nothing unverified ever notifies" property.
func TestSecurityNotification_SilentWhenTheSignatureIsForged(t *testing.T) {
	ch := newOTANotifyChannel(t)
	ch.publish(t, otaSecurityManifest("v09"), nil)
	// Re-sign with the ROOT key, which signs exactly one thing: the cert.
	sigData, err := signing.MarshalSig(signing.Signature{
		Algorithm: signing.AlgorithmID,
		KeyID:     "release-test",
		SigBytes:  signing.Sign(ch.rootPriv, ch.files["/stable.json"]),
	})
	if err != nil {
		t.Fatal(err)
	}
	ch.files["/stable.json.sig"] = sigData

	svc := notify.New()
	onCheck := onSecurityCheck(svc, func() string { return otaTestOwner })
	status := checkAndNotify(t, newOTANotifyClient(t, ch), onCheck)

	if status.LastError == "" {
		t.Fatal("a manifest signed by the ROOT anchor verified as a release manifest")
	}
	if got := ownerNotifications(svc); len(got) != 0 {
		t.Fatalf("an unverified manifest pushed %d notifications to the owner", len(got))
	}
}

// TestSecurityNotification_SilentWithNoOwner — a box with no resolved owner has
// nobody to target, and an untargeted urgent notification would be visible to
// every account on a multi-user box.
func TestSecurityNotification_SilentWithNoOwner(t *testing.T) {
	ch := newOTANotifyChannel(t)
	ch.publish(t, otaSecurityManifest("v09"), nil)

	svc := notify.New()
	onCheck := onSecurityCheck(svc, func() string { return "" })
	checkAndNotify(t, newOTANotifyClient(t, ch), onCheck)

	if got := svc.List(0); len(got) != 0 {
		t.Fatalf("a box with no owner still broadcast %d notifications: %+v", len(got), got)
	}
}
