#!/usr/bin/env node
/**
 * The pack validator a developer runs before shipping.
 *
 *     node src/desktop/cli/validate-pack.mjs my-layout.pack.json
 *
 * It calls the SAME validatePack() the shell installs through — imported from
 * src/desktop/validate.ts, not reimplemented here. A second, drifting copy of a
 * validator is this codebase's best-documented defect class (mock-vs-core
 * divergence), and a validator that disagrees with the thing it gates is worse
 * than no validator: it tells a developer their pack is fine and the box then
 * refuses it, or the reverse.
 *
 * Exits 0 when every file validates, 1 when any fails, 2 on usage error.
 */
import { readFileSync } from 'node:fs'
import { register } from 'node:module'

register('./ts-resolve.mjs', import.meta.url)
const { validatePack } = await import('../validate.ts')

const files = process.argv.slice(2)
if (files.length === 0) {
  console.error('usage: node src/desktop/cli/validate-pack.mjs <pack.json> [...]')
  console.error('  Validates a Vulos desktop layout pack against the published format.')
  console.error('  Schema: src/desktop/schema.json   Docs: roadmap/CUSTOMIZATION.md')
  process.exit(2)
}

let failed = 0
for (const file of files) {
  let parsed
  try {
    parsed = JSON.parse(readFileSync(file, 'utf8'))
  } catch (err) {
    console.error(`FAIL ${file}\n    not readable JSON: ${err.message}`)
    failed++
    continue
  }
  const result = validatePack(parsed, file)
  if (result.ok) {
    const d = result.value.layout.dock
    const tokens = Object.keys(result.value.layout.tokens)
    console.log(`OK   ${file}  ->  "${result.value.name}" (${result.value.id})`)
    console.log(`       desktop: ${d.desktop.style} dock on the ${d.desktop.edge} edge, ${d.desktop.items.length} default items`)
    console.log(`       mobile:  ${d.mobile.style} dock on the ${d.mobile.edge} edge, ${d.mobile.items.length} default items, drawer on`)
    console.log(`       window controls: ${result.value.layout.windowControls}`)
    console.log(`       tokens: ${tokens.length ? tokens.join(', ') : 'none'}`)
  } else {
    console.error(`FAIL ${file}`)
    for (const e of result.errors) console.error(`       ${e}`)
    failed++
  }
}

process.exit(failed === 0 ? 0 : 1)
