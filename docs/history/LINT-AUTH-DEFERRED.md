# Deferred Auth-Area Lint Findings

These findings were left unresolved because fixing them would require behavior or
architecture changes that fall outside a lint-only cleanup.

---

## src/auth/AuthProvider.jsx:64

**Rule:** `react-refresh/only-export-components`

**Message:** Fast refresh only works when a file only exports components. Use a
new file to share constants or functions between components.

**Reasoning:** The file exports both the `AuthProvider` component and the `useAuth`
hook. Splitting them into separate files would be an architectural refactor
(callers that import `{ useAuth }` from this path would need updating). This is
not a pure lint fix.

---

## src/auth/Setup.jsx:731

**Rule:** `react-hooks/set-state-in-effect`

**Message:** Avoid calling setState() directly within an effect — can trigger
cascading renders.

**Reasoning:** The `IS09_poll()` function is called directly inside a `useEffect`
body to start an immediate poll on mount. `IS09_poll` calls `setState` internally
when it receives data. Restructuring this to avoid the synchronous-within-effect
pattern would require changing the polling architecture (e.g. moving the initial
call into a callback or using `useLayoutEffect`), which is a behavioral change.

---

## src/auth/Setup.jsx:2104

**Rule:** `react-hooks/exhaustive-deps` (warning)

**Message:** React Hook useEffect has missing dependencies: 'IS05_hostnameEdited'
and 'update'.

**Reasoning:** The effect in `IS05_IdentityStep` fetches `/api/identity` on mount
only. Adding `IS05_hostnameEdited` would cause a re-fetch every time the user edits
the hostname field, which is unintended. Adding `update` (a local arrow function
defined on every render) would also cause spurious re-fetches unless it is
stabilized with `useCallback` in the parent — a behavioral/structural change.

---

## src/auth/Setup.jsx:2619

**Rule:** `react-hooks/exhaustive-deps` (warning)

**Message:** React Hook useEffect has a missing dependency: 'config'.

**Reasoning:** The effect in `IS05_RecoveryKitStep` intentionally depends on
specific `config` fields (`config.IS05_ulid`, `config.IS05_hostname`,
`config.IS05_storageEnabled`, `config.IS05_sshFingerprint`) rather than the whole
`config` object. Adding `config` as a dependency would cause the payload to rebuild
on every render because `config` is a plain object that gets a new reference on
each parent re-render. The correct fix would be to memoize the relevant config
slice in the parent — a structural change.
