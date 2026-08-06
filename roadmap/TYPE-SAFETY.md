# Client Type Safety

How the React shell is gaining compile-time type checking via a **TypeScript (TS7) migration**, starting with the security-critical `src/lib/` SDK.

> **History.** This document originally described *gradual JSDoc-only* typing (`// @ts-check` comments inside `.js` files, zero `.ts`/`.tsx` files ever) as the way to add types without crossing the frozen stack's "never `.tsx`" line. That line is **overturned** — see [`../docs/decisions.md`](../docs/decisions.md) D97. `src/lib/` is migrating to real TypeScript. This document is rewritten to describe that migration; the reasoning that motivated the effort in the first place (security boundary, Go→JS wire) is unchanged and is restated below.

For the (amended) stack rule this operates under, see [`../ROADMAP.md`](../ROADMAP.md) ("Settled invariants") and `docs/decisions.md` D97. For the client SDK modules this targets first, see [`OFFLINE-AUTH.md`](OFFLINE-AUTH.md) (`masterKey.js`) and [`FILES.md`](FILES.md) (sealed sharing / `contentSeal.js`).

> **Goal.** Catch shape errors at author time in the two places they are most expensive — the **security-critical client SDK** (`src/lib/`) and the **Go→JS API boundary** — by migrating those modules to real TypeScript, file by file.
> **Non-goals.** A repo-wide rewrite in one pass — `src/lib/` first, everything else is a later, separate call. Runtime type validation is a *required companion discipline*, not something a type system substitutes for (see "Parse, don't cast" below). 100% coverage as a blocker — the migration proceeds directory by directory, each landing green (typecheck, tests, lint, build) before the next starts. Components are now IN scope: the earlier "`.jsx` stays" framing was superseded once `src/lib/` landed cleanly.
> **Status.** Foundation landed: `tsconfig.json` (`allowJs: true`, `checkJs: false`, `strict: true` for new TS), `npm run typecheck`, wired into CI, **probe-verified** (confirmed to actually fail on a planted type error — a checked, not assumed, gate). `src/lib/` file-by-file migration is TYPE-02, in progress.

---

## Why here, why now

The shell is **~48.9k lines of `.js`/`.jsx`** with essentially no type information going into this track:

| Signal | Count |
|---|---|
| Files using `prop-types` | 0 |
| Files with any JSDoc type annotation (`@typedef` / `@param {`) | 8 |
| `tsconfig.json` | now present (this track) |
| `@types/react`, `@types/react-dom` in `devDependencies` | already present |

Two properties of Vulos specifically make untyped JS more expensive here than in a typical frontend — these are the reasons the migration was undertaken, carried over unchanged from the original design and restated in `docs/decisions.md` D97:

**1. `src/lib/` is a security boundary, not UI code.** `contentSeal.js`, `masterKey.js`, `offlineAuth.js`, `offlineQueue.js`, and `stepup.js` implement crypto envelopes and the step-up auth state machine. A silent shape mismatch — a key arriving as a base64 `string` where a `Uint8Array` was expected, an `undefined` that stringifies to `"undefined"` before being sealed — is a **security** defect, not a rendering glitch.

**2. The backend is statically typed and discards that at the wire.** ~145k lines of production Go define every response shape, and all of it is erased the moment a payload crosses into `src/lib/api.ts`. That boundary is mechanically typeable from definitions that already exist — generated wire types structurally prevent the drift that has been this project's dominant defect class.

Secondary: the largest components are past the size where shapes fit in a reader's head — `src/core/Settings.tsx` (2,823 lines), `src/auth/Setup.tsx` (2,646), `src/builtin/drive/Drive.tsx` (2,012). These stay `.jsx` under this phase; converting them to `.tsx` is a future, separate decision — not implied by D97, which authorizes `src/lib/` TypeScript specifically.

---

## The honest counterweight (D97)

Types are worth adopting for the reasons above, but they are not a general fix for this project's demonstrated weakness. The actual defects found and fixed in the session that produced D97 were, without exception, semantic or verification failures that TypeScript would have caught **none** of:

- an invalid `-env` value
- an artifact-dropping shell glob
- a screenshot that showed the wrong thing
- a swallowed promise rejection
- a smoke gate that passed on the wrong signal

This is recorded so the migration is never oversold as a general defect-rate fix — its actual, defensible scope is crypto/auth shape errors and Go↔JS wire drift, and nothing broader.

## Parse, don't cast

Types at a trust boundary are worthless without runtime validation. Casting unvalidated JSON to a TypeScript interface (`const x = json as MyType`) is **typed fiction** — and it is worse than staying untyped, because it *reads* as safe while it has validated nothing. Every place `src/lib/api.ts` (or its TS successor) and `src/lib/net/endpoints.ts` receive a network response must parse and validate the shape at runtime, not merely assert a compile-time type onto it. This applies whether the type comes from a hand-written `.d.ts` or the Go-generated one described in TYPE-03 below.

## vulos-cloud is deliberately NOT migrating

vulos-cloud is a static content site. Its real failure modes — broken links, stale synced screenshots, layout regressions — are not shape errors; TypeScript does not catch them. Its existing render/link gates (link checks, screenshot diffing) catch far more per unit effort there than a type system would. This track is scoped to the OS shell's `src/lib/`; it does not extend to vulos-cloud.

---

## Compatibility with the (amended) stack rule

The invariant in [`../ROADMAP.md`](../ROADMAP.md) used to read *"React/JSX only (never `.tsx`)"*. Per D97 that line no longer holds. What still holds:

| Constraint | Current state |
|---|---|
| Component files (`.jsx`) | Unchanged for now — this phase targets `src/lib/`, not components. Converting a component to `.tsx` is a separate, future decision, not authorized by D97. |
| Single-binary build unaffected | `tsc` runs as a lint step (`--noEmit`) via `npm run typecheck` in CI; Vite/esbuild behaviour is unchanged. |
| Existing untouched `.js` keeps building | `allowJs: true`, `checkJs: false` globally — the ~48.9k lines of JS not yet migrated are not type-checked and need not pass anything. |
| Migrated modules are strict | `strict: true` applies to files that have actually become `.ts`. |

## oxlint evaluated and rejected

Considered alongside the TS migration as a faster ESLint replacement. Run on an identical probe (two deliberately planted error-level violations): oxlint caught `no-unused-vars` and `exhaustive-deps` but **missed `react-hooks/rules-of-hooks` entirely** — the rule that catches conditional/early-return hooks, a real bug class in this shell. Faster is not worth silently dropping that rule. ESLint stays as the linter of record; oxlint is not adopted.

---

## Phases

Each phase is independently landable and independently revertible. TYPE-01 gates the others; TYPE-02/03/04 are parallel.

| ID | What | Touches | Status |
|---|---|---|---|
| **TYPE-01** | Baseline config: `tsconfig.json` with `allowJs: true`, `checkJs: false`, `strict: true`, `noEmit: true`, `jsx: "react-jsx"`. `npm run typecheck`. CI job. | `tsconfig.json`, `package.json`, `.github/workflows/` | **Landed**, probe-verified. |
| **TYPE-02** | Migrate the SDK to real TypeScript, file-by-file: `masterKey.js` → `contentSeal.js` → `offlineAuth.js` → `stepup.js` → `offlineQueue.js` become `.ts`. Runtime validation added at every boundary the module receives untrusted input (parse, don't cast). | `src/lib/*.ts` | In progress. |
| **TYPE-03** | Type the API boundary: emit `src/lib/api.types.d.ts` describing every response shape, consumed by `api.js`/its TS successor and `src/lib/net/endpoints.ts`. Runtime-validated on receipt, not just typed. | new `.d.ts`, `src/lib/api.ts` | Design (source-of-truth choice below). |
| **TYPE-04** | Extend to shell core — `src/providers/`, `src/core/AppRegistry.ts`, `src/lib/net/`. Explicitly **not** the large leaf components. | `src/core/`, `src/providers/` | Not started. |
| **TYPE-05** | Enforcement: flip the CI job to blocking for migrated files; ESLint rule so a `.ts` file can't silently regress or lose coverage. | `eslint.config.js`, CI | Not started. |

### TYPE-03 needs a source of truth

There is **no OpenAPI spec and no `.proto` in the repo today** — handlers write responses directly. So TYPE-03 must first pick one:

| Option | Cost | Note |
|---|---|---|
| Hand-written `.d.ts` | Low | Fast, but drifts from Go silently — the failure mode this track exists to prevent. |
| Small Go→`.d.ts` generator over response structs | Medium | Reflection or `go/ast` over annotated structs; no new runtime dep; stays inside the Go toolchain. |
| Introduce an OpenAPI spec, generate both sides | High | Best long-term, but a much larger commitment than this track — would deserve its own decision. |

**Recommendation: the Go→`.d.ts` generator**, run via `go generate` and checked in, with CI failing if regeneration produces a diff. It keeps the Go structs authoritative and makes drift a build error. The generated type alone is not sufficient by itself — the receiving code must still validate at runtime, not just cast (see "Parse, don't cast" above).

---

## What "done" looks like

Partial coverage is the target for this phase, not a milestone on the way to a full rewrite:

- Every module in `src/lib/` is real `.ts`, passes `strict`, and validates untrusted input at runtime rather than casting it.
- Every API response shape is described in a generated `.d.ts`, with drift caught in CI.
- `npm run typecheck` is green and blocking.
- Component files stay `.jsx` for this phase. Whether and when to migrate `Settings.jsx` (2,823 lines) or `Setup.jsx` (2,646 lines) to `.tsx` is a future, separate decision — not implied by D97.

---

## Risks

| Risk | Mitigation |
|---|---|
| `strict: true` produces an unusable error count mid-migration | Migrate file-by-file in the order given above (TYPE-02); a file only ships once it actually passes. |
| Types drift from runtime behaviour, or give false confidence at a boundary | Types complement the existing vitest/msw suite, they do not replace it; and per "Parse, don't cast," compile-time types alone are never trusted at a boundary — runtime validation is required there regardless. |
| Read as "this fixes our defect rate" | It does not — see "The honest counterweight" above. Every defect actually found in the D97 session was a semantic/verification failure TypeScript would not have caught. |
| Read as license to convert components to `.tsx` | Out of scope for this phase. D97 authorizes `src/lib/` TypeScript, not a component rewrite. |
| Generated `.d.ts` goes stale | CI regenerates and fails on diff (TYPE-03). |
| vulos-cloud gets swept into this by habit | Explicitly excluded — see "vulos-cloud is deliberately NOT migrating" above. |
