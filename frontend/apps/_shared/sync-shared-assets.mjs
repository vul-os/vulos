#!/usr/bin/env node
/**
 * sync-shared-assets.mjs — push everything in apps/_shared/ that the bundled
 * apps depend on INTO those apps, verbatim, as inline blocks.
 *
 * Run:  node frontend/apps/_shared/sync-shared-assets.mjs
 *       node frontend/apps/_shared/sync-shared-assets.mjs --check   (exit 1 on drift)
 *
 * WHY INLINE AND NOT <link> / <script src>
 * ----------------------------------------
 * The fifteen bundled apps each ship their own hand-rolled server.py with its
 * own ad-hoc static route table. Only four of them (browser, camera, phone,
 * system-info) ever special-cased `/vulos-tokens.css` so the request could
 * escape their APP_DIR; the other eleven would 404 a link or a script src,
 * turning a shared asset into a dead app — or, for CSS, an unstyled first
 * paint. An inline copy has no such failure mode and no round trip. The price
 * is duplication, and duplication is paid for by
 * backend/internal/docsref/appthemetokens_test.go, which pins every copy
 * byte-for-byte against the one source here.
 *
 * ORDER MATTERS. The blocks are emitted in the order declared below, all inside
 * <head>: the theme resolver stamps <html data-theme> first, then the tokens
 * that read that attribute, then the API resolver. Reversing the first two
 * would give a frame one paint in the wrong theme.
 */
import { readFileSync, writeFileSync, readdirSync, statSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'

const SHARED = dirname(fileURLToPath(import.meta.url))
const APPS = dirname(SHARED)

// site-template is not an OS app: it is the scaffold a user's OWN site is built
// from, with its own stylesheet and no OS chrome. Giving it the OS palette
// would be giving a user's website our design language by default.
const NOT_AN_OS_APP = new Set(['site-template'])

/**
 * `scope: 'all'`   — every bundled app gets it.
 * `scope: 'onUse'` — only apps whose source calls it, detected by `uses`.
 * `must`           — a string the source file has to contain, so a gutted or
 *                    truncated asset is never syndicated to fifteen apps.
 */
const ASSETS = [
  { file: 'vulos-theme.js', tag: 'script', scope: 'all', must: 'function applyTheme' },
  { file: 'vulos-tokens.css', tag: 'style', scope: 'all', must: '[data-theme="light"]' },
  {
    file: 'vulos-api.js',
    tag: 'script',
    scope: 'onUse',
    must: 'function appUrl',
    uses: /vulosApi\.(appUrl|appFetch|mountBase)\s*\(/,
  },
]

const blockRe = (file, tag) =>
  new RegExp(`[ \\t]*<${tag} data-vulos-shared="${file.replace('.', '\\.')}">[\\s\\S]*?</${tag}>\\n?`)

const sources = new Map()
for (const a of ASSETS) {
  const src = readFileSync(join(SHARED, a.file), 'utf8')
  if (!src.includes(a.must)) {
    console.error(`${a.file} no longer contains ${JSON.stringify(a.must)}; refusing to syndicate it`)
    process.exit(2)
  }
  sources.set(a.file, src)
}

const check = process.argv.includes('--check')
let changed = 0
const placed = new Map(ASSETS.map((a) => [a.file, 0]))

for (const name of readdirSync(APPS).sort()) {
  const dir = join(APPS, name)
  if (name === '_shared' || NOT_AN_OS_APP.has(name) || !statSync(dir).isDirectory()) continue
  const file = join(dir, 'index.html')
  let src
  try {
    src = readFileSync(file, 'utf8')
  } catch {
    continue
  }
  const original = src

  // Strip every existing block first, so the emitted order below is the order
  // that ends up in the file even for an app that acquired them piecemeal.
  const present = new Map()
  for (const a of ASSETS) {
    const re = blockRe(a.file, a.tag)
    present.set(a.file, re.test(src))
    src = src.replace(re, '')
  }

  const wanted = ASSETS.filter((a) => {
    if (a.scope === 'all') return true
    // For an opt-in asset the source must be examined WITHOUT its own block,
    // which the strip above has already removed — otherwise the asset's own
    // prose would count as a use of itself.
    return a.uses.test(src)
  })

  // A <link rel="stylesheet" href="vulos-tokens.css"> is the old mechanism; the
  // inline block replaces it, and leaving it behind would 404 in eleven apps.
  src = src.replace(/[ \t]*<link rel="stylesheet" href="vulos-tokens\.css">\n?/g, '')

  let blocks = ''
  for (const a of wanted) {
    placed.set(a.file, placed.get(a.file) + 1)
    blocks += `<${a.tag} data-vulos-shared="${a.file}">\n${sources.get(a.file)}</${a.tag}>\n`
  }

  // PLACEMENT: immediately after </title>, which in every bundled app precedes
  // that app's own <style>. That ordering is deliberate and load-bearing in two
  // directions. The shared tokens must come BEFORE the app's own :root, because
  // both have identical specificity and the later one wins — putting the shared
  // block last would silently repaint fifteen hand-designed apps in one commit.
  // Placed first, it only supplies names the app does not define itself, and an
  // app adopts the palette when its local definitions are deliberately removed.
  // The theme resolver must also run before any of that paints.
  const anchor = src.indexOf('</title>')
  const at = anchor >= 0 ? anchor + '</title>\n'.length : src.indexOf('</head>')
  if (at < 0) {
    console.error(`${name}/index.html has neither </title> nor </head>; cannot place shared assets`)
    process.exit(2)
  }
  src = src.slice(0, at) + blocks + src.slice(at)

  if (src !== original) {
    changed++
    if (check) console.error(`drift: ${name}/index.html`)
    else {
      writeFileSync(file, src)
      const which = wanted.map((a) => a.file).join(', ')
      const was = ASSETS.filter((a) => present.get(a.file)).length
      console.log(`synced: ${name}/index.html (${which})${was ? '' : ' [new]'}`)
    }
  }
}

for (const a of ASSETS) {
  const n = placed.get(a.file)
  console.log(`${a.file}: ${n} app(s)`)
  if (n === 0) {
    console.error(`${a.file} was placed in NO app; the sync would have silently done nothing`)
    process.exit(2)
  }
}
console.log(`${changed} app(s) updated`)
if (check && changed > 0) process.exit(1)
