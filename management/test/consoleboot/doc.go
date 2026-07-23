// Package consoleboot holds an isolated, full-boot integration test for the
// self-hosted management control plane with the OPERATOR (super-admin) console
// ENABLED. It lives in its own package (a separate test binary) on purpose: the
// super-admin store is a process-global singleton, and enabling the console
// registers it — so this boot must not share a binary with cmd/server's
// selfhost_smoke_test (which asserts the console-DISABLED deny posture).
//
// It is the regression guard for L1 (the security telemetry dashboard must use
// the REAL admin gate — not the dead deny-all — once the console is wired) and a
// proof of the two-role separation over real HTTP: a portal user is rejected from
// every admin surface while still able to reach the public operator login page.
package consoleboot
