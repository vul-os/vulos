package appnet

// ssrf_metadata_test.go — SSRF-APPNET-01 regression tests.
//
// The installed-app network namespaces default-ALLOW egress (the OUTPUT chain's
// implicit policy is ACCEPT) and forward to the internet via the host's
// masquerade route. Without an explicit drop, a launched native app could reach
// the cloud instance-metadata service (169.254.169.254) over that route and
// steal the host VM's instance credentials.
//
// These tests exercise the pure namespaceSteps() builder — no root or iproute2
// needed — to prove the metadata drop rule is present, targets the right range,
// and is ordered so it cannot be shadowed by an ACCEPT rule.

import (
	"strings"
	"testing"
)

func testNS() *Namespace {
	return &Namespace{
		Name:     "vulos_user1-calculator",
		AppID:    "user1-calculator",
		OwnerID:  "user1",
		HostPort: 7070,
		AppPort:  80,
		VethHost: "vh_abc123",
		VethNS:   "vn_abc123",
		HostIP:   "10.200.5.1",
		NSIP:     "10.200.5.2",
	}
}

// stepJoined renders a step's argv as a single space-joined string for matching.
func stepJoined(s nsStep) string { return strings.Join(s.args, " ") }

// TestNamespaceSteps_DropsMetadataRange verifies a DROP rule for the
// 169.254.0.0/16 metadata range is programmed into the namespace OUTPUT chain.
func TestNamespaceSteps_DropsMetadataRange(t *testing.T) {
	steps := namespaceSteps(testNS())

	found := false
	for _, s := range steps {
		j := stepJoined(s)
		// Must be an OUTPUT-chain drop of the metadata CIDR inside the namespace.
		if strings.Contains(j, "netns exec") &&
			strings.Contains(j, "iptables") &&
			strings.Contains(j, "OUTPUT") &&
			strings.Contains(j, metadataDenyCIDRv4) &&
			strings.Contains(j, "-j DROP") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("SSRF-APPNET-01 REGRESSION: no namespace OUTPUT DROP rule for the cloud metadata range %s — a launched app can reach IMDS via the host masquerade route and steal instance credentials.\nsteps:\n%s",
			metadataDenyCIDRv4, dumpSteps(steps))
	}
}

// TestNamespaceSteps_MetadataDropCoversIMDS verifies the configured deny CIDR
// actually contains the canonical IMDS address 169.254.169.254.
func TestNamespaceSteps_MetadataDropCoversIMDS(t *testing.T) {
	if !strings.HasPrefix(metadataDenyCIDRv4, "169.254.") || !strings.HasSuffix(metadataDenyCIDRv4, "/16") {
		t.Fatalf("SSRF-APPNET-01 REGRESSION: metadata deny CIDR %q does not cover the 169.254.169.254 IMDS endpoint", metadataDenyCIDRv4)
	}
}

// TestNamespaceSteps_MetadataDropNotShadowedByAccept verifies rule ordering: the
// metadata DROP must not sit AFTER an ACCEPT rule that could match traffic to a
// metadata address. The only ACCEPT rules are the gateway (host veth IP :8080)
// and established/related — neither matches a NEW connection to 169.254/16 — but
// we assert the drop is not appended after a broad accept as a defence against
// future rule reordering.
func TestNamespaceSteps_MetadataDropNotShadowedByAccept(t *testing.T) {
	steps := namespaceSteps(testNS())

	metadataIdx := -1
	for i, s := range steps {
		j := stepJoined(s)
		if strings.Contains(j, "netns exec") && strings.Contains(j, "OUTPUT") &&
			strings.Contains(j, metadataDenyCIDRv4) && strings.Contains(j, "-j DROP") {
			metadataIdx = i
			break
		}
	}
	if metadataIdx == -1 {
		t.Fatal("SSRF-APPNET-01 REGRESSION: metadata drop rule not found at all")
	}

	// Every ACCEPT rule in the namespace OUTPUT chain must be scoped (host IP or
	// established) — none may be a catch-all that would let metadata traffic
	// through before the drop is evaluated. The metadata drop is inserted at the
	// head (-I OUTPUT 1) so it is also evaluated before appended rules.
	for i, s := range steps {
		j := stepJoined(s)
		if !strings.Contains(j, "netns exec") || !strings.Contains(j, "OUTPUT") {
			continue
		}
		if strings.Contains(j, "-j ACCEPT") {
			// Accept rules must be constrained: to the host gateway IP or to an
			// established/related conntrack state. A bare "-j ACCEPT" with no
			// destination/state scoping would shadow the drop.
			scoped := strings.Contains(j, "10.200.") || strings.Contains(j, "ESTABLISHED")
			if !scoped {
				t.Fatalf("SSRF-APPNET-01 REGRESSION: unscoped ACCEPT rule at step %d may shadow the metadata drop: %s", i, j)
			}
		}
	}

	// The metadata drop uses head-insertion so it is evaluated first regardless
	// of the append order of later rules.
	metaStep := stepJoined(steps[metadataIdx])
	if !strings.Contains(metaStep, "-I OUTPUT 1") {
		t.Fatalf("SSRF-APPNET-01 REGRESSION: metadata drop is not head-inserted (-I OUTPUT 1); it could be shadowed by an accept: %s", metaStep)
	}
}

func dumpSteps(steps []nsStep) string {
	var b strings.Builder
	for _, s := range steps {
		b.WriteString("  " + s.desc + ": " + stepJoined(s) + "\n")
	}
	return b.String()
}
