package webpush

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ---- store isolation, cap, validation ---------------------------------------

func testStores(t *testing.T) map[string]Store {
	t.Helper()
	sq, err := NewSQLiteStore(filepath.Join(t.TempDir(), "subs.sqlite"))
	if err != nil {
		t.Fatalf("sqlite store: %v", err)
	}
	t.Cleanup(func() { sq.Close() })
	return map[string]Store{"mem": NewMemStore(), "sqlite": sq}
}

func sampleSub(ep string) Subscription {
	return Subscription{Endpoint: ep, P256DH: "pkey", Auth: "asalt"}
}

func enabledCfg() Config {
	return Config{VAPIDPublic: "pub", VAPIDPrivate: "priv", VAPIDSubject: "mailto:a@b", TTL: 300}
}

func TestPushStore_PerOwnerIsolation(t *testing.T) {
	for name, st := range testStores(t) {
		t.Run(name, func(t *testing.T) {
			if err := st.Save("alice", sampleSub("https://push.example/a")); err != nil {
				t.Fatal(err)
			}
			if err := st.Save("bob", sampleSub("https://push.example/b")); err != nil {
				t.Fatal(err)
			}
			a, _ := st.List("alice")
			if len(a) != 1 || a[0].Endpoint != "https://push.example/a" {
				t.Fatalf("alice sees wrong subs: %+v", a)
			}
			if a[0].OwnerID != "alice" {
				t.Fatalf("owner not stamped: %q", a[0].OwnerID)
			}
			// bob deleting alice's endpoint string must NOT touch alice's row.
			if err := st.Delete("bob", "https://push.example/a"); err != nil {
				t.Fatal(err)
			}
			if a2, _ := st.List("alice"); len(a2) != 1 {
				t.Fatalf("cross-owner delete leaked: alice now has %d", len(a2))
			}
		})
	}
}

func TestPushStore_CapEvictsOldest(t *testing.T) {
	for name, st := range testStores(t) {
		t.Run(name, func(t *testing.T) {
			base := time.Now().Add(-time.Hour)
			for i := 0; i < MaxSubsPerUser; i++ {
				s := sampleSub("https://push.example/" + string(rune('a'+i)))
				s.CreatedAt = base.Add(time.Duration(i) * time.Minute)
				if err := st.Save("u", s); err != nil {
					t.Fatal(err)
				}
			}
			// One more (newer) → the oldest ("a") is evicted, count stays capped.
			extra := sampleSub("https://push.example/NEW")
			extra.CreatedAt = time.Now()
			if err := st.Save("u", extra); err != nil {
				t.Fatal(err)
			}
			subs, _ := st.List("u")
			if len(subs) != MaxSubsPerUser {
				t.Fatalf("cap not enforced: %d subs", len(subs))
			}
			for _, s := range subs {
				if s.Endpoint == "https://push.example/a" {
					t.Fatalf("oldest not evicted")
				}
			}
		})
	}
}

func TestPushStore_UpsertSameEndpoint(t *testing.T) {
	for name, st := range testStores(t) {
		t.Run(name, func(t *testing.T) {
			s := sampleSub("https://push.example/x")
			if err := st.Save("u", s); err != nil {
				t.Fatal(err)
			}
			s.P256DH = "rotated"
			if err := st.Save("u", s); err != nil {
				t.Fatal(err)
			}
			subs, _ := st.List("u")
			if len(subs) != 1 || subs[0].P256DH != "rotated" {
				t.Fatalf("upsert failed: %+v", subs)
			}
		})
	}
}

func TestValidate(t *testing.T) {
	ok := Subscription{Endpoint: "https://push.example/x", P256DH: "p", Auth: "a"}
	if err := Validate(ok); err != nil {
		t.Fatalf("valid rejected: %v", err)
	}
	cases := []Subscription{
		{Endpoint: "", P256DH: "p", Auth: "a"},
		{Endpoint: "https://x/y", P256DH: "", Auth: "a"},
		{Endpoint: "http://push.example/x", P256DH: "p", Auth: "a"}, // not https
		{Endpoint: "https://" + strings.Repeat("x", maxEndpointLen), P256DH: "p", Auth: "a"},
		{Endpoint: "https://x/y", P256DH: strings.Repeat("k", 300), Auth: "a"},
	}
	for i, c := range cases {
		if err := Validate(c); err == nil {
			t.Fatalf("case %d: invalid sub accepted", i)
		}
	}
}

// TestValidate_SSRF proves a subscription endpoint can never point the box's
// OUTBOUND push POST at a loopback/private/link-local/metadata target. The box
// POSTs the encrypted payload to this endpoint, so an unscreened endpoint is a
// genuine SSRF sink. Public vendor endpoints must still pass.
func TestValidate_SSRF(t *testing.T) {
	blocked := []string{
		"https://127.0.0.1/push",             // loopback
		"https://localhost/push",             // loopback name
		"https://[::1]/push",                 // IPv6 loopback
		"https://169.254.169.254/latest/api", // cloud metadata (link-local)
		"https://10.0.0.5/push",              // RFC1918
		"https://192.168.1.1/push",           // RFC1918
		"https://172.16.0.1/push",            // RFC1918
		"https://2130706433/push",            // decimal-encoded 127.0.0.1
		"https://0x7f000001/push",            // hex-encoded 127.0.0.1
		"https://box.internal/push",          // internal hostname
		"https://svc.local/push",             // mDNS/.local
		"https://[::ffff:127.0.0.1]/push",    // IPv4-mapped loopback
	}
	for _, ep := range blocked {
		sub := Subscription{Endpoint: ep, P256DH: "p", Auth: "a"}
		if err := Validate(sub); err == nil {
			t.Fatalf("SSRF endpoint accepted: %s", ep)
		}
	}
	// A real vendor endpoint (public host) must still validate.
	ok := Subscription{Endpoint: "https://fcm.googleapis.com/fcm/send/abc", P256DH: "p", Auth: "a"}
	if err := Validate(ok); err != nil {
		t.Fatalf("public vendor endpoint rejected: %v", err)
	}
}

// TestLiveSender_RefusesInternalEndpoint proves the SEND path itself refuses an
// internal endpoint even if a row somehow bypassed Validate (e.g. a legacy
// subscription persisted before the screen existed) — defense-in-depth.
func TestLiveSender_RefusesInternalEndpoint(t *testing.T) {
	sub := Subscription{Endpoint: "https://127.0.0.1/push", P256DH: "p", Auth: "a"}
	if _, err := (LiveSender{}).Send(sub, []byte("{}"), enabledCfg()); err == nil {
		t.Fatal("live sender POSTed to a loopback endpoint (SSRF)")
	}
}

// ---- VAPID resolve -----------------------------------------------------------

func TestResolveVAPID_GeneratesAndPersists(t *testing.T) {
	kf := filepath.Join(t.TempDir(), "vapid.json")
	c := Config{VAPIDKeyFile: kf}
	if err := ResolveVAPID(&c); err != nil {
		t.Fatal(err)
	}
	if !c.Enabled() {
		t.Fatalf("keys not generated")
	}
	// File must be 0600 (private key is a secret).
	info, err := os.Stat(kf)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("key file perms = %v, want 0600", info.Mode().Perm())
	}
	// Second resolve loads the SAME pair (persistence).
	pub := c.VAPIDPublic
	c2 := Config{VAPIDKeyFile: kf}
	if err := ResolveVAPID(&c2); err != nil {
		t.Fatal(err)
	}
	if c2.VAPIDPublic != pub {
		t.Fatalf("key not persisted: %q != %q", c2.VAPIDPublic, pub)
	}
}

func TestResolveVAPID_NoFileStaysOff(t *testing.T) {
	c := Config{}
	if err := ResolveVAPID(&c); err != nil {
		t.Fatal(err)
	}
	if c.Enabled() {
		t.Fatalf("push should stay off with no keys and no key file")
	}
}
