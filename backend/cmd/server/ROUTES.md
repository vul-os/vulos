# Routes file pattern (parallelism contract)

`backend/cmd/server/main.go` was the parallelism bottleneck — every roadmap task
that added an endpoint touched the same file, so concurrent worker waves piled
up on it. This file documents the pattern that replaces that: **new routes go
in their own per-area file**, and `main.go`'s setup section gains exactly one
wiring line per file. Each worker owns its own file.

## The contract

1. Each cluster of related HTTP routes lives in `backend/cmd/server/routes_<area>.go`.
2. The file exports exactly one wiring function:
   `func register<Area>Routes(mux *http.ServeMux, deps ...) { ... }`
3. **No globals captured from `main.go`.** Every dependency the handlers need
   is passed in as a parameter. If a helper is needed (e.g. a private
   `isRestrictedHost`), put it in the same file as an unexported function.
4. `main.go` calls `register<Area>Routes(mux, ...)` exactly once, alongside
   the other `register*Routes` calls. Adding a new area = adding one line to
   `main.go`, not 50.
5. **Existing pattern matches** (already structured this way, do NOT touch):
   - `<service>.RegisterHandlers(mux, ...)` — service-package-owned routes
     (e.g. `appnet.RegisterVisibilityHandlers`, `bootmode.RegisterHandlers`,
     `gpu.RegisterGPUInfoHandlers`, `storageprov.RegisterHandlers`).
   - Per-area files in this dir: `routes_open.go` (canonical reference).

## When to extract vs add new

- **New endpoints** → put in a new `routes_<area>.go` (or extend an existing
  one if the area already has a file). Do NOT add inline `mux.HandleFunc` to
  `main.go`.
- **Existing inline handlers in `main.go`** → fine to leave until naturally
  touched by a task; opportunistic migration only. The point of this pattern
  is *future* parallelism, not a blocking rewrite.

## Privileged routes have an extra rule

Any handler that runs commands, spawns processes, or touches the filesystem
in ways an attacker could influence must replicate the SEC-B gate ordering
**inside the handler**:

1. Kill-switch — `if os.Getenv("VULOS_DISABLE_EXEC") != "" { 503 }`
2. Admin-role check — `authStore.GetProfile(X-User-ID).Role == RoleAdmin` else 403
3. Audit log — `execAuditLog(r, "<route>", "<what was authorised>")`
4. Then run the action.

See the existing `/api/exec`, `/api/apps/launch`, `/api/sandbox/run`, and
`/api/shell/native-launch` handlers for the canonical shape. Privileged
routes also belong in `routes_<area>.go`, not `main.go`.

## Worker prompt language

When dispatching a Sonnet worker that needs to add a new endpoint:

> Put your new handler in **`backend/cmd/server/routes_<area>.go`** (new file)
> as `register<Area>Routes(mux *http.ServeMux, ...)`. Add **exactly one
> wiring line** to `main.go` calling that function alongside the other
> `register*Routes` calls. Do NOT add inline `mux.HandleFunc` to `main.go`.

That keeps the diff hot zone to the new file + a single-line addition, so a
wave of 6+ workers can each add their own area without colliding.

## Reference

- Canonical example: `routes_open.go` (SSRF-hardened `/api/open` handler with
  its private helpers `isRestrictedHost`, `openTabCount`, `openTabMax`).
- Decisions log: see `decisions.md` D33 for the original motivation.
