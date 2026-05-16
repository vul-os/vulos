// Package security holds cross-cutting adversarial regression tests for
// Vula OS (SECAUDIT2).
//
// These tests encode security INVARIANTS established by prior audits
// (decisions.md D24 and the SEC-A..SEC-I / C1..C4 / H1..H6 fixes) so that a
// regression in any of them fails `go test` loudly. They are deliberately
// black/grey-box: each test drives a real production entrypoint (the auth
// Middleware, the webproxy resolver, the joinsync persistence path, the
// registry security gate, the tar extractor, the AI-apps id validator) and
// asserts the hardened behaviour still holds on current main.
//
// A FAILING test here is not a flaky test — it is a real, reportable
// vulnerability. Do not weaken an assertion to make it pass.
package security
