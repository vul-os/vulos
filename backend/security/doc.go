// Package security holds cross-cutting adversarial regression tests for
// Vula OS (SECAUDIT2) AND the attacker-style PENTEST suite (PENTEST-OS-01).
//
// The original tests encode security INVARIANTS established by prior audits
// (decisions.md D24 and the SEC-A..SEC-I / C1..C4 / H1..H6 fixes) so that a
// regression in any of them fails `go test` loudly. They are deliberately
// black/grey-box: each test drives a real production entrypoint (the auth
// Middleware, the webproxy resolver, the joinsync persistence path, the
// registry security gate, the tar extractor, the AI-apps id validator) and
// asserts the hardened behaviour still holds on current main.
//
// The PENTEST suite (files *_pentest_test.go) is the active red-team layer:
// every test ATTEMPTS a concrete attack against a real production entrypoint
// and ASSERTS it is blocked. A test named `..._DOCUMENTS_LiveFinding` records a
// confirmed-live weakness that is flagged in SECURITY-TESTING.md rather than
// hard-failed (because the safe fix is non-trivial). Attack classes covered:
//
//	#1 LAN cert is NOT a CA              — lan_cert_pentest_test.go
//	#2 LAN-cert puller / first-boot MITM — lan_cert_pentest_test.go
//	#3 fabric peer auth (401 gate, TLS)  — fabric_crdt_pentest_test.go
//	#4 CRDT injection / quorum forcing    — fabric_crdt_pentest_test.go
//	#5 mDNS / LAN bind (no wildcard)     — lan_bind_pentest_test.go
//	#6 SSRF via the web proxy            — ssrf_webproxy_pentest_test.go
//	#7 multi-instance provisioning auth  — multiinstance_provision_pentest_test.go
//
// A FAILING test here is not a flaky test — it is a real, reportable
// vulnerability. Do not weaken an assertion to make it pass. See
// backend/security/SECURITY-TESTING.md for coverage + run instructions +
// the live-findings register.
package security
