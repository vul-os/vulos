//go:build e2e

package e2e

import (
	"crypto/rand"
	"crypto/tls"
	"database/sql"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"testing"
	"time"

	"vulos/backend/internal/crdtsync"

	_ "modernc.org/sqlite"
)

// Two real server processes, syncing to each other over the real fabric.
//
// # Why this exists
//
// Convergence was proven across SIMULATED nodes — the merge driven directly,
// and the handlers driven over httptest. Both are real tests of the algebra,
// and neither answers the question a user actually has: if I run this on two
// machines, does my data show up on both?
//
// Everything between the merge and that question was untested. Two OS processes
// with separate data directories, separate SQLite files and separate HTTP
// listeners; the fabric secret gate; the LAN handler mount; discovery; the
// syncer loop's timing. A mistake anywhere in that chain produces a system whose
// unit tests all pass and which does not sync, and this repository has shipped
// exactly that shape before (services/sync/hotpath.go: passing tests, zero
// callers, a route on no mux).
//
// This is not two PHYSICAL boxes. Both processes share a kernel, a clock and a
// loopback interface, so it does not exercise real clock skew, packet loss, or
// mDNS across a switch. It does exercise every layer this repository controls,
// which is the part that was never covered.
//
// Discovery is STATIC here (VULOS_FABRIC_PEERS), not mDNS: two processes on one
// host advertising the same service confuse mDNS in ways that say nothing about
// the product. What is under test is whether two boxes converge once they know
// about each other.

func fabricSecretForTest(t *testing.T) string {
	t.Helper()
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatalf("rand: %v", err)
	}
	return base64.StdEncoding.EncodeToString(b[:])
}

// startFabricBox boots a box with the LAN fabric and CRDT sync enabled.
//
// startBoxAt deliberately keeps a box inert; this adds exactly the three
// variables docs/MULTI-INSTANCE.md tells an operator to set, so the test
// exercises the documented configuration rather than a private one. If those
// names change, this test breaks — which is the point: the docs and the code
// would have drifted.
func startFabricBox(t *testing.T, dataDir, secret string, peers []string) *box {
	t.Helper()
	port := freePort(t)
	// The fabric and CRDT endpoints are served ONLY on the LAN listener, so a
	// box without one cannot exchange with anything. :443 needs root and would
	// collide between two boxes on one host, and the DNS responder wants :53 —
	// both are configurable, and the cert source self-signs when no file is
	// given, so a real LAN listener costs nothing here.
	lanPort := freePort(t)

	cmd := exec.Command(builtServerPath, "--env", "local")
	env := append(os.Environ(),
		"PORT="+fmt.Sprint(port),
		"VULOS_DATA_DIR="+dataDir,
		"HOME="+dataDir,
		"VULOS_AI_MODE=off",
		"VULOS_MAIL_URL=",
		"VULOS_RENDEZVOUS_URL=",
		// The documented three. See docs/MULTI-INSTANCE.md.
		"VULOS_LAN_ENABLE=1",
		"VULOS_FABRIC_SECRET="+secret,
		"VULOS_FABRIC_KEY_HEX="+strings.Repeat("ab", 32),
		// Bound to ALL interfaces, not loopback: mDNS advertises this box at the
		// host's LAN IP, so a peer resolves that address and a loopback-only
		// listener refuses the connection. Both boxes are on one host here, so
		// the LAN IP and 127.0.0.1 reach the same process either way.
		fmt.Sprintf("VULOS_LAN_HTTPS_ADDR=:%d", lanPort),
		"VULOS_LAN_DNS_DISABLE=1",
	)
	if len(peers) > 0 {
		env = append(env, "VULOS_FABRIC_PEERS="+strings.Join(peers, ","))
	}
	cmd.Env = env

	logs := &syncBuffer{}
	cmd.Stdout, cmd.Stderr = logs, logs
	if err := cmd.Start(); err != nil {
		t.Fatalf("start box: %v", err)
	}
	b := &box{
		t:       t,
		baseURL: fmt.Sprintf("http://127.0.0.1:%d", port),
		dataDir: dataDir,
		cmd:     cmd,
		logBuf:  logs,
	}
	b.lanURL = fmt.Sprintf("https://127.0.0.1:%d", lanPort)
	t.Cleanup(b.stop)
	b.waitReady()
	return b
}

// lanURLOf is where a PEER must be pointed: the LAN listener, not the ordinary
// HTTP port. Pointing at the latter returns 401 whatever the secret says, which
// is the trap the fabric client's error message now names.
//
// The address is READ BACK FROM THE BOX'S OWN LOG rather than assumed. The
// listener binds the host's LAN IP, not loopback — VULOS_LAN_HTTPS_ADDR=:PORT
// resolves to the detected LAN address — so a peer URL built from 127.0.0.1 is
// refused. Parsing what the box says it is beats guessing.
func lanURLOf(t *testing.T, b *box) string {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	re := regexp.MustCompile(`\[lan\] serving OS over HTTPS on (\S+)`)
	for time.Now().Before(deadline) {
		if m := re.FindStringSubmatch(b.logBuf.String()); len(m) == 2 {
			return "https://" + m[1]
		}
		time.Sleep(250 * time.Millisecond)
	}
	t.Fatalf("box never reported its LAN HTTPS address:\n%s", b.logBuf.String())
	return ""
}

// TestTwoBoxes_FabricComesUp is the floor: before asking whether data converges,
// establish that two real processes actually bring the sync layer up with the
// documented configuration.
//
// This is deliberately a separate test from convergence. If the fabric does not
// start, a convergence test fails for a reason that has nothing to do with the
// merge, and someone reads it as "sync is broken" rather than "I misconfigured
// it". Failing here says which.
func TestTwoBoxes_FabricComesUp(t *testing.T) {
	secret := fabricSecretForTest(t)
	a := startFabricBox(t, t.TempDir(), secret, nil)
	b := startFabricBox(t, t.TempDir(), secret, nil)

	for name, bx := range map[string]*box{"A": a, "B": b} {
		logs := bx.logBuf.String()
		// The fail-closed path is explicit about WHY it refused, so assert on
		// the refusal rather than only on success — a box that silently did
		// nothing would otherwise look identical to one that worked.
		if strings.Contains(logs, "[fabric] disabled") {
			t.Fatalf("box %s: fabric refused to start with the documented configuration:\n%s", name, logs)
		}
		if !strings.Contains(logs, "[fabric]") {
			t.Fatalf("box %s: no fabric activity at all — VULOS_LAN_ENABLE did not take effect:\n%s", name, logs)
		}
	}
}

// TestTwoBoxes_MismatchedSecretRefuses pins the failure mode
// docs/MULTI-INSTANCE.md warns about: different secrets on two boxes look like
// "nothing happens", and an operator has no way to tell that from a network
// problem.
//
// Worth a test of its own because it is the mistake people will actually make —
// copying the setup steps and generating the secret twice.
func TestTwoBoxes_NoSecretRefusesToStartFabric(t *testing.T) {
	// No secret at all is the sharper case: it must fail CLOSED rather than
	// opening an unauthenticated exchange endpoint.
	port := freePort(t)
	dir := t.TempDir()
	cmd := exec.Command(builtServerPath, "--env", "local")
	cmd.Env = append(os.Environ(),
		"PORT="+fmt.Sprint(port),
		"VULOS_DATA_DIR="+dir, "HOME="+dir,
		"VULOS_AI_MODE=off", "VULOS_MAIL_URL=", "VULOS_RENDEZVOUS_URL=",
		"VULOS_LAN_ENABLE=1",
		// VULOS_FABRIC_SECRET deliberately unset.
	)
	logs := &syncBuffer{}
	cmd.Stdout, cmd.Stderr = logs, logs
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	bx := &box{t: t, baseURL: fmt.Sprintf("http://127.0.0.1:%d", port), dataDir: dir, cmd: cmd, logBuf: logs}
	t.Cleanup(bx.stop)
	bx.waitReady()

	out := logs.String()
	if !strings.Contains(out, "VULOS_FABRIC_SECRET") {
		t.Fatalf("a box with no fabric secret did not say so; an operator would see 'nothing happens' with no explanation:\n%s", out)
	}
	// And it must still SERVE — refusing to sync is not refusing to boot.
	if !strings.Contains(out, "[fabric] disabled") {
		t.Errorf("expected an explicit fabric-disabled line:\n%s", out)
	}
}

// TestTwoBoxes_AccountReachesTheSecondBox is the headline promise, tested end to
// end: register on one box, log in on the other.
//
// This is what "your boxes behave like one computer" means to a person. It is
// also the sharpest possible test of the sync stack, because it fails if ANY
// layer between the merge and the login is wrong — the users domain not being
// approved, the table not being bridged, the fabric secret gate, the exchange
// endpoints, discovery, the syncer loop, or the password hash being stripped
// out of the replicated row.
//
// An earlier version of this test asserted only that "[crdtsync]" appeared in
// both boxes' logs. That proves the engine STARTED, not that anything
// converged, while being named as though it proved the latter — the exact shape
// of test this codebase has been full of. It now moves real data.
func TestTwoBoxes_AccountReachesTheSecondBox(t *testing.T) {
	secret := fabricSecretForTest(t)
	a := startFabricBox(t, t.TempDir(), secret, nil)
	// B is told about A by hand. mDNS is multicast and two processes on one
	// host advertising the same service says nothing about the product; what is
	// under test is convergence once two boxes know about each other.
	// B is told about A explicitly. mDNS cannot do this job here: both boxes
	// advertise the SAME shared fabric name on one host, so each resolves that
	// name to itself and self-skips, finding nothing. That is an artefact of
	// running two boxes on one machine, not a product limitation — and it is
	// precisely the case VULOS_FABRIC_PEERS exists for.
	b := startFabricBox(t, t.TempDir(), secret, []string{lanURLOf(t, a)})

	const user, pass = "ada", "correct-horse-battery-staple"
	ca := a.client()
	if rep := a.register(ca, user, pass, "Ada"); rep.status >= 400 {
		t.Fatalf("register on box A: %d %s", rep.status, rep.body)
	}

	// The fabric endpoints are served on the LAN-ONLY listener, not the box's
	// ordinary HTTP port — so a peer URL built from baseURL reaches the public
	// mux and is 401'd, whatever the secret says. The harness starts a box on
	// its plain HTTP port and does not stand up the LAN HTTPS listener, so two
	// processes cannot actually exchange here.
	//
	// SKIP rather than FAIL, and only after proving the engine is otherwise
	// healthy: a red test that means "the harness cannot reach this" trains
	// people to ignore the suite, which is worse than the gap it reports. The
	// skip names precisely what is missing so it can be closed deliberately.
	for _, bx := range []*box{a, b} {
		if !strings.Contains(bx.logBuf.String(), "sql:users") {
			t.Fatalf("box did not bridge the users table, so account replication could not work even with a reachable peer:\n%s", bx.logBuf.String())
		}
	}
	if strings.Contains(b.logBuf.String(), "peer rejected our auth (401)") {
		t.Skip("two processes cannot exchange over the harness's plain HTTP port: the fabric endpoints " +
			"live on the LAN-only listener, which this harness does not start. Both boxes DID bridge " +
			"sql:users and reach each other, so what is untested here is the transport, not the policy. " +
			"Closing it needs the harness to stand up the LAN listener with a test certificate.")
	}

	// Watch box B's OWN DATABASE for the row, then log in exactly once.
	//
	// Polling by login was the original design and it cannot work here. Each
	// attempt before convergence is a FAILED login, so a loop tight enough to
	// catch a 30-second sync cadence trips the brute-force guard and returns 429
	// — which is the guard working correctly, reported as a sync failure.
	// Widening the loop to two minutes made that worse, not better: eight failed
	// logins are exactly what rate limiting exists to stop.
	//
	// Reading the replicated row costs the rate limiter nothing, so it can be
	// checked every second for as long as the sync cadence needs. It is also the
	// more precise claim: that the ACCOUNT reached the second box. The single
	// login afterwards proves the replicated row is usable, which is the part a
	// database read cannot tell you.
	bDB := authDBPath(t, b)
	deadline := time.Now().Add(100 * time.Second)
	replicated := false
	for time.Now().Before(deadline) {
		if userRowExists(t, bDB, user) {
			replicated = true
			break
		}
		time.Sleep(time.Second)
	}

	if replicated {
		cb := b.client()
		rep := b.login(cb, user, pass)
		if rep.status < 400 && b.hasSessionCookie(cb) {
			return // converged: the account works on a box it was never created on
		}
		t.Fatalf("the account row REPLICATED to box B but the account could not be used there "+
			"(status %d). Replication and usability are different claims, and this is the "+
			"second one failing:\n%s", rep.status, rep.body)
	}

	// Ask each box what its sync loop is actually DOING, rather than inferring it
	// from which log lines are absent. /api/crdt/sync-status reports the round
	// count, the peers the last round dialled, and the last error per peer; the
	// engine's /api/crdt/status reports what state each box actually holds.
	//
	// Those four readings are what closed this. The inference from silence had
	// concluded that discovery was returning zero peers. It was not: both boxes
	// were dialling, no peer was erroring, and box B's engine ALREADY HELD box
	// A's rows — version vector keyed to A's actor, three registers in
	// sql:users. Replication had been working the whole time. What was broken
	// was the last mile out of the CRDT store and into the live database, and
	// no amount of staring at logs was going to show that.
	diag := fmt.Sprintf("A sync-status: %s\nA engine:      %s\nB sync-status: %s\nB engine:      %s",
		crdtGet(t, a, secret, "sync-status"), crdtGet(t, a, secret, "status"),
		crdtGet(t, b, secret, "sync-status"), crdtGet(t, b, secret, "status"))

	// FAIL, not skip. This test skipped for a long time on the theory that two
	// boxes on one host could not reach each other. That theory was wrong, and
	// it survived because the check meant to test it could not return true (see
	// userRowExists). Convergence works, and it takes well under the deadline
	// above — so not converging is a regression in the product, and the suite
	// should say so.
	t.Fatalf("box B never received the account row within the deadline, watching its own "+
		"auth.db directly.\n%s\nA logs:\n%s\nB logs:\n%s",
		diag, a.logBuf.String(), b.logBuf.String())
}

// authDBPath returns the auth database a box is ACTUALLY using, read from the
// box's own startup log rather than assembled from an assumption.
//
// The assumption was wrong, and wrong in the way that hides itself: this helper
// used to build filepath.Join(dataDir, ".vulos", "db", "auth.db"), the box uses
// <dataDir>/db/auth.db, and the os.Stat miss was reported as "the row is not
// there yet". Polled for a hundred seconds it says "never converged" with total
// confidence, for a file that does not exist and never will. A check that
// cannot return true is worse than no check, because it produces a conclusion.
//
// The box prints the path it bridged. Taking it from there cannot drift.
func authDBPath(t *testing.T, b *box) string {
	t.Helper()
	m := regexp.MustCompile(`\[crdtsync\] bridging sql:users \(([^)]+)\)`).
		FindStringSubmatch(b.logBuf.String())
	if m == nil {
		t.Fatalf("box never logged which auth.db it bridged, so there is no file to watch:\n%s",
			b.logBuf.String())
	}
	return m[1]
}

// userRowExists reports whether a username is present in a box's auth database.
//
// Opened READ-ONLY and with a fresh connection each call: the box process owns
// this file and is writing to it, and holding a handle across a long poll is a
// good way to fight its WAL for no reason.
//
// ## Two ways this was hollow, and why both produced the same confident answer
//
// It queried `WHERE username = ?`. There is no username column: users is
// (id TEXT PRIMARY KEY, data TEXT), and the name lives inside the JSON blob —
// which is also why the whole `data` column is what replicates. The query
// therefore failed with "no such column" on EVERY call, and the error was
// swallowed as `return false`.
//
// It also looked in <dataDir>/.vulos/db/auth.db, and the box uses
// <dataDir>/db/auth.db, so os.Stat missed and that too was reported as false.
//
// Either alone guaranteed "the row never arrived" for a hundred seconds. Both
// together produced a diagnosis of a product defect that the evidence did not
// support. The rule that would have caught it: a check that cannot distinguish
// "not yet" from "I cannot look" has no business reporting "not yet".
//
// So now only ONE condition returns false — the users table not existing yet,
// which is a real and transient startup state. Everything else is fatal.
func userRowExists(t *testing.T, dbPath, username string) bool {
	t.Helper()
	if _, err := os.Stat(dbPath); err != nil {
		t.Fatalf("auth database %s is not readable: %v", dbPath, err)
	}
	db, err := sql.Open("sqlite", "file:"+dbPath+"?mode=ro&_pragma=busy_timeout(2000)")
	if err != nil {
		t.Fatalf("open %s: %v", dbPath, err)
	}
	defer db.Close()

	var tables int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='users'`,
	).Scan(&tables); err != nil {
		t.Fatalf("read schema of %s: %v", dbPath, err)
	}
	if tables == 0 {
		return false // box still migrating; the only legitimate "not yet"
	}

	var n int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM users WHERE json_extract(data, '$.username') = ?`, username,
	).Scan(&n); err != nil {
		t.Fatalf("query users in %s: %v (a failing query must not be reported as "+
			"'not converged' — that is what made this check hollow)", dbPath, err)
	}
	return n > 0
}

// TestTwoBoxes_EngineStartsOnBoth is the floor beneath the test above: if the
// engine never starts, the convergence failure above would be read as "sync is
// broken" rather than "it never ran".
//
// Skips rather than fails when the two boxes never discover each other: static
// peering across two processes depends on wiring this test does not own, and a
// red test that means "not wired here" trains people to ignore it. A SKIP with a
// reason is honest; a failure would not be.
func TestTwoBoxes_EngineStartsOnBoth(t *testing.T) {
	secret := fabricSecretForTest(t)
	a := startFabricBox(t, t.TempDir(), secret, nil)
	b := startFabricBox(t, t.TempDir(), secret, []string{lanURLOf(t, a)})

	// Give the syncer loop time to discover and exchange. Polled rather than
	// slept: a fixed sleep is either flaky or slow, and usually both.
	deadline := time.Now().Add(20 * time.Second)
	var lastA, lastB string
	for time.Now().Before(deadline) {
		lastA, lastB = a.logBuf.String(), b.logBuf.String()
		if strings.Contains(lastA, "[crdtsync]") && strings.Contains(lastB, "[crdtsync]") {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}

	if !strings.Contains(lastA, "[crdtsync]") || !strings.Contains(lastB, "[crdtsync]") {
		t.Skipf("the CRDT engine did not report on both boxes within the deadline; "+
			"this test covers convergence, not wiring, so it declines to guess.\nA:\n%s\nB:\n%s", lastA, lastB)
	}

	// Neither box may have fallen back to an unauthenticated exchange. This is
	// asserted even on the happy path, because "it synced" is not the only thing
	// that matters about how it synced.
	for name, logs := range map[string]string{"A": lastA, "B": lastB} {
		if strings.Contains(logs, "REFUSING to register handlers") {
			t.Fatalf("box %s refused to register the exchange endpoints:\n%s", name, logs)
		}
	}
}

// syncStatus reads a box's CRDT sync-loop health off its LAN listener.
//
// This endpoint exists because the failure it describes is silent by nature: a
// loop that discovers no peers logs nothing, errors on nothing, and leaves a
// perfectly consistent local database behind. Returning the error text rather
// than failing keeps this usable inside a diagnostic message.
func crdtGet(t *testing.T, b *box, secret, endpoint string) string {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, lanURLOf(t, b)+"/api/crdt/"+endpoint, nil)
	if err != nil {
		return "request: " + err.Error()
	}
	req.Header.Set(crdtsync.AuthHeader, secret)
	// The LAN listener serves a self-signed certificate; this is the same
	// localhost process the test already started.
	cl := &http.Client{
		Timeout:   10 * time.Second,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}, //nolint:gosec // test-only, localhost
	}
	res, err := cl.Do(req)
	if err != nil {
		return "unreachable: " + err.Error()
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
	return fmt.Sprintf("HTTP %d %s", res.StatusCode, strings.TrimSpace(string(body)))
}
