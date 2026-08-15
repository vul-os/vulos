import '@testing-library/jest-dom'

// ─────────────────────────────────────────────────────────────────────────────
// Node 25 ships its own global `localStorage`/`sessionStorage`, gated on the
// `--localstorage-file` flag. Started without a valid path — which is every
// ordinary `npx vitest` run — the global still EXISTS but is inert: `clear`,
// `getItem` and friends are undefined, and Node prints
//
//     Warning: `--localstorage-file` was provided without a valid path
//
// In the jsdom environment that inert global SHADOWS jsdom's working
// `window.localStorage`, so every test that touches storage dies on
// `localStorage.clear is not a function`.
//
// This is worth a comment rather than a one-liner because of how it presents:
// on Node 25 it failed 38 tests across 9 files — appBridge's storage-namespacing
// suite, the shell-state geometry round-trips, offline queue — and every one of
// them reads as a product defect in a security-relevant area. It is not. The
// same files pass on Node 22, and they failed identically at the pre-work
// commit. A machine where "the suite is green" cannot be checked is exactly the
// condition under which a real regression hides, so the shadowing is corrected
// here rather than worked around per-file.
//
// There is no working implementation to fall back TO. In vitest's jsdom
// environment `globalThis` IS the window, so Node's inert binding occupies
// `window.localStorage` as well — reaching for jsdom's own finds the same
// broken object. So a real one is installed here.
//
// Detection is by capability, not by version: a binding that already behaves
// like Storage is left untouched, so this is a no-op on Node 22, and becomes a
// no-op again the day Node's implementation works. It must not silently
// substitute a double for a functioning browser API.
function installStorage(name: 'localStorage' | 'sessionStorage'): void {
  const current = (globalThis as Record<string, unknown>)[name] as Storage | undefined
  if (typeof current?.clear === 'function') return

  const map = new Map<string, string>()
  // Spec-shaped on the points tests actually depend on: keys and values are
  // coerced to strings (a test storing a number must read one back), missing
  // keys are null rather than undefined, and `key(i)` follows insertion order.
  const storage: Storage = {
    get length() {
      return map.size
    },
    key(index: number) {
      return [...map.keys()][index] ?? null
    },
    getItem(key: string) {
      return map.has(String(key)) ? (map.get(String(key)) as string) : null
    },
    setItem(key: string, value: string) {
      map.set(String(key), String(value))
    },
    removeItem(key: string) {
      map.delete(String(key))
    },
    clear() {
      map.clear()
    },
  }

  Object.defineProperty(globalThis, name, {
    value: storage,
    configurable: true,
    writable: true,
  })
}

installStorage('localStorage')
installStorage('sessionStorage')
