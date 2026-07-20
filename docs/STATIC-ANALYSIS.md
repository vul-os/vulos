# Static analysis: how to read the numbers

This repo is an OSS control plane that is also a **library**. Its exported
surface is consumed by `vulos-cloud`, which carries the deployed `main`. That one
fact changes how two common tools should be read here, so read this before
"cleaning up" anything they report.

## staticcheck — expected: 0

```sh
staticcheck ./...
```

Zero findings is the standing expectation. The value of that number is entirely
in it being zero: at 28 findings (where this repo sat until 2026-07-20) a real
finding is invisible, and the linter stops being able to tell you anything.

Where a finding is deliberate, it is suppressed **at the site with a reason**,
never globally. Two rules learned the hard way:

- `//lint:ignore` attaches to the **line staticcheck reported**, which for a
  string literal or a mid-expression call is not the line the statement starts
  on. If the directive does not match, staticcheck says
  `this linter directive didn't match anything` — which is itself a finding. Use
  `//lint:file-ignore` for those.
- `//nolint:` is **golangci-lint** syntax. staticcheck ignores it completely.

Current suppressions, all with reasoning in-code: the zero-width spaces in the
assistant's delimiter defanging, the empty critical section that is a liveness
probe, and deliberately-deprecated `tar.TypeRegA` in tests that exist to prove
old-GNU archives still extract.

## deadcode — expected: NOT 0, and that is fine

```sh
deadcode -test ./...     # ~182 entries; almost none of it is dead
```

`deadcode` reports what is unreachable **from a `main` in the analysed scope**.
This repo has exactly one `main` (`cmd/server`), while the deployed control plane
is `vulos-cloud`'s. So every exported function that only vulos-cloud calls is
reported as unreachable, and it is not.

Measured on 2026-07-20: of 182 entries, **121 are referenced by name in
vulos-cloud**, and *every* package appearing in the list has at least one
importer. There are no dead packages.

The list is also **clustered, not flat**: most entries are internally-consistent
groups hanging off one unreachable root. `computeJA3`, `isGREASECurve` and
`parseAndStoreFingerprint` all appear, but they are live — reached through
`NewJA3Listener`, which vulos-cloud wraps its production TLS listener with.
Deleting a "0 callers" leaf function from a cluster whose root is live breaks
running code.

**Therefore: never delete from this list by count.** Triage per entry:

1. Is it referenced in vulos-cloud? → keep.
2. Is it reached from something that is? → keep (check the call graph, not just
   the name).
3. Is it a genuine root with no caller anywhere? → then ask why. On this
   codebase that has repeatedly meant *missing wiring*, not rot.

That third case is where the value is. The 2026-07-20 pass found a **production
memory leak** exactly there: `JA3Store.Delete` and `CleanHelloStore` had no
callers in either repo, while the JA3 listener they serve runs on cloud's
production TLS listener. The store is keyed by `RemoteAddr` (unique per
connection, since it carries the ephemeral port), so it gained one permanent
entry per TLS connection forever, holding data nothing read back. Bulk-deleting
those two "unused" functions would have cemented the leak instead of exposing it.
`JA3Conn.Close` now cleans up after itself, so correctness no longer depends on a
caller remembering.

### Dormant entry points (deliberate, pending a policy decision)

These are wired nowhere in either repo. They are **not** rot — each is a complete
security control awaiting a policy call (which routes, what threshold). Listed
here so the next reader does not have to re-derive it:

| Symbol | State |
|---|---|
| `security.BotMiddleware` | JA3/JA4 fingerprints ARE collected in production; nothing acts on the score. Blocks with 403, so the threshold is a judgement call. |
| `security.EgressMiddleware` | `InitEgressTracker` runs, but no handler is wrapped — the 3σ anomaly baseline never sees a byte. |
| `security.HoneypotCheckLogin` / `HoneypotLoginMiddleware` | `WireSecurity` bootstraps honeypot accounts; no login path checks them, so the trap cannot fire. |
| `security.enum.HandleAvailableMiddleware` | Account-enumeration protection, unmounted. |
| `security.GetTLSConfigForClient` | The documented *alternative* fingerprinting path for deployments that do not wrap the listener. Keep. |

## node_modules

`web/node_modules/flatted/golang/` ships Go files, and `go list ./...` picks them
up, so `deadcode` reports 7 entries from a JavaScript dependency. `node_modules`
is gitignored and untracked, so a clean checkout never sees them — filter with
`grep -v node_modules` rather than "fixing" anything.
