package routing

import (
	"context"
	"testing"
)

// TestValidDirectIP_RejectsNonRoutable closes the security-suite finding:
// net.ParseIP alone accepts 0.0.0.0 / :: and other non-routable targets for
// direct mode. ValidDirectIP must reject them while still allowing real
// (incl. private/CGNAT) addresses.
func TestValidDirectIP_RejectsNonRoutable(t *testing.T) {
	reject := []string{
		"", "not-an-ip", "256.0.0.1", "1.2.3", "1.2.3.4.5",
		"0.0.0.0", "::", "::0",
		"127.0.0.1", "127.5.5.5", "::1",
		"169.254.1.1", "fe80::1",
		"224.0.0.1", "239.1.2.3", "ff02::1", "ff01::1",
	}
	for _, ip := range reject {
		if ValidDirectIP(ip) {
			t.Errorf("ValidDirectIP(%q) = true, want false (non-routable / invalid)", ip)
		}
	}
	accept := []string{
		"203.0.113.42", "8.8.8.8", "1.1.1.1",
		"192.168.1.10", "10.0.0.5", "172.16.3.4", "100.64.0.1", // private/CGNAT allowed
		"2606:4700:4700::1111",
	}
	for _, ip := range accept {
		if !ValidDirectIP(ip) {
			t.Errorf("ValidDirectIP(%q) = false, want true (routable)", ip)
		}
	}
}

// TestSetDirectIP_RejectsUnspecified asserts the store gate uses ValidDirectIP:
// 0.0.0.0 / :: must NOT be accepted as a direct-mode IP even by the owner.
func TestSetDirectIP_RejectsUnspecified(t *testing.T) {
	ctx := context.Background()
	const ulid = "01ARZ3NDEKTSV4RRFFQ69G5FAV"
	st := NewMemStore()
	if _, err := st.Enroll(ctx, ulid, "acct-a", ModeDirect); err != nil {
		t.Fatalf("enroll: %v", err)
	}
	for _, bad := range []string{"0.0.0.0", "::", "127.0.0.1"} {
		if err := st.SetDirectIP(ctx, ulid, "acct-a", bad); err == nil {
			t.Errorf("SetDirectIP accepted non-routable %q, want rejection", bad)
		}
	}
	b, _ := st.GetBinding(ctx, ulid)
	if b.DirectIP != "" {
		t.Errorf("DirectIP was set to %q despite rejected inputs", b.DirectIP)
	}
	if err := st.SetDirectIP(ctx, ulid, "acct-a", "203.0.113.42"); err != nil {
		t.Fatalf("SetDirectIP rejected a valid public IP: %v", err)
	}
}
