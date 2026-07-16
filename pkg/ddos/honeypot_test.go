package ddos

import (
	"testing"
	"time"
)

func TestHoneypotPaths_Count(t *testing.T) {
	paths := HoneypotPaths()
	if len(paths) < 8 {
		t.Fatalf("expected at least 8 honeypot paths, got %d", len(paths))
	}
	// Check a few specific ones.
	want := map[string]bool{
		"/wp-admin.php": true,
		"/.env":         true,
		"/.git/config":  true,
	}
	found := make(map[string]bool)
	for _, p := range paths {
		found[p] = true
	}
	for w := range want {
		if !found[w] {
			t.Errorf("missing honeypot path: %s", w)
		}
	}
}

func TestSuspiciousTracker_MarkAndCheck(t *testing.T) {
	ip := "3.4.5.6"
	MarkSuspicious(ip, 5*time.Second)
	if !IsSuspicious(ip) {
		t.Fatal("expected ip to be suspicious after MarkSuspicious")
	}
}

func TestSuspiciousTracker_Expiry(t *testing.T) {
	ip := "7.8.9.0"
	MarkSuspicious(ip, -time.Second) // already expired
	if IsSuspicious(ip) {
		t.Fatal("expired suspicious entry should not be considered suspicious")
	}
}
