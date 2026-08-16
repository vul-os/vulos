#!/usr/bin/env node
/**
 * sync-api-helper.mjs — push apps/_shared/vulos-api.js into every bundled app
 * that uses it, as a verbatim inline <script> block.
 *
 * Run:  node frontend/apps/_shared/sync-api-helper.mjs
 *       node frontend/apps/_shared/sync-api-helper.mjs --check   (exit 1 on drift)
 *
 * WHY A GENERATOR AND NOT A <script src>
 * --------------------------------------
 * See the header of vulos-api.js: the fifteen bundled apps each ship their own
 * server.py with its own ad-hoc static route table, so a linked shared script
 * would 404 in every app whose server was not also edited — turning a wrong URL
 * into a dead app. The copy is mechanical instead, and pinned by
 * backend/internal/docsref/appapibase_test.go so it cannot drift.
 *
 * An app opts in simply by CALLING the helper (`vulosApi.appUrl(`/
 * `vulosApi.appFetch(`). This script then guarantees the definition is present.
 * A twelfth app that opts in and forgets to run this fails the Go guard.
 */
import { readFileSync, writeFileSync, readdirSync, statSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'

const SHARED = dirname(fileURLToPath(import.meta.url))
const APPS = dirname(SHARED)

const OPEN = '<script data-vulos-shared="vulos-api.js">'
const CLOSE = '</script>'
const BLOCK_RE = /[ \t]*<script data-vulos-shared="vulos-api\.js">[\s\S]*?<\/script>\n?/

const helper = readFileSync(join(SHARED, 'vulos-api.js'), 'utf8')
if (!helper.includes('function appUrl')) {
  console.error('vulos-api.js does not define appUrl; refusing to sync a helper that is not the helper')
  process.exit(2)
}
const block = `${OPEN}\n${helper}${CLOSE}\n`

const check = process.argv.includes('--check')
let changed = 0
let synced = 0

for (const name of readdirSync(APPS).sort()) {
  const dir = join(APPS, name)
  if (name === '_shared' || !statSync(dir).isDirectory()) continue
  const file = join(dir, 'index.html')
  let src
  try {
    src = readFileSync(file, 'utf8')
  } catch {
    continue
  }

  const uses = /vulosApi\.(appUrl|appFetch|mountBase)\s*\(/.test(src)
  const has = BLOCK_RE.test(src)
  if (!uses && !has) continue

  let next
  if (has) {
    next = src.replace(BLOCK_RE, block)
  } else {
    const head = src.indexOf('</head>')
    if (head < 0) {
      console.error(`${name}/index.html has no </head>; cannot place the helper`)
      process.exit(2)
    }
    next = src.slice(0, head) + block + src.slice(head)
  }

  synced++
  if (next !== src) {
    changed++
    if (check) {
      console.error(`drift: ${name}/index.html`)
    } else {
      writeFileSync(file, next)
      console.log(`synced: ${name}/index.html`)
    }
  }
}

if (synced === 0) {
  console.error('no app opted into vulos-api.js; the sync would have silently done nothing')
  process.exit(2)
}
console.log(`${synced} app(s) carry vulos-api.js; ${changed} updated`)
if (check && changed > 0) process.exit(1)
