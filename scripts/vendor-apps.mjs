#!/usr/bin/env node
/**
 * vendor-apps.mjs — regenerate the self-hosted, same-origin JS bundles for the
 * installed OS apps that would otherwise load libraries from the esm.sh CDN.
 *
 * SOVEREIGNTY: a self-host / air-gapped Vulos OS must never depend on a
 * third-party CDN to open an app, and must not leak user activity to esm.sh.
 * The `sheets` and `text-editor` apps therefore ship their JS deps locally in
 * `apps/<id>/vendor/*.js`, referenced by a same-origin `<script type=importmap>`.
 *
 * This script installs the exact pinned versions, bundles each import-map
 * specifier with esbuild (code-splitting ON so shared deps like
 * @codemirror/state, @codemirror/view, yjs and lib0 stay single instances),
 * and copies the outputs into each app's vendor/ dir.
 *
 * Usage:  node scripts/vendor-apps.mjs
 * Requires: network access (npm install of the pinned versions) + npx esbuild.
 *
 * If you bump a version here, also bump the version in the matching app's
 * index.html importmap comment so the two stay in sync.
 */
import { execSync } from 'node:child_process'
import { mkdtempSync, mkdirSync, writeFileSync, copyFileSync, readFileSync, readdirSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, dirname } from 'node:path'
import { fileURLToPath } from 'node:url'

const repoRoot = join(dirname(fileURLToPath(import.meta.url)), '..')

// Import-map specifier -> pinned npm package@version. Output file is derived
// from the entry name (e.g. @lezer/highlight -> lezer-highlight.js).
const MODS = {
  'state':            '@codemirror/state@6.4.1',
  'view':             '@codemirror/view@6.34.3',
  'commands':         '@codemirror/commands@6.7.1',
  'language':         '@codemirror/language@6.10.3',
  'search':           '@codemirror/search@6.5.8',
  'autocomplete':     '@codemirror/autocomplete@6.18.4',
  'lint':             '@codemirror/lint@6.8.4',
  'theme-one-dark':   '@codemirror/theme-one-dark@6.1.2',
  'lang-javascript':  '@codemirror/lang-javascript@6.2.2',
  'lang-python':      '@codemirror/lang-python@6.1.6',
  'lang-html':        '@codemirror/lang-html@6.4.9',
  'lang-css':         '@codemirror/lang-css@6.3.1',
  'lang-json':        '@codemirror/lang-json@6.0.1',
  'lang-markdown':    '@codemirror/lang-markdown@6.3.1',
  'lang-sql':         '@codemirror/lang-sql@6.8.0',
  'lang-xml':         '@codemirror/lang-xml@6.1.0',
  'lezer-highlight':  '@lezer/highlight@1.2.1',
  'yjs':              'yjs@13.6.18',
  'y-websocket':      'y-websocket@1.5.4',
  'y-codemirror.next': 'y-codemirror.next@0.3.5',
  'lib0':             'lib0@0.2.99',
}

// Which entry names each app consumes (transitive shared chunks are copied too).
const APP_ENTRIES = {
  'sheets': ['yjs', 'y-websocket'],
  'text-editor': Object.keys(MODS),
}

const bare = (v) => v.replace(/^(@[^/]+\/)?[^@]+@.*/, (_, s) => (s || '') + v.split('@').slice(s ? 1 : 0).join('@').split('@')[0])

const work = mkdtempSync(join(tmpdir(), 'vulos-vendor-'))
console.log('workdir:', work)
try {
  writeFileSync(join(work, 'package.json'), JSON.stringify({ name: 'vendor', private: true, type: 'module' }))
  const pkgs = Object.values(MODS).map((p) => `"${p}"`).join(' ')
  console.log('installing pinned versions…')
  execSync(`npm install --no-save ${pkgs}`, { cwd: work, stdio: 'inherit' })

  const entriesDir = join(work, 'entries')
  mkdirSync(entriesDir, { recursive: true })
  for (const [name, pkgv] of Object.entries(MODS)) {
    const pkg = pkgv.replace(/@[^@]+$/, '').replace(/^(@[^/]+\/.+)@.*/, '$1')
    // strip trailing @version (handles scoped names)
    const clean = pkgv.replace(/@(\d[^@]*)$/, '')
    writeFileSync(join(entriesDir, `${name}.js`), `export * from '${clean}';\n`)
  }

  const outDir = join(work, 'out')
  console.log('bundling with esbuild…')
  execSync(`npx --yes esbuild entries/*.js --bundle --format=esm --splitting --outdir=out --minify --log-level=warning`, { cwd: work, stdio: 'inherit' })

  // Reachability: for each app, copy its entry files + their relative chunk closure.
  const readDeps = (dir, file, seen = new Set()) => {
    if (seen.has(file)) return seen
    seen.add(file)
    const src = readFileSync(join(dir, file), 'utf8')
    for (const m of src.matchAll(/(?:from|import)"(\.\/[^"]+)"/g)) readDeps(dir, m[1].slice(2), seen)
    return seen
  }
  for (const [app, entries] of Object.entries(APP_ENTRIES)) {
    const closure = new Set()
    for (const e of entries) for (const f of readDeps(outDir, `${e}.js`)) closure.add(f)
    const vendorDir = join(repoRoot, 'apps', app, 'vendor')
    rmSync(vendorDir, { recursive: true, force: true })
    mkdirSync(vendorDir, { recursive: true })
    for (const f of closure) copyFileSync(join(outDir, f), join(vendorDir, f))
    console.log(`apps/${app}/vendor: ${closure.size} files`)
  }
  console.log('done. Verify no esm.sh references remain in apps/*/index.html.')
} finally {
  rmSync(work, { recursive: true, force: true })
}
