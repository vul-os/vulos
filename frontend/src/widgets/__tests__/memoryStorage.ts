// memoryStorage.ts — a real Storage for tests that exercise persistence.
//
// THIS SUITE'S ENVIRONMENT HAS NO WORKING localStorage. `globalThis.localStorage`
// resolves to a plain empty object with no getItem/setItem/key/length, so every
// persistence path silently degrades to "no store" — which is exactly the state
// in which a test asserting "the layout round-trips" would PASS by asserting
// nothing.
//
// Installing a real in-memory Storage is therefore not a convenience, it is what
// makes those tests capable of failing at all. It is the ENVIRONMENT being
// repaired, never the assertion being loosened: the code under test is
// unchanged and still has to do the right thing.

export class MemoryStorage implements Storage {
  private map = new Map<string, string>()

  get length(): number { return this.map.size }
  key(i: number): string | null { return [...this.map.keys()][i] ?? null }
  getItem(k: string): string | null { return this.map.has(k) ? this.map.get(k)! : null }
  setItem(k: string, v: string): void { this.map.set(String(k), String(v)) }
  removeItem(k: string): void { this.map.delete(k) }
  clear(): void { this.map.clear() }
  [name: string]: unknown
}

let saved: Storage | undefined
let installed: MemoryStorage | null = null

export function installMemoryStorage(): MemoryStorage {
  if (!installed) {
    saved = globalThis.localStorage
    installed = new MemoryStorage()
    Object.defineProperty(globalThis, 'localStorage', {
      value: installed, configurable: true, writable: true,
    })
  }
  installed.clear()
  return installed
}

export function uninstallMemoryStorage(): void {
  if (!installed) return
  Object.defineProperty(globalThis, 'localStorage', {
    value: saved, configurable: true, writable: true,
  })
  installed = null
}
