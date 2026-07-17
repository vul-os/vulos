package cpserver

// failopen_guard_test.go — in-package unit tests for the M2 fail-open-to-free
// guard. The guard predicate is exercised directly (warnBillingSeamFailOpen)
// rather than through a full New() boot, because a full prod boot trips unrelated
// prod fail-closed subsystems (e.g. mobilepush log.Fatalf without APNS/FCM keys).
// Testing the pure predicate keeps the M2 regression guard fast and robust.

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"github.com/vul-os/vulos-management/pkg/billingport"
)

// realResolver is a non-noop EntitlementResolver: its concrete type is not
// *billingport.NoopResolver, so IsNoopResolver reports false — the shape a real
// commercial resolver has.
type realResolver struct{ *billingport.NoopResolver }

func capture() (*slog.Logger, *bytes.Buffer) {
	var buf bytes.Buffer
	return slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})), &buf
}

// prod + no-op resolver → LOUD fail-open warning (but never a hard fail).
func TestWarnBillingSeamFailOpen_ProdNoop_Warns(t *testing.T) {
	logger, buf := capture()
	warnBillingSeamFailOpen(logger, true, billingport.NewNoopResolver(), billingport.NewNoopProvider(), "prod")
	logs := buf.String()
	if !strings.Contains(logs, "FAIL-OPEN") {
		t.Fatalf("expected a loud FAIL-OPEN warning for prod+noop resolver, got:\n%s", logs)
	}
	if !strings.Contains(logs, "NO-OP payment rail") {
		t.Fatalf("expected the no-op payment-rail warning too, got:\n%s", logs)
	}
}

// prod + real resolver → no fail-open warning (no false alarm).
func TestWarnBillingSeamFailOpen_ProdReal_Quiet(t *testing.T) {
	logger, buf := capture()
	warnBillingSeamFailOpen(logger, true, realResolver{billingport.NewNoopResolver()}, billingport.NewNoopProvider(), "prod")
	if strings.Contains(buf.String(), "FAIL-OPEN") {
		t.Fatalf("real resolver in prod must NOT trip the fail-open warning; got:\n%s", buf.String())
	}
}

// non-prod (self-host dev / local) → silent even with the no-op resolver.
func TestWarnBillingSeamFailOpen_NonProd_Silent(t *testing.T) {
	logger, buf := capture()
	warnBillingSeamFailOpen(logger, false, billingport.NewNoopResolver(), billingport.NewNoopProvider(), "local")
	if buf.Len() != 0 {
		t.Fatalf("non-prod must be silent, got:\n%s", buf.String())
	}
}

// The resolver-name helper reports the rail identity used on /version + /healthz.
func TestResolverRail_Identity(t *testing.T) {
	if got := billingport.ResolverRail(billingport.NewNoopResolver()); got != "noop" {
		t.Fatalf("ResolverRail(noop) = %q, want noop", got)
	}
	if got := billingport.ResolverRail(realResolver{billingport.NewNoopResolver()}); got != "custom" {
		t.Fatalf("ResolverRail(real) = %q, want custom", got)
	}
}
