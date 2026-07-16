// Package abuse implements the *code* half of the Trust & Safety risk
// (RISK-ABUSE-01). It provides:
//
//   - Anomaly detection (sliding-window per-account + per-IP rate counters for
//     signup, send, login, recipient-fanout) that emits Signal values to a
//     pluggable handler.
//   - Auto-suspend on signals whose score crosses StrongThreshold, with a
//     human-only Reinstate path. Suspend/Reinstate are recorded in the tamper-
//     evident audit log via the provided AuditRecorder seam.
//   - A pluggable ContentClassifier seam for body-content scoring (default
//     no-op).
//   - A pluggable CSAMHashMatcher seam for PhotoDNA-style hash matching
//     (default no-op stub).
//   - A persistent abuse-report intake queue with a triage status lifecycle
//     (open → investigating → actioned|dismissed) and HTTP routes that the
//     human T&S team drains.
//
// # Human responsibilities (NOT in this package)
//
// Per /docs/RISKS-HUMAN-TEAM.md §2, the following are explicitly OUT of scope
// for this package and must be owned by people:
//
//   - The actual PhotoDNA (or equivalent) integration. CSAMHashMatcher is a
//     one-method interface; the default implementation matches nothing. The
//     real implementation must be plugged in by the T&S lead and reviewed by
//     counsel before any user-content storage product goes live.
//   - Legal CSAM reporting workflows (in ZA: SAPS / Film and Publication
//     Board; if any US nexus: NCMEC). This package never reports automatically;
//     CSAM matches are queued as abuse_reports with category="csam" for
//     immediate human + legal review.
//   - Law-enforcement request handling (subpoenas, preservation orders,
//     emergency disclosure).
//   - Final takedown decisions, AUP enforcement edge cases, appeals.
//   - On-call abuse triage staffing.
//
// # Storage invariants (FROZEN)
//
//   - Pure-Go modernc.org/sqlite ONLY. Never CGO.
//   - No Go-module dependency on vulos-mail / vulos-relay / vulos-office.
//   - The detector's sliding-window counters are intentionally in-memory only;
//     they are forensic-quality on the scale of minutes, not hours, and reset
//     on restart. Durable state (suspensions, reports) lives in SQLite.
package abuse
