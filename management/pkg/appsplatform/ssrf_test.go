package appsplatform

import (
	"context"
	"net"
	"strconv"
	"testing"
	"time"
)

func TestValidateWebhookURL_IPLiterals(t *testing.T) {
	t.Setenv(AllowPrivateWebhooksEnv, "") // guard ON

	blocked := []string{
		"http://127.0.0.1/hook",
		"https://127.5.5.5/hook",
		"http://10.0.0.1/hook",
		"http://172.16.0.1/hook",
		"http://192.168.1.1/hook",
		"http://169.254.169.254/latest/meta-data/", // cloud metadata
		"http://169.254.0.1/hook",
		"http://0.0.0.0/hook",
		"http://[::1]/hook",
		"http://[fc00::1]/hook",
		"http://[fe80::1]/hook",
		"http://[::]/hook",
	}
	for _, u := range blocked {
		if err := ValidateWebhookURL(u); err == nil {
			t.Errorf("expected %q to be rejected, got nil", u)
		}
	}

	allowed := []string{
		"https://8.8.8.8/hook",
		"http://1.1.1.1/hook",
		"https://[2606:4700:4700::1111]/hook",
	}
	for _, u := range allowed {
		if err := ValidateWebhookURL(u); err != nil {
			t.Errorf("expected %q to be allowed, got %v", u, err)
		}
	}
}

func TestValidateWebhookURL_SchemeAndEmpty(t *testing.T) {
	t.Setenv(AllowPrivateWebhooksEnv, "")

	if err := ValidateWebhookURL(""); err != nil {
		t.Errorf("empty url should be allowed, got %v", err)
	}
	if err := ValidateWebhookURL("   "); err != nil {
		t.Errorf("blank url should be allowed, got %v", err)
	}
	for _, u := range []string{
		"ftp://8.8.8.8/x",
		"file:///etc/passwd",
		"gopher://8.8.8.8/x",
		"http://",
	} {
		if err := ValidateWebhookURL(u); err == nil {
			t.Errorf("expected %q to be rejected", u)
		}
	}
}

func TestValidateWebhookURL_HostnameResolution(t *testing.T) {
	t.Setenv(AllowPrivateWebhooksEnv, "")

	orig := resolveIPs
	defer func() { resolveIPs = orig }()

	// A hostname that resolves only to a public address is allowed.
	resolveIPs = func(string) ([]net.IP, error) {
		return []net.IP{net.ParseIP("93.184.216.34")}, nil
	}
	if err := ValidateWebhookURL("https://example.com/hook"); err != nil {
		t.Errorf("public hostname should be allowed, got %v", err)
	}

	// A hostname that resolves to even one private address is rejected
	// (DNS-rebinding style).
	resolveIPs = func(string) ([]net.IP, error) {
		return []net.IP{net.ParseIP("93.184.216.34"), net.ParseIP("127.0.0.1")}, nil
	}
	if err := ValidateWebhookURL("https://rebind.example/hook"); err == nil {
		t.Error("hostname resolving to a private address should be rejected")
	}
}

func TestValidateWebhookURL_EnvOverride(t *testing.T) {
	// With the override on, private targets are permitted, but the scheme is
	// still enforced.
	t.Setenv(AllowPrivateWebhooksEnv, "1")
	if err := ValidateWebhookURL("http://127.0.0.1/hook"); err != nil {
		t.Errorf("override should allow loopback, got %v", err)
	}
	if err := ValidateWebhookURL("http://10.0.0.1/hook"); err != nil {
		t.Errorf("override should allow rfc1918, got %v", err)
	}
	if err := ValidateWebhookURL("ftp://10.0.0.1/hook"); err == nil {
		t.Error("override must still reject non-http(s) schemes")
	}
}

func TestCreateRejectsPrivateWebhook(t *testing.T) {
	t.Setenv(AllowPrivateWebhooksEnv, "")
	r := NewMemoryRegistry()
	_, err := r.Create(CreateParams{Name: "evil", WebhookURL: "http://169.254.169.254/"})
	if err == nil {
		t.Fatal("Create should reject a private/metadata webhook_url")
	}
}

func TestUpdateRejectsPrivateWebhook(t *testing.T) {
	t.Setenv(AllowPrivateWebhooksEnv, "")
	r := NewMemoryRegistry()
	created, err := r.Create(CreateParams{Name: "ok", WebhookURL: "https://8.8.8.8/hook"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	bad := "http://192.168.0.5/hook"
	if _, err := r.Update(created.App.ID, UpdateParams{WebhookURL: &bad}); err == nil {
		t.Fatal("Update should reject a private webhook_url")
	}
}

// TestSafeDialContext_RebindBlocked simulates a DNS rebind: ValidateWebhookURL
// passed earlier (against a public address), but at dial time the host now
// resolves to a blocked (loopback) address. The dial must be refused so the
// connection never reaches the internal target.
func TestSafeDialContext_RebindBlocked(t *testing.T) {
	t.Setenv(AllowPrivateWebhooksEnv, "") // guard ON

	orig := resolveIPs
	defer func() { resolveIPs = orig }()
	resolveIPs = func(string) ([]net.IP, error) {
		return []net.IP{net.ParseIP("127.0.0.1")}, nil // rebinds to loopback
	}

	dial := safeDialContext(&net.Dialer{Timeout: time.Second})
	conn, err := dial(context.Background(), "tcp", "rebind.example:80")
	if err == nil {
		conn.Close()
		t.Fatal("expected dial to a rebinding host (private IP) to be refused")
	}
}

// TestSafeDialContext_PinsResolvedIP proves the dialer connects to the IP it
// screened, not to the (unresolvable) hostname: the stub resolves the name to a
// loopback listener and, with the override permitting private targets, the dial
// reaches that listener.
func TestSafeDialContext_PinsResolvedIP(t *testing.T) {
	t.Setenv(AllowPrivateWebhooksEnv, "1") // permit loopback so the dial proceeds

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	_, port, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		t.Fatalf("split: %v", err)
	}

	orig := resolveIPs
	defer func() { resolveIPs = orig }()
	resolveIPs = func(string) ([]net.IP, error) {
		return []net.IP{net.ParseIP("127.0.0.1")}, nil
	}

	dial := safeDialContext(&net.Dialer{Timeout: time.Second})
	conn, err := dial(context.Background(), "tcp", "pinned.example:"+port)
	if err != nil {
		t.Fatalf("expected pinned dial to the resolved loopback IP to succeed, got %v", err)
	}
	conn.Close()
}

func TestVerifyWithSkew(t *testing.T) {
	body, secret := []byte(`{"a":1}`), "vas_secret"

	fresh := NowTimestamp()
	sig := Sign(fresh, body, secret)
	if !VerifyWithSkew(fresh, body, secret, sig, 5*time.Minute) {
		t.Error("fresh signature should verify within window")
	}

	// Stale timestamp (10 minutes old) fails a 5-minute window.
	stale := strconv.FormatInt(time.Now().Add(-10*time.Minute).Unix(), 10)
	staleSig := Sign(stale, body, secret)
	if VerifyWithSkew(stale, body, secret, staleSig, 5*time.Minute) {
		t.Error("stale signature should be rejected")
	}

	// Far-future timestamp also fails.
	future := strconv.FormatInt(time.Now().Add(10*time.Minute).Unix(), 10)
	futureSig := Sign(future, body, secret)
	if VerifyWithSkew(future, body, secret, futureSig, 5*time.Minute) {
		t.Error("future signature should be rejected")
	}

	// Wrong secret fails regardless of freshness.
	if VerifyWithSkew(fresh, body, "wrong", sig, 5*time.Minute) {
		t.Error("wrong secret should be rejected")
	}

	// maxAge <= 0 disables the freshness check.
	if !VerifyWithSkew(stale, body, secret, staleSig, 0) {
		t.Error("maxAge<=0 should disable freshness check")
	}

	// Non-numeric timestamp fails.
	if VerifyWithSkew("not-a-number", body, secret, Sign("not-a-number", body, secret), time.Minute) {
		t.Error("non-numeric timestamp should be rejected")
	}
}

func TestGetByIncomingWebhookIDConstantTime(t *testing.T) {
	r := NewMemoryRegistry()
	created, err := r.Create(CreateParams{Name: "wh"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	id := created.App.Incoming.ID
	got, err := r.GetByIncomingWebhookID(id)
	if err != nil {
		t.Fatalf("lookup by webhook id failed: %v", err)
	}
	if got.ID != created.App.ID {
		t.Fatalf("got app %s, want %s", got.ID, created.App.ID)
	}
	if _, err := r.GetByIncomingWebhookID("nope"); err != ErrNotFound {
		t.Fatalf("unknown id should be ErrNotFound, got %v", err)
	}
	if _, err := r.GetByIncomingWebhookID(""); err != ErrNotFound {
		t.Fatalf("empty id should be ErrNotFound, got %v", err)
	}
}
