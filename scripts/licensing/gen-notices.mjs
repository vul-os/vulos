#!/usr/bin/env node
/**
 * gen-notices.mjs — generate THIRD_PARTY_NOTICES.md from the ACTUAL dependency
 * graph, with the full licence text of every dependency we redistribute.
 *
 * WHY: Vulos ships (and sells) an image that contains third-party binaries and
 * bundled third-party JavaScript. Apache-2.0 §4(d), the BSD/MIT clauses and the
 * GPL/LGPL notice requirements all oblige a redistributor to reproduce the
 * copyright notices and licence texts of what it distributes. A hand-written
 * list goes stale the moment someone runs `go get`; this file is generated from
 * the graph so it cannot drift.
 *
 * Sources of truth (nothing here is hand-typed):
 *   Go   — `go list -deps` over backend/./... → the modules actually linked into
 *          vulos-server / vulos-init → licence file read from the module cache.
 *   npm  — the production closure in package-lock.json (dev deps are build-only
 *          and are not shipped) → licence file read from node_modules/.
 *   apps — apps/<app>/vendor/LICENSES/index.json, written by vendor-apps.mjs
 *          from the pinned packages it bundles.
 *   deb  — the Debian package manifest produced by collect-corresponding-source.sh
 *          against the built rootfs (--deb-manifest). Debian ships each package's
 *          copyright file at /usr/share/doc/<pkg>/copyright inside the image, so
 *          those are referenced rather than inlined (they travel with the binary).
 *
 * Usage:
 *   node scripts/licensing/gen-notices.mjs [--out FILE] [--deb-manifest FILE] [--check]
 *
 *   --check   regenerate into memory and exit non-zero if the on-disk file
 *             differs (CI guard against a stale notices file).
 */
import { execFileSync } from 'node:child_process'
import { existsSync, readFileSync, readdirSync, writeFileSync, mkdirSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { detectSPDX, LICENSE_FILENAMES } from './spdx.mjs'

const REPO = join(dirname(fileURLToPath(import.meta.url)), '..', '..')
// The web tier lives in frontend/ (alongside backend/ and clients/), so the npm
// lockfile, node_modules and the bundled apps/ all resolve from there.
const FRONTEND = join(REPO, 'frontend')

// Modules/packages that are ours: no third-party notice is owed for them.
const FIRST_PARTY = [/^vulos\//, /^github\.com\/vul-os\//, /^@vulos\//]
const isFirstParty = (name) => FIRST_PARTY.some((re) => re.test(name))

const args = process.argv.slice(2)
const argOf = (flag) => {
  const i = args.indexOf(flag)
  return i >= 0 ? args[i + 1] : null
}
const OUT = argOf('--out') || join(REPO, 'THIRD_PARTY_NOTICES.md')
const DEB_MANIFEST = argOf('--deb-manifest')
const CHECK = args.includes('--check')

/** Find the licence-ish files directly inside a package directory. */
function licenseFiles(dir) {
  if (!existsSync(dir)) return []
  return readdirSync(dir, { withFileTypes: true })
    .filter((e) => e.isFile() && LICENSE_FILENAMES.test(e.name))
    .map((e) => e.name)
    .sort()
}

/** github.com/Foo -> github.com/!foo — the Go module cache's case-escape. */
const escapeModPath = (p) => p.replace(/[A-Z]/g, (c) => '!' + c.toLowerCase())

/**
 * The platforms this document must cover.
 *
 * `go list -deps ./...` resolves build constraints for the CURRENT GOOS, so the
 * dependency set — and therefore this file — used to depend on which machine
 * generated it. That is not a cosmetic difference for a licensing notice.
 *
 * Measured 2026-08-11 on this repo: a darwin-generated notices file OMITS
 * github.com/prometheus/procfs, which is reachable only from the `//go:build
 * linux` packages (cmd/init, internal/installer, services/installer) and is
 * therefore shipped in every Vulos image; and it LISTS github.com/mattn/go-isatty
 * and github.com/ncruces/go-strftime, which the linux build never includes.
 * Vulos ships as a linux OS, so the file distributed with the product was
 * under-reporting code that ships and over-reporting code that does not.
 *
 * It also made the CI check unsatisfiable: the notices were regenerated on a
 * mac, verified there, and `--check` then failed on an ubuntu runner — with a
 * message telling the developer to run the very command that had just produced
 * the file.
 *
 * Taking the UNION across a fixed platform list fixes both. The list is
 * explicit rather than derived from the host so that two machines produce
 * byte-identical output.
 */
const NOTICE_PLATFORMS = [
  { GOOS: 'linux', GOARCH: 'amd64' },
  { GOOS: 'linux', GOARCH: 'arm64' },
  { GOOS: 'darwin', GOARCH: 'arm64' },
]

function collectGo() {
  const backend = join(REPO, 'backend')
  const modcache = execFileSync('go', ['env', 'GOMODCACHE'], { cwd: backend, encoding: 'utf8' }).trim()
  const seen = new Map()
  for (const plat of NOTICE_PLATFORMS) {
    const out = execFileSync(
      'go',
      ['list', '-deps', '-f', '{{if .Module}}{{.Module.Path}}\t{{.Module.Version}}{{end}}', './...'],
      {
        cwd: backend,
        encoding: 'utf8',
        maxBuffer: 64 << 20,
        env: { ...process.env, GOOS: plat.GOOS, GOARCH: plat.GOARCH },
      },
    )
    for (const line of out.split('\n')) {
      const [path, version] = line.trim().split('\t')
      if (!path || !version || isFirstParty(path)) continue
      seen.set(`${path}@${version}`, { path, version })
    }
  }
  const mods = []
  for (const { path, version } of [...seen.values()].sort((a, b) => a.path.localeCompare(b.path))) {
    const dir = join(modcache, `${escapeModPath(path)}@${version}`)
    const files = licenseFiles(dir)
    if (files.length === 0) {
      mods.push({ name: path, version, spdx: null, text: null, missing: true })
      continue
    }
    // A module may ship LICENSE + NOTICE (Apache-2.0 §4(d) NOTICE files must be
    // reproduced too) — keep every licence-ish file, in filename order.
    const parts = files.map((f) => ({ file: f, text: readFileSync(join(dir, f), 'utf8').trimEnd() }))
    const primary = parts[0].text
    mods.push({ name: path, version, spdx: detectSPDX(primary), parts })
  }
  return mods
}

function collectNpm() {
  const lockPath = join(FRONTEND, 'package-lock.json')
  if (!existsSync(lockPath)) return []
  const lock = JSON.parse(readFileSync(lockPath, 'utf8'))
  const pkgs = []
  for (const [key, meta] of Object.entries(lock.packages || {})) {
    if (!key) continue // the root project itself
    if (meta.dev) continue // build-only: never shipped in the bundle
    if (!key.startsWith('node_modules/')) continue // file: links to our own repos
    const name = key.slice(key.lastIndexOf('node_modules/') + 'node_modules/'.length)
    if (isFirstParty(name)) continue
    const dir = join(FRONTEND, key)
    const files = licenseFiles(dir)
    const parts = files.map((f) => ({ file: f, text: readFileSync(join(dir, f), 'utf8').trimEnd() }))
    pkgs.push({
      name,
      version: meta.version || '',
      // The lock's `license` field is the publisher's own SPDX declaration.
      declared: meta.license || null,
      spdx: parts.length ? detectSPDX(parts[0].text) : null,
      parts,
      missing: parts.length === 0,
    })
  }
  const byName = new Map()
  for (const p of pkgs) byName.set(`${p.name}@${p.version}`, p)
  return [...byName.values()].sort((a, b) => a.name.localeCompare(b.name))
}

function collectVendored() {
  const appsDir = join(FRONTEND, 'apps')
  if (!existsSync(appsDir)) return []
  const out = []
  for (const app of readdirSync(appsDir).sort()) {
    const idx = join(appsDir, app, 'vendor', 'LICENSES', 'index.json')
    if (!existsSync(idx)) continue
    const entries = JSON.parse(readFileSync(idx, 'utf8'))
    for (const e of entries) {
      const textPath = e.file ? join(appsDir, app, 'vendor', 'LICENSES', e.file) : null
      out.push({
        app,
        name: e.name,
        version: e.version,
        declared: e.declared || null,
        spdx: e.spdx || null,
        text: textPath && existsSync(textPath) ? readFileSync(textPath, 'utf8').trimEnd() : null,
      })
    }
  }
  return out
}

function collectDebian() {
  if (!DEB_MANIFEST || !existsSync(DEB_MANIFEST)) return null
  const rows = []
  for (const line of readFileSync(DEB_MANIFEST, 'utf8').split('\n')) {
    if (!line.trim() || line.startsWith('#')) continue
    const [pkg, version, srcPkg, srcVersion, arch] = line.split('\t')
    if (!pkg) continue
    rows.push({ pkg, version, srcPkg, srcVersion, arch })
  }
  return rows.sort((a, b) => a.pkg.localeCompare(b.pkg))
}

// ── render ────────────────────────────────────────────────────────────────────

const fence = (text) => '```text\n' + text.replace(/```/g, "'''") + '\n```'

function render({ go, npm, vendored, debian }) {
  const L = []
  const counts = new Map()
  const bump = (id) => counts.set(id || 'see text', (counts.get(id || 'see text') || 0) + 1)
  go.forEach((m) => bump(m.spdx))
  npm.forEach((p) => bump(p.spdx || p.declared))
  vendored.forEach((v) => bump(v.spdx || v.declared))

  L.push('# Third-Party Notices')
  L.push('')
  L.push('This file is GENERATED — do not edit it by hand. Regenerate with:')
  L.push('')
  L.push('    node scripts/licensing/gen-notices.mjs')
  L.push('')
  L.push('It is produced from the dependency graph that is actually built into the')
  L.push('shipped artifacts: the Go modules linked into `vulos-server`/`vulos-init`, the')
  L.push('npm production closure bundled into the web UI, the pinned libraries vendored')
  L.push('into the OS apps, and (when generated against a built rootfs) the Debian')
  L.push('packages installed into the image.')
  L.push('')
  L.push('Vulos distributes these components. The licence text of each one is reproduced')
  L.push('below, as those licences require.')
  L.push('')
  L.push('**Source code for the GPL/LGPL-licensed components of the Vulos image** is')
  L.push('covered by the written offer in `WRITTEN-OFFER.md` (shipped in the image at')
  L.push('`/opt/vulos/legal/WRITTEN-OFFER.md`), and by the package manifest in')
  L.push('`SOURCES.manifest`.')
  L.push('')
  L.push('## Summary')
  L.push('')
  L.push('| Licence | Components |')
  L.push('|---|---|')
  for (const [id, n] of [...counts.entries()].sort((a, b) => b[1] - a[1])) L.push(`| ${id} | ${n} |`)
  L.push('')
  L.push(`Go modules: ${go.length} · npm packages: ${npm.length} · vendored app libraries: ${vendored.length}` +
    (debian ? ` · Debian packages: ${debian.length}` : ''))
  L.push('')

  L.push('---')
  L.push('')
  L.push('## Go modules (linked into vulos-server and vulos-init)')
  L.push('')
  for (const m of go) {
    L.push(`### ${m.name} ${m.version}`)
    L.push('')
    L.push(`- Licence: ${m.spdx || 'see text below'}`)
    L.push('')
    if (m.missing) {
      L.push('> No licence file was found in the module. This must be resolved before release.')
      L.push('')
      continue
    }
    for (const part of m.parts) {
      if (m.parts.length > 1) L.push(`**${part.file}**`), L.push('')
      L.push(fence(part.text))
      L.push('')
    }
  }

  L.push('---')
  L.push('')
  L.push('## npm packages (bundled into the Vulos web UI)')
  L.push('')
  L.push('Development-only dependencies are excluded: they are not part of any shipped')
  L.push('artifact. Everything below is bundled by Vite into the JavaScript we ship.')
  L.push('')
  for (const p of npm) {
    L.push(`### ${p.name} ${p.version}`)
    L.push('')
    L.push(`- Licence: ${p.spdx || p.declared || 'see text below'}${p.declared && p.spdx && p.declared !== p.spdx ? ` (package.json declares: ${p.declared})` : ''}`)
    L.push('')
    if (p.missing) {
      L.push(`> This package ships no licence file. Declared licence: ${p.declared || 'NONE DECLARED — must be resolved before release'}.`)
      L.push('')
      continue
    }
    for (const part of p.parts) {
      if (p.parts.length > 1) L.push(`**${part.file}**`), L.push('')
      L.push(fence(part.text))
      L.push('')
    }
  }

  L.push('---')
  L.push('')
  L.push('## Libraries vendored into the OS apps')
  L.push('')
  L.push('These are shipped as pre-built JavaScript bundles under `apps/<app>/vendor/`.')
  L.push('The bundler strips the source comments, so the licence text is carried')
  L.push('alongside in `apps/<app>/vendor/LICENSES/` and reproduced here.')
  L.push('')
  L.push('The list is the full install closure of the pinned packages, so it may name a')
  L.push('package whose code the bundler dropped — we would rather over-attribute than')
  L.push('miss a notice we owe.')
  L.push('')
  if (vendored.length === 0) {
    L.push('> None recorded. Run `node scripts/vendor-apps.mjs --licenses-only` to collect them.')
    L.push('')
  }
  for (const v of vendored) {
    L.push(`### ${v.name} ${v.version} — vendored into apps/${v.app}`)
    L.push('')
    L.push(`- Licence: ${v.spdx || v.declared || 'see text below'}`)
    L.push('')
    if (!v.text) {
      L.push(`> This package ships no licence file upstream. Its publisher declares: ${v.declared || 'NOTHING — must be resolved before this package is shipped'}.`)
      L.push('')
      continue
    }
    L.push(fence(v.text))
    L.push('')
  }

  L.push('---')
  L.push('')
  L.push('## Debian base system (installed into the Vulos image)')
  L.push('')
  if (!debian) {
    L.push('> This copy of the notices was generated on a host without a built Debian')
    L.push('> rootfs, so the package list is not included. The notices file shipped')
    L.push('> inside a Vulos image is regenerated by `build.sh` with the real package')
    L.push('> manifest of that image.')
    L.push('')
  } else {
    L.push('Every package below is installed into the image. Debian ships each package\'s')
    L.push('copyright and licence text with the package itself: inside a running Vulos')
    L.push('system the text for `<pkg>` is at `/usr/share/doc/<pkg>/copyright`, and the')
    L.push('common licence texts (GPL-2, GPL-3, LGPL-2.1, Apache-2.0, ...) are under')
    L.push('`/usr/share/common-licenses/`. Those files travel with the binaries, so they')
    L.push('are referenced here rather than duplicated.')
    L.push('')
    L.push('Many of these packages are licensed under the GPL or LGPL. See')
    L.push('`WRITTEN-OFFER.md` for how to obtain their complete corresponding source.')
    L.push('')
    L.push('| Package | Version | Source package | Source version | Arch |')
    L.push('|---|---|---|---|---|')
    for (const d of debian) L.push(`| ${d.pkg} | ${d.version} | ${d.srcPkg} | ${d.srcVersion} | ${d.arch} |`)
    L.push('')
  }

  L.push('---')
  L.push('')
  L.push('## Third-party names and logos')
  L.push('')
  L.push('Vulos displays the names of third-party applications it can install. Trademarks')
  L.push('are the property of their respective owners; their use here is nominative (to')
  L.push('identify the software) and does not imply endorsement.')
  L.push('')
  return L.join('\n') + '\n'
}

// ── main ──────────────────────────────────────────────────────────────────────

const data = { go: collectGo(), npm: collectNpm(), vendored: collectVendored(), debian: collectDebian() }
const text = render(data)

if (CHECK) {
  const current = existsSync(OUT) ? readFileSync(OUT, 'utf8') : ''
  if (current !== text) {
    console.error(`${OUT} is stale — run: node scripts/licensing/gen-notices.mjs`)
    process.exit(1)
  }
  console.log(`${OUT} is up to date.`)
  process.exit(0)
}

mkdirSync(dirname(OUT), { recursive: true })
writeFileSync(OUT, text)

const missingGo = data.go.filter((m) => m.missing).map((m) => m.name)
const missingNpm = data.npm.filter((p) => p.missing && !p.declared).map((p) => p.name)
console.log(
  `wrote ${OUT}: ${data.go.length} Go modules, ${data.npm.length} npm packages, ` +
    `${data.vendored.length} vendored libraries` +
    (data.debian ? `, ${data.debian.length} Debian packages` : ', no Debian manifest'),
)
if (missingGo.length) console.warn(`WARNING: no licence file found for Go modules: ${missingGo.join(', ')}`)
if (missingNpm.length) console.warn(`WARNING: no licence file and no declared licence for npm packages: ${missingNpm.join(', ')}`)
