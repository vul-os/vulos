#!/usr/bin/env node
/**
 * gen-wire-types.mjs — generate src/types/wire.ts from a small, hand-picked
 * set of Go wire types, by shelling out to scripts/wiregen (a Go program that
 * parses the real declarations with go/parser + go/ast — see the doc comment
 * at the top of scripts/wiregen/main.go for why AST over go/types or regex).
 *
 * WHY: ~145k lines of Go define every API response shape statically, and
 * that shape is erased the moment a payload crosses into the frontend.
 * Divergent duplicate definitions (a hand-written TS interface that quietly
 * stops matching its Go struct) are this project's dominant defect class.
 * Generating the frontend's view of the wire FROM the Go source, rather than
 * hand-writing and hoping it stays in sync, is the fix — same idea as
 * scripts/licensing/gen-notices.mjs generating THIRD_PARTY_NOTICES.md from
 * the actual dependency graph instead of a hand-maintained list.
 *
 * This does not attempt to cover the whole Go surface — see the `targets`
 * slice in scripts/wiregen/main.go for exactly what is (and, in its header
 * comment, is not) covered.
 *
 * Usage:
 *   node scripts/gen-wire-types.mjs          # write src/types/wire.ts
 *   node scripts/gen-wire-types.mjs --check  # exit 1 if the committed file is stale
 */
import { execFileSync } from 'node:child_process'
import { existsSync, mkdirSync, readFileSync, writeFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'

const REPO = join(dirname(fileURLToPath(import.meta.url)), '..')
// The web tier lives in frontend/.
const OUT = join(REPO, 'frontend', 'src', 'types', 'wire.ts')
const CHECK = process.argv.includes('--check')

function generate() {
  // GO111MODULE=off: scripts/wiregen is a standalone, stdlib-only Go program
  // (see its doc comment) with no go.mod of its own, and it must not be
  // pulled into backend's module (a different Go module the frontend does
  // not own the boundaries of). Running in GOPATH mode means `go run` parses
  // exactly the files wiregen asks for and nothing about module resolution.
  return execFileSync('go', ['run', './scripts/wiregen'], {
    cwd: REPO,
    encoding: 'utf8',
    env: { ...process.env, GO111MODULE: 'off' },
    maxBuffer: 16 << 20,
  })
}

let text
try {
  text = generate()
} catch (err) {
  console.error('gen-wire-types: generation failed:\n' + (err.stderr || err.message))
  process.exit(1)
}

if (CHECK) {
  const current = existsSync(OUT) ? readFileSync(OUT, 'utf8') : ''
  if (current !== text) {
    console.error(`${OUT} is stale — run: node scripts/gen-wire-types.mjs`)
    process.exit(1)
  }
  console.log(`${OUT} is up to date.`)
  process.exit(0)
}

mkdirSync(dirname(OUT), { recursive: true })
writeFileSync(OUT, text)
console.log(`wrote ${OUT}`)
