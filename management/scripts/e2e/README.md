# scripts/e2e — console end-to-end test (Playwright)

A headless-Chromium end-to-end test that drives the **real** built management
console SPA (`web/dist`) against a stateful **in-memory mock** of the management
JSON API. No Go backend is run — the binary's role-separation and last-admin
guard are proven exhaustively by the Go tests; this proves the browser-facing
half of the same guarantees.

Isolated Node tooling (its own `package.json`), not part of the Go module — the
same convention as `scripts/screenshots`.

## What it covers

1. **Sign-in** — the self-host login page renders and a submit authenticates
   (`POST /api/auth/login`), landing on the authed console.
2. **Role separation (UI)** — a signed-in **portal user** (`/api/superadmin/whoami`
   → 403) sees **no** Operator nav group, and deep-linking to `/admin/*` shows the
   access-required gate rather than admin data; an **operator** (whoami → 200) sees
   the Operator group and its admin surfaces render.
3. **Admin team** — grant an admin by email, revoke it, and the **last-admin guard**
   in both forms: the UI hides the final Revoke button, and a server `409` surfaces
   as the honest "cannot revoke the last remaining admin" message.

Every scenario asserts **zero uncaught page errors**.

## Run

```sh
npm --prefix web run build            # build web/dist first (the SPA under test)
npm --prefix scripts/e2e install      # first time (installs playwright)
npx --prefix scripts/e2e playwright install chromium   # first time (browser binary)
npm --prefix scripts/e2e run e2e
```

Exit code is non-zero if any check fails (CI-friendly). Requires Chromium via
Playwright; where a browser is unavailable the run prints a clear message and the
Go tests remain the authoritative role-separation proof.
