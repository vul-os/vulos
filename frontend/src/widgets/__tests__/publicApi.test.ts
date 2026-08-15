// publicApi.test.ts — the gate that keeps the public API honest.
//
// THE CLAIM THIS FILE EXISTS TO CHECK: a third party can build a real widget
// using only what `src/widgets/index.ts` exports.
//
// That claim is unfalsifiable by inspection — it is very easy to write an API,
// build the builtins AROUND it by importing host internals, and then document
// the API as if it were sufficient. Every builtin here therefore imports from
// the public entry point and nothing else, and this test reads their sources to
// prove it. If the API were missing something, a builtin would have had to
// reach past it and this test would name the file.
//
// Reading the sources off disk (the same technique as src/shell/zLayers.test.ts)
// rather than inspecting the module graph is deliberate: the property is about
// what the code is ALLOWED to say, and a bundler will happily resolve an import
// that should never have been written.

import { describe, it, expect } from 'vitest'
import { readFileSync, readdirSync } from 'node:fs'
import { join } from 'node:path'

const BUILTIN_DIR = 'src/widgets/builtin'
const EXAMPLE_DIR = 'src/widgets/examples'
const PUBLIC_ENTRY = 'src/widgets/index.ts'

function sourcesIn(dir: string): { name: string; path: string; text: string }[] {
  return readdirSync(dir)
    .filter((n) => /\.tsx?$/.test(n) && n !== 'index.ts' && !/\.test\./.test(n))
    .map((name) => {
      const path = join(dir, name)
      return { name, path, text: readFileSync(path, 'utf8') }
    })
}

/** Every `from '...'` specifier in a source file. */
function importsOf(text: string): string[] {
  return [...text.matchAll(/(?:^|\n)\s*import[^\n]*?from\s+['"]([^'"]+)['"]/g)].map((m) => m[1])
}

describe('builtin widgets are built through the public API', () => {
  const files = [...sourcesIn(BUILTIN_DIR), ...sourcesIn(EXAMPLE_DIR)]

  it('found the widget sources', () => {
    // A glob that matched nothing would make every assertion below vacuously
    // true — the exact shape of a gate that prints PASS while checking nothing.
    expect(files.length).toBeGreaterThanOrEqual(8)
    const names = files.map((f) => f.name)
    expect(names).toContain('worldClock.tsx')
    expect(names).toContain('stocks.tsx')
    expect(names).toContain('moon.ts')
  })

  it.each([...sourcesIn(BUILTIN_DIR), ...sourcesIn(EXAMPLE_DIR)].map((f) => [f.name, f] as const))(
    '%s imports only the public entry, React, and its own sibling helpers',
    (_name, file) => {
      const allowed = new Set(['react', '../index', './logic'])
      const offenders = importsOf(file.text).filter((spec) => !allowed.has(spec))
      expect(offenders, `${file.path} reaches past the public API`).toEqual([])
    },
  )

  it('no widget reaches into the host, the registry internals or the shell', () => {
    for (const f of files) {
      // The specific escapes that would matter: host machinery, the shell's own
      // components, and the box's API modules.
      expect(f.text, f.path).not.toMatch(/from\s+['"][^'"]*\/host\//)
      expect(f.text, f.path).not.toMatch(/from\s+['"][^'"]*\/shell\//)
      expect(f.text, f.path).not.toMatch(/from\s+['"][^'"]*\/core\//)
      expect(f.text, f.path).not.toMatch(/from\s+['"][^'"]*\/builtin\/(?!.)/)
      // …and the one that would quietly break the sovereignty claim.
      expect(f.text, f.path).not.toMatch(/\bfetch\s*\(/)
      expect(f.text, f.path).not.toMatch(/XMLHttpRequest|new WebSocket|EventSource/)
    }
  })

  it('every widget declares its permissions and nothing beyond them', () => {
    // A widget that names a capability in its code without requesting it in its
    // manifest would render a permanently dead branch.
    for (const f of files) {
      const manifestPerms = /permissions:\s*\[([^\]]*)\]/.exec(f.text)?.[1] ?? ''
      const declared = new Set([...manifestPerms.matchAll(/'([a-z]+)'/g)].map((m) => m[1]))
      const uses: [RegExp, string][] = [
        [/ctx\.storage\b/, 'storage'],
        [/ctx\.telemetry\b/, 'telemetry'],
        [/ctx\.calendar\b/, 'calendar'],
        [/ctx\.notifications\b/, 'notifications'],
        [/ctx\.openApp\b/, 'launch'],
        [/ctx\.notify\b/, 'notify'],
        [/ctx\.net\b/, 'network'],
      ]
      for (const [re, perm] of uses) {
        if (re.test(f.text)) {
          expect(declared.has(perm), `${f.path} uses ctx.${perm === 'launch' ? 'openApp' : perm} without requesting "${perm}"`).toBe(true)
        }
      }
    }
  })
})

describe('the public entry point', () => {
  const entry = readFileSync(PUBLIC_ENTRY, 'utf8')

  it('exports the three things a widget author cannot work without', () => {
    for (const name of ['defineWidget', 'defineSandboxedWidget', 'registerWidget']) {
      expect(entry).toContain(name)
    }
  })

  it('does NOT export the host internals', () => {
    // Exporting the rail, the bridge or the layout store would make them public
    // API by accident, and every one of them is a thing a widget must not touch.
    for (const forbidden of ['WidgetRail', 'handleWidgetMessage', 'saveLayout', 'widgetSet', 'storageFor']) {
      expect(entry, `${forbidden} must not be public`).not.toContain(forbidden)
    }
  })

  it('points a developer at the documentation', () => {
    expect(entry).toMatch(/docs\/WIDGETS\.md/)
  })
})

describe('the shipped catalogue', () => {
  it('registers every widget module exactly once', () => {
    const index = readFileSync(join(BUILTIN_DIR, 'index.ts'), 'utf8')
    const modules = sourcesIn(BUILTIN_DIR).filter((f) => f.name !== 'logic.ts')
    for (const m of modules) {
      const stem = m.name.replace(/\.tsx?$/, '')
      expect(index, `${m.name} is not in the catalogue`).toContain(`'./${stem}'`)
    }
    // The sandboxed third-party example ships too, so the untrusted lane is
    // exercised on every boot rather than only in a test.
    expect(index).toContain("'../examples/moon'")
  })

  it('ships at least one SANDBOXED widget', async () => {
    const moon = readFileSync(join(EXAMPLE_DIR, 'moon.ts'), 'utf8')
    expect(moon).toContain('defineSandboxedWidget')
    // A sandboxed widget's code is a string, not a React tree: it cannot import
    // anything at runtime and cannot touch the shell.
    expect(moon).not.toMatch(/\bfrom ['"]react['"]/)
  })
})
