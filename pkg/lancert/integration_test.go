// integration_test.go — cross-repo joint integration test for the LANCERT
// renewal flow (FIX-LANCERT-RENEWAL-TEST-01).
//
// # What this test exercises
//
// The renewal pathway has TWO halves that ship in two separate repos:
//
//   - CLOUD (this repo): Service.RunRenewals scans for boxes within
//     RenewWindow of NotAfter, calls Issue (DNS-01 + mock CA), persists the
//     new BoxCert. The cloud route GET /api/lancert/cert serves the latest
//     PEMs.
//
//   - OS-SIDE PULLER (vulos repo, FIX-LANCERT-PULL-01 — being written in
//     parallel by another agent): a small daemon that
//       1. POSTs /api/lancert/report-ip after boot,
//       2. polls GET /api/lancert/cert,
//       3. writes the returned cert_pem/key_pem ATOMICALLY to
//          /var/lib/vulos/tls/lan.crt and /var/lib/vulos/tls/lan.key,
//       4. relies on the lan.fileCertSource fsnotify-equivalent (mtime-poll)
//          inside LoadCertSource to hot-reload on the next ClientHello.
//
// Because we cannot import the OS repo here (FROZEN invariant: NO
// Go-module dep on vulos-mail/relay/office; vulos itself is the parent OS
// repo and is similarly isolated), we mock the OS puller's HTTP behaviour
// from the cloud test process:
//
//   - drive POST /api/lancert/report-ip + GET /api/lancert/cert as a thin
//     test client (this is precisely what the OS puller will do — same
//     headers, same JSON shape), and
//   - write the returned PEMs to a t.TempDir() using the SAME atomic-write
//     pattern the OS puller is documented to use (write-temp + rename).
//
// We then advance the clock to trigger renewal on the cloud, re-pull from
// the route, write again, and assert via the tempdir reader that the bytes
// on disk are NEW (post-renewal cert), not the old ones.
//
// # Expected real-OS-side behaviour (documented for the OS team)
//
// On the real OS the puller runs continuously; this test approximates one
// iteration plus a renewal cycle. The real OS behaviour the cloud-side
// contract relies on is:
//
//	1. The puller exits non-zero only on auth / network failure — schema
//	   mismatches from the cloud (e.g. 202 Accepted with empty body) are
//	   transient and DO NOT crash the daemon.
//
//	2. After writing both PEMs atomically (write-temp + rename), the puller
//	   resets file mtimes to time.Now() so the fileCertSource sees a
//	   monotonic ModTime change every renewal — this avoids a race where
//	   a renewal happens to land on the same second as the previous write.
//
//	3. The puller never reads/parses the cert PEM; the cloud's leaf
//	   NotAfter is authoritative, and the puller relies on the renewal
//	   schedule it polls on a fixed interval (default 1h).
//
//	4. If the cloud returns 404 (unknown box), the puller MUST re-issue
//	   the report-ip call on its next tick — the cloud's row may have been
//	   nuked during a tenant move.

package lancert

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// mockOSpuller mimics the OS-side LANCERT puller (vulos repo
// FIX-LANCERT-PULL-01) from inside the cloud test process. It owns a
// tempdir into which it atomically writes the PEMs the cloud serves on
// /api/lancert/cert — the same on-disk contract LoadCertSource reads.
type mockOSpuller struct {
	t          *testing.T
	baseURL    string
	secret     string
	boxID      string
	lanIP      string
	certPath   string
	keyPath    string
	httpClient *http.Client
}

func newMockOSPuller(t *testing.T, baseURL, secret, boxID, lanIP string) *mockOSpuller {
	t.Helper()
	dir := t.TempDir()
	return &mockOSpuller{
		t:          t,
		baseURL:    strings.TrimRight(baseURL, "/"),
		secret:     secret,
		boxID:      boxID,
		lanIP:      lanIP,
		certPath:   filepath.Join(dir, "lan.crt"),
		keyPath:    filepath.Join(dir, "lan.key"),
		httpClient: http.DefaultClient,
	}
}

// reportIP POSTs /api/lancert/report-ip. The real OS puller does this on
// every boot and on LAN-IP-change.
func (p *mockOSpuller) reportIP(t *testing.T) {
	t.Helper()
	body := strings.NewReader(`{"box_id":"` + p.boxID + `","lan_ip":"` + p.lanIP + `"}`)
	req, _ := http.NewRequest(http.MethodPost, p.baseURL+"/api/lancert/report-ip", body)
	req.Header.Set("X-Device-Auth", p.secret)
	resp, err := p.httpClient.Do(req)
	if err != nil {
		t.Fatalf("puller report-ip: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("puller report-ip: want 200, got %d", resp.StatusCode)
	}
}

// pollAndWrite GETs /api/lancert/cert; if 200, writes both PEMs atomically
// to certPath/keyPath. Returns true on a 200 write, false on a 202 (no cert
// yet — the real puller would re-poll on its next tick).
//
// Atomic write pattern mirrors what the real OS puller does:
//
//  1. write to tempfile in the same dir,
//  2. fsync (skipped here — t.TempDir() is /tmp, not survival-critical),
//  3. rename over the canonical path (renames are atomic on the same fs).
func (p *mockOSpuller) pollAndWrite(t *testing.T) bool {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, p.baseURL+"/api/lancert/cert?box_id="+p.boxID, nil)
	req.Header.Set("X-Device-Auth", p.secret)
	resp, err := p.httpClient.Do(req)
	if err != nil {
		t.Fatalf("puller poll: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusAccepted {
		return false // not yet issued; real puller would back off.
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("puller poll: want 200, got %d body=%s", resp.StatusCode, body)
	}
	var bc BoxCert
	if err := json.NewDecoder(resp.Body).Decode(&bc); err != nil {
		t.Fatalf("puller decode: %v", err)
	}
	atomicWrite(t, p.certPath, []byte(bc.CertPEM))
	atomicWrite(t, p.keyPath, []byte(bc.KeyPEM))
	return true
}

// atomicWrite is the write-temp-then-rename idiom the real OS puller uses.
func atomicWrite(t *testing.T, path string, data []byte) {
	t.Helper()
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		t.Fatalf("write tmp: %v", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		t.Fatalf("rename: %v", err)
	}
}

// readCertPEM returns the leaf certificate from the tempdir. This is what
// the real LoadCertSource (vulos repo, lan.fileCertSource) parses every
// time the file mtime changes.
func (p *mockOSpuller) readCertPEM(t *testing.T) *x509.Certificate {
	t.Helper()
	raw, err := os.ReadFile(p.certPath)
	if err != nil {
		t.Fatalf("read cert: %v", err)
	}
	block, _ := pem.Decode(raw)
	if block == nil {
		t.Fatalf("cert PEM does not decode")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("parse cert: %v", err)
	}
	return cert
}

// TestLANCERT_CrossRepo_FullRenewalCycle exercises:
//
//	report-ip → issue → puller-poll → write to tempdir → assert cert OK
//	→ advance clock → RunRenewals → puller-poll → write again → assert
//	the on-disk cert is NEW (different serial / NotAfter).
//
// The "advance clock" step uses the mock CA's NotAfter field directly — we
// rewind the issued cert's expiry into the renewal window. The real CA
// behaviour is identical from the cloud's POV: certs simply approach
// expiry as wall time passes.
func TestLANCERT_CrossRepo_FullRenewalCycle(t *testing.T) {
	// ── Cloud side ────────────────────────────────────────────────────────
	acmeM := newMockACME()
	// First issuance: cert expires far in the future so the initial Issue
	// does not look like it needs immediate renewal.
	acmeM.NotAfter = time.Now().UTC().Add(90 * 24 * time.Hour)
	dns := NewNoopDNSProvider()
	svc := newTestService(t, acmeM, dns)
	svc.RenewWindow = 30 * 24 * time.Hour

	h := NewHandler(svc, "joint-secret")
	mux := http.NewServeMux()
	h.Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	// ── Mock OS-side puller (vulos repo FIX-LANCERT-PULL-01) ──────────────
	puller := newMockOSPuller(t, srv.URL, "joint-secret", "jointbox", "10.0.0.42")

	// Step 1 — puller registers its LAN IP.
	puller.reportIP(t)

	// Step 2 — first poll: no cert yet (issuance is async on the real cloud).
	if got := puller.pollAndWrite(t); got {
		t.Fatalf("first poll: expected 202, but a cert was already issued")
	}

	// Step 3 — cloud issues. (In production this is the renewal loop or an
	// explicit operator trigger; in the test we drive it directly so the
	// test is deterministic.)
	firstCert, err := svc.Issue(context.Background(), "jointbox")
	if err != nil {
		t.Fatalf("svc.Issue: %v", err)
	}

	// Step 4 — puller polls again: 200, writes both PEMs to tempdir.
	if !puller.pollAndWrite(t) {
		t.Fatalf("second poll: expected 200 with cert")
	}

	// Verify the tempdir bytes equal what the cloud just issued — this is
	// the cross-repo round-trip the OS-side lan.fileCertSource will read.
	onDisk1 := puller.readCertPEM(t)
	if !onDisk1.NotAfter.Equal(firstCert.NotAfter) {
		t.Fatalf("first cert NotAfter mismatch on disk: %v vs %v",
			onDisk1.NotAfter, firstCert.NotAfter)
	}
	firstSerial := onDisk1.SerialNumber.String()
	firstNotAfter := onDisk1.NotAfter

	// Make sure the mtime is non-zero so the OS-side fileCertSource has a
	// baseline to compare against. (sanity)
	st1, err := os.Stat(puller.certPath)
	if err != nil {
		t.Fatalf("stat cert 1: %v", err)
	}
	mtimeBefore := st1.ModTime()

	// ── Advance clock: rewind cert expiry into the renewal window ────────
	// The cloud's RunRenewals queries DueForRenewal(now, RenewWindow); a
	// cert with NotAfter < now + RenewWindow is due. Simulate the
	// just-issued cert "aging into" the renewal window by making the
	// mock CA's next-issuance NotAfter equally close — and then patch
	// the stored row to look like it expires soon, so the renewer picks
	// it up.

	// Patch the persisted row so its NotAfter is 5d away → inside the
	// 30d renew window.
	soon := time.Now().UTC().Add(5 * 24 * time.Hour)
	patchBoxState(t, svc.Store, "jointbox", soon)

	// New issuance returns a cert that expires further out than the first
	// cert (use 120d instead of 90d so the NotAfter is unambiguously later
	// even though both Issue calls happen in the same test second).
	acmeM.NotAfter = time.Now().UTC().Add(120 * 24 * time.Hour)

	// Step 5 — RunRenewals: cloud detects the box is due and re-issues.
	svc.RunRenewals(context.Background())

	if acmeM.FinalizeCalls != 2 {
		t.Fatalf("renewal pass: want 2 Finalize calls, got %d", acmeM.FinalizeCalls)
	}

	// Ensure the file system can observe a distinct ModTime — atomic
	// writes in the same nanosecond produce equal mtimes on some
	// filesystems, which would defeat the OS-side fsnotify/mtime poll.
	// Sleep a hair so the rename below changes mtime perceptibly.
	for {
		// Force-poke the file system clock by re-stat'ing until
		// time.Now > mtimeBefore + 1 tick.
		if time.Now().After(mtimeBefore.Add(time.Millisecond)) {
			break
		}
		time.Sleep(time.Millisecond)
	}

	// Step 6 — puller polls AGAIN. The cloud now serves the rotated cert.
	if !puller.pollAndWrite(t) {
		t.Fatalf("post-renewal poll: expected 200")
	}

	// Step 7 — assert via the tempdir reader that the cert ROTATED.
	// (On the real OS, lan.fileCertSource compares ModTime — that is
	// covered by lan.fileCertSource's own tests in the vulos repo. Here
	// we assert the bytes on disk are post-renewal.)
	onDisk2 := puller.readCertPEM(t)
	if onDisk2.SerialNumber.String() == firstSerial {
		t.Fatalf("post-renewal cert has SAME serial as pre-renewal — not rotated")
	}
	if !onDisk2.NotAfter.After(firstNotAfter) {
		t.Fatalf("post-renewal NotAfter not advanced: pre=%v post=%v",
			firstNotAfter, onDisk2.NotAfter)
	}

	// And the file mtime must have advanced — the documented signal the
	// OS-side fileCertSource uses to reload (mtime change). This is the
	// "would see the rotated cert" assertion the AC requires.
	st2, err := os.Stat(puller.certPath)
	if err != nil {
		t.Fatalf("stat cert 2: %v", err)
	}
	if !st2.ModTime().After(mtimeBefore) {
		t.Fatalf("cert file mtime did not advance after rotation: before=%v after=%v",
			mtimeBefore, st2.ModTime())
	}

	// And the renewal log carries the "renewed" event the cloud emits
	// once a renewal pass succeeds — this is what the cloud dashboard
	// surfaces.
	logs, err := svc.Store.RecentLog(context.Background(), "jointbox", 10)
	if err != nil {
		t.Fatalf("RecentLog: %v", err)
	}
	foundRenewed := false
	for _, e := range logs {
		if e.Event == "renewed" {
			foundRenewed = true
		}
	}
	if !foundRenewed {
		t.Errorf("expected 'renewed' log entry after RunRenewals; got %+v", logs)
	}
}

// patchBoxState directly rewrites the persisted NotAfter on the BoxState
// row so DueForRenewal returns it. The SQLite Store does not expose a
// generic UPDATE; we use a fresh SaveCert which re-keys (NotBefore,
// NotAfter) on the existing PEM data. This is purely a test affordance —
// nothing in the real renewal path mutates NotAfter that way.
func patchBoxState(t *testing.T, st Store, boxID string, newNotAfter time.Time) {
	t.Helper()
	bs, err := st.Get(context.Background(), boxID)
	if err != nil {
		t.Fatalf("Get for patch: %v", err)
	}
	if err := st.SaveCert(context.Background(), BoxCert{
		BoxID:     bs.BoxID,
		FQDN:      bs.FQDN,
		CertPEM:   bs.CertPEM,
		KeyPEM:    bs.KeyPEM,
		NotBefore: bs.NotBefore,
		NotAfter:  newNotAfter,
	}, time.Now().UTC()); err != nil {
		t.Fatalf("SaveCert patch: %v", err)
	}
}
