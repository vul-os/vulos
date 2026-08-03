# Client Type Safety

How the React/JSX shell gets **compile-time type checking without leaving JSX**. Types are added as JSDoc annotations checked by `tsc --noEmit`; no `.ts` or `.tsx` file is ever created, and no runtime or build behaviour changes.

For the frozen-stack rule this operates under see [`../ROADMAP.md`](../ROADMAP.md) ("Settled invariants") and `docs/decisions.md` D95. For the client SDK modules this targets first see [`OFFLINE-AUTH.md`](OFFLINE-AUTH.md) (`masterKey.js`) and [`FILES.md`](FILES.md) (sealed sharing / `contentSeal.js`).

> **Goal.** Catch shape errors at author time in the two places they are most expensive — the **security-critical client SDK** (`src/lib/`) and the **Go→JS API boundary** — using gradual, file-by-file opt-in that leaves every existing component untouched.
> **Non-goals.** Converting `.jsx` to `.tsx` (forbidden by the frozen stack). A repo-wide migration. Runtime type validation. Blocking on 100% coverage — partial adoption is the intended end state, not a stepping stone to full TypeScript.
> **Status.** Design. Nothing built. TYPE-01 is the only prerequisite; the rest are independent and can land in any order.

---

## Why here, why now

The shell is **~48.9k lines of `.js`/`.jsx`** with essentially no type information:

| Signal | Count |
|---|---|
| Files using `prop-types` | 0 |
| Files with any JSDoc type annotation (`@typedef` / `@param {`) | 8 |
| `tsconfig.json` / `jsconfig.json` | none |
| `@types/react`, `@types/react-dom` in `devDependencies` | **already present** |

Two properties of Vulos specifically make untyped JS more expensive here than in a typical frontend:

**1. `src/lib/` is a security boundary, not UI code.** `contentSeal.js`, `masterKey.js`, `offlineAuth.js`, `offlineQueue.js`, and `stepup.js` implement crypto envelopes and the step-up auth state machine. A silent shape mismatch — a key arriving as a base64 `string` where a `Uint8Array` was expected, an `undefined` that stringifies to `"undefined"` before being sealed — is a **security** defect, not a rendering glitch. This is the same class of logic bug identified as the project's real risk surface in D95; types attack it directly.

**2. The backend is statically typed and discards that at the wire.** ~145k lines of production Go define every response shape, and all of it is erased the moment a payload crosses into `src/lib/api.js`. That boundary is mechanically typeable from definitions that already exist.

Secondary: the largest components are past the size where shapes fit in a reader's head — `src/core/Settings.jsx` (2,823 lines), `src/auth/Setup.jsx` (2,646), `src/builtin/drive/Drive.jsx` (2,012).

---

## Compatibility with the frozen stack

The invariant in [`../ROADMAP.md`](../ROADMAP.md) reads *"React/JSX only (never `.tsx`)"*. This track does not touch it:

| Constraint | How this track satisfies it |
|---|---|
| No `.tsx` files | Components stay `.jsx`. Types are JSDoc comments inside them. |
| No `.ts` files | Runtime modules stay `.js`. The only new TypeScript-syntax files are **`.d.ts` declarations**, which emit nothing and are never imported at runtime. |
| Single-binary build unaffected | `tsc` runs as a **lint step** (`--noEmit`), never in the Vite build. Vite/esbuild behaviour is byte-identical. |
| No new runtime dependency | `typescript` is a `devDependency`. Nothing ships. |

If a future maintainer wants real `.ts` in `src/lib/`, that is a **separate decision requiring an invariant amendment** — deliberately out of scope here.

---

## Phases

Each phase is independently landable and independently revertible. TYPE-01 gates the others; TYPE-02/03/04 are parallel.

| ID | What | Touches |
|---|---|---|
| **TYPE-01** | Baseline config: `tsconfig.json` with `allowJs: true`, `checkJs: false`, `strict: true`, `noEmit: true`, `jsx: "react-jsx"`. Add `npm run typecheck`. Wire a **non-blocking** CI job. | `tsconfig.json`, `package.json`, `.github/workflows/` |
| **TYPE-02** | Type the SDK: add `// @ts-check` file-by-file across `src/lib/`, starting with `masterKey.js` → `contentSeal.js` → `offlineAuth.js` → `stepup.js` → `offlineQueue.js`. Shapes expressed as `@typedef`. | `src/lib/*.js` (comments only) |
| **TYPE-03** | Type the API boundary: emit `src/lib/api.types.d.ts` describing every response shape, consumed by `api.js` and `src/lib/net/endpoints.js` via `@type` imports. | new `.d.ts`, `src/lib/api.js` |
| **TYPE-04** | Extend opt-in to shell core — `src/providers/`, `src/core/AppRegistry.js`, `src/lib/net/`. Explicitly **not** the large leaf components. | `src/core/`, `src/providers/` |
| **TYPE-05** | Enforcement: flip the CI job to blocking for already-checked files; add an ESLint rule so a file that has `// @ts-check` cannot silently lose it. | `eslint.config.js`, CI |

### TYPE-03 needs a source of truth

There is **no OpenAPI spec and no `.proto` in the repo today** — handlers write responses directly. So TYPE-03 must first pick one:

| Option | Cost | Note |
|---|---|---|
| Hand-written `.d.ts` | Low | Fast, but drifts from Go silently — the failure mode this track exists to prevent. |
| Small Go→`.d.ts` generator over response structs | Medium | Reflection or `go/ast` over annotated structs; no new runtime dep; stays inside the Go toolchain. |
| Introduce an OpenAPI spec, generate both sides | High | Best long-term, but a much larger commitment than this track — would deserve its own decision. |

**Recommendation: the Go→`.d.ts` generator**, run via `go generate` and checked in, with CI failing if regeneration produces a diff. It keeps the Go structs authoritative and makes drift a build error.

---

## What "done" looks like

Partial coverage is the target, not a milestone on the way to full TypeScript:

- Every module in `src/lib/` carries `// @ts-check` and passes `strict`.
- Every API response shape is described in a generated `.d.ts`, with drift caught in CI.
- `npm run typecheck` is green and blocking.
- Component files are converted **opportunistically** — a `// @ts-check` added when someone is already editing a file for another reason. `Settings.jsx` and `Setup.jsx` may never be checked, and that is an acceptable end state.

---

## Risks

| Risk | Mitigation |
|---|---|
| `strict: true` on a large untyped file produces an unusable error count | `checkJs: false` globally; opt-in is strictly per-file via `// @ts-check`. A file is only checked once someone has made it pass. |
| JSDoc types drift from runtime behaviour | Same exposure as any type system without runtime validation. Types complement the existing vitest/msw suite; they do not replace it. |
| Read as the first step of a full TypeScript migration | It is not. The frozen stack stands (D95); crossing to `.ts` requires an explicit amendment. |
| Generated `.d.ts` goes stale | CI regenerates and fails on diff (TYPE-03). |
