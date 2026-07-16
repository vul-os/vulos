package main

// selfhost_smoke_test.go — the formal self-host smoke test.
//
// It proves the standalone OSS control plane RUNS on its own: it assembles the
// server with the SAME wiring the cmd/server binary uses (cpserver.New with the
// SPA-fallback registrar and NO injected commercial providers), boots it on a
// real ephemeral port with ZERO commercial configuration, and asserts — over a
// real HTTP client — that the operational surface serves and that the free
// no-op / bring-your-own-bucket seams are the ones actually wired.
//
// This is the evidence that the open/closed split is honest: nothing here sets
// a payment key, a managed-storage credential, or any cloud env var, yet the
// control plane is fully functional.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/vul-os/vulos-management/pkg/billingport"
	"github.com/vul-os/vulos-management/pkg/cproutes"
	"github.com/vul-os/vulos-management/pkg/cpserver"
	"github.com/vul-os/vulos-management/pkg/storageport"
)

// TestMain defaults VULOS_ENV=local so the single prod resolver
// (env.IsProdResolved) — which fails safe to prod, and in prod refuses to start
// without SESSION_SECRET — does not trip in this self-host boot test. "local" is
// the repo's self-host environment signal.
func TestMain(m *testing.M) {
	if os.Getenv("VULOS_ENV") == "" {
		_ = os.Setenv("VULOS_ENV", "local")
	}
	os.Exit(m.Run())
}

// commercialEnvVars are the configuration knobs that only exist because the
// commercial cloud build charges money / provisions managed buckets. A genuine
// self-host boot must require NONE of them. The smoke test clears them and
// asserts they are absent, so "runs standalone with zero commercial config" is
// a checked precondition, not an assumption.
var commercialEnvVars = []string{
	// Payment rail.
	"PAYSTACK_SECRET_KEY", "PAYSTACK_PUBLIC_KEY", "PAYSTACK_WEBHOOK_SECRET",
	// Managed object-storage / bucket provisioning credentials.
	"AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY",
	"TIGRIS_ACCESS_KEY_ID", "TIGRIS_SECRET_ACCESS_KEY",
	"S3_ACCESS_KEY_ID", "S3_SECRET_ACCESS_KEY",
	"BUCKET_PROVISIONER_URL", "STORAGE_PROVISIONER_URL",
}

// TestSelfHostSmoke boots the standalone control plane exactly as the binary
// does, with no commercial configuration, and proves it serves.
func TestSelfHostSmoke(t *testing.T) {
	// ── Precondition: zero commercial configuration ──────────────────────────
	// Clear every commercial knob for this process and assert none survive, so
	// the boot below is provably a pure self-host boot.
	for _, k := range commercialEnvVars {
		t.Setenv(k, "")
	}
	for _, k := range commercialEnvVars {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			t.Fatalf("commercial env var %s is set (%q) — not a clean self-host boot", k, v)
		}
	}

	// ── Assemble the server with the BINARY's wiring ─────────────────────────
	// Same Config shape and same Deps as cmd/server/main.go: leave all provider
	// fields nil (→ free no-op billing rail, unlimited self-host entitlements,
	// bring-your-own-bucket storage) and add only the SPA fallback registrar.
	addr := freePort(t)
	cfg := cpserver.Config{
		Addr:        addr,
		Environment: "local",
		Version:     "smoke-test",
		DBDir:       t.TempDir(), // zero-config per-subsystem SQLite; no DATABASE_URL.
	}
	srv, err := cpserver.New(cfg, cpserver.Deps{
		Routes: []cpserver.RouteRegistrar{
			func(mux *http.ServeMux, _ *cpserver.Runtime) error {
				cproutes.RegisterSPAFallback(mux)
				return nil
			},
		},
	})
	if err != nil {
		t.Fatalf("cpserver.New (self-host, zero commercial config): %v", err)
	}
	defer srv.Close()

	// ── Seam proof: the FREE defaults are the ones actually wired ────────────
	rt := srv.Runtime()
	if got := rt.Billing.Name(); got != "noop" {
		t.Fatalf("billing rail = %q, want %q (self-host must never wire a real payment rail)", got, "noop")
	}
	if _, err := rt.Billing.InitTransaction(context.Background(), billingport.InitRequest{}); err != billingport.ErrUnsupported {
		t.Fatalf("noop billing InitTransaction err = %v, want ErrUnsupported (never contacts a payment network)", err)
	}
	if rt.StorageProvisioner.Enabled() {
		t.Fatal("StorageProvisioner.Enabled() = true, want false (self-host is bring-your-own-bucket, provisions nothing)")
	}
	if err := rt.StorageProvisioner.EnsureBucket(context.Background(), "smoke"); err != storageport.ErrProvisioningDisabled {
		t.Fatalf("StorageProvisioner.EnsureBucket err = %v, want ErrProvisioningDisabled (BYOB signal, never a real provisioning call)", err)
	}

	// ── Boot: run the real server on the ephemeral port ──────────────────────
	ctx, cancel := context.WithCancel(context.Background())
	runErr := make(chan error, 1)
	go func() { runErr <- srv.Run(ctx) }()

	base := "http://" + addr
	client := &http.Client{Timeout: 3 * time.Second}
	waitReady(t, client, base+"/healthz")

	// ── Serve the operational surface (real HTTP client) ─────────────────────

	// 1) Liveness.
	if code, body := get(t, client, base+"/healthz"); code != http.StatusOK {
		t.Fatalf("GET /healthz = %d, want 200; body=%s", code, body)
	}

	// 2) Readiness — 200 only when the (SQLite) database answers.
	if code, body := get(t, client, base+"/readyz"); code != http.StatusOK {
		t.Fatalf("GET /readyz = %d, want 200; body=%s", code, body)
	}

	// 3) Version — reports the no-op / self-host billing rail so an operator can
	//    confirm the self-host build never charges.
	code, body := get(t, client, base+"/version")
	if code != http.StatusOK {
		t.Fatalf("GET /version = %d, want 200; body=%s", code, body)
	}
	var ver map[string]string
	if err := json.Unmarshal([]byte(body), &ver); err != nil {
		t.Fatalf("decode /version: %v; body=%s", err, body)
	}
	if ver["billing_rail"] != "noop" {
		t.Fatalf("/version billing_rail = %q, want %q", ver["billing_rail"], "noop")
	}
	if ver["environment"] != "local" {
		t.Fatalf("/version environment = %q, want %q", ver["environment"], "local")
	}

	// 4) An auth route is mounted and functional — signup returns a sane result,
	//    NOT a 404 (route missing) and NOT a 5xx (missing dependency). A unique
	//    address exercises the real account-creation path end to end.
	email := fmt.Sprintf("smoke-%d@example.test", time.Now().UnixNano())
	code, body = postJSON(t, client, base+"/api/auth/signup",
		fmt.Sprintf(`{"email":%q,"password":"correct-horse-battery-staple-9"}`, email))
	if code == http.StatusNotFound {
		t.Fatalf("POST /api/auth/signup = 404 — auth route is not mounted; body=%s", body)
	}
	if code >= 500 {
		t.Fatalf("POST /api/auth/signup = %d (5xx) — auth route hit a missing dependency; body=%s", code, body)
	}

	// 5) The admin/superadmin surface is reachable but gated — deny-all (403),
	//    never a 404. Proves the operator console is mounted, closed by default.
	if code, body := get(t, client, base+"/superadmin/security"); code == http.StatusNotFound {
		t.Fatalf("GET /superadmin/security = 404 — admin surface is not mounted; body=%s", body)
	} else if code != http.StatusForbidden {
		t.Fatalf("GET /superadmin/security = %d, want 403 (mounted but gated deny-all); body=%s", code, body)
	}

	// 6) The products/docs catalogue serves publicly (the Workspace shell reads
	//    it to render, even offline / self-host).
	if code, body := get(t, client, base+"/api/products"); code != http.StatusOK {
		t.Fatalf("GET /api/products = %d, want 200 (public catalogue serves); body=%s", code, body)
	}

	// ── Graceful shutdown: cancel ctx, Run returns without error/panic ───────
	cancel()
	select {
	case err := <-runErr:
		if err != nil {
			t.Fatalf("srv.Run returned %v, want clean shutdown", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("srv.Run did not shut down within 10s of ctx cancel")
	}
}

// freePort binds :0, reads the assigned address, and releases it so the server
// can claim it. Returns a host:port on the loopback interface.
func freePort(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve ephemeral port: %v", err)
	}
	addr := l.Addr().String()
	_ = l.Close()
	return addr
}

// waitReady polls url until it answers 200 or the deadline elapses.
func waitReady(t *testing.T, c *http.Client, url string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := c.Get(url)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("server did not become ready at %s within 5s", url)
}

func get(t *testing.T, c *http.Client, url string) (int, string) {
	t.Helper()
	resp, err := c.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func postJSON(t *testing.T, c *http.Client, url, body string) (int, string) {
	t.Helper()
	resp, err := c.Post(url, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}
