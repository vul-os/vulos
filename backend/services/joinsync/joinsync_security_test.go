package joinsync

// joinsync_security_test.go — SECAUDIT2 / INIT-08 adversarial regressions.
//
// Invariants:
//   * The cluster passphrase is NEVER written anywhere under $HOME — not just
//     storage.json (the existing test) but every file the join path touches
//     (sync-state.json, instance.json, *.tmp atomic-write scratch files).
//   * sanitize() rejects every missing/blank required field (defence against
//     a half-built JoinRequest reaching the S3 backend).
//   * A distinctive passphrase is used so the recursive scan cannot get a
//     false negative from base64/JSON encoding of unrelated fields.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestJoin_PassphraseNeverHitsDiskAnywhere walks the whole fake $HOME after a
// successful join + drained pull and fails if the passphrase appears in ANY
// file. This is stronger than the existing storage.json-only check.
func TestJoin_PassphraseNeverHitsDiskAnywhere(t *testing.T) {
	m := newMockBackend()
	withMock(t, m)
	home := tmpHome(t)
	drainPull(t, m, home)

	req := validReq()
	// A highly distinctive sentinel — extremely unlikely to collide with any
	// legitimately-persisted field, and not base64/hex of anything else here.
	req.Passphrase = "ZZ_SECAUDIT2_PASSPHRASE_SENTINEL_do_not_persist_ZZ"

	if _, err := Join(req, home); err != nil {
		t.Fatalf("Join: %v", err)
	}

	// The mock must actually have RECEIVED the passphrase in memory (proves
	// the test is meaningful — the value really flows through Join).
	if m.gotPassphrase != req.Passphrase {
		t.Fatalf("backend.validate did not receive the passphrase in memory "+
			"(got %q) — test would be a false negative", m.gotPassphrase)
	}

	sentinel := []byte(req.Passphrase)
	var offenders []string
	err := filepath.WalkDir(home, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		data, rerr := os.ReadFile(path)
		if rerr != nil {
			return nil
		}
		if strings.Contains(string(data), string(sentinel)) {
			offenders = append(offenders, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk home: %v", err)
	}
	if len(offenders) > 0 {
		t.Fatalf("INIT-08 REGRESSION: cluster passphrase was persisted to "+
			"disk in: %v. The passphrase must live ONLY in memory for the "+
			"lifetime of the pull goroutine.", offenders)
	}
}

// TestSanitize_RejectsEveryMissingField guards the input gate that runs
// before any S3 / crypto work on the unauthenticated /api/setup/join path.
func TestSanitize_RejectsEveryMissingField(t *testing.T) {
	cases := map[string]JoinRequest{
		"missing bucket":     {Access: "a", Secret: "s", Passphrase: "p"},
		"missing access":     {Bucket: "b", Secret: "s", Passphrase: "p"},
		"missing secret":     {Bucket: "b", Access: "a", Passphrase: "p"},
		"missing passphrase": {Bucket: "b", Access: "a", Secret: "s"},
		"all blank":          {Bucket: "   ", Access: "\t", Secret: " ", Passphrase: "  "},
	}
	for name, req := range cases {
		r := req
		if err := sanitize(&r); err == nil {
			t.Fatalf("%s: sanitize accepted an incomplete JoinRequest — "+
				"blank/missing required fields must be rejected before the "+
				"S3 backend is contacted", name)
		}
	}

	// A complete request must pass and be whitespace-trimmed.
	ok := JoinRequest{Bucket: " b ", Access: " a ", Secret: " s ", Passphrase: " p "}
	if err := sanitize(&ok); err != nil {
		t.Fatalf("sanitize rejected a complete request: %v", err)
	}
	if ok.Bucket != "b" || ok.Access != "a" || ok.Secret != "s" || ok.Passphrase != "p" {
		t.Fatalf("sanitize did not trim fields: %+v", ok)
	}
}
