// sandboxDoc.ts — building the document a sandboxed widget runs inside.
//
// Split out of SandboxFrame.tsx so the document construction is a pure function
// of (source, origin, theme) and can be asserted directly: the tests that matter
// here — that the bridge client is injected, that a `</script>` inside a widget's
// own source cannot break out of the injected script block, that the palette is
// resolved rather than linked — are string assertions, not DOM ones.
//
// THEME: an opaque-origin frame cannot inherit the shell's stylesheet, so the
// host RESOLVES the design tokens against the live document and injects their
// computed values. The widget therefore gets correct colours in both themes
// while receiving sixteen colour strings rather than a stylesheet reference or a
// DOM handle — and it cannot hardcode a hex that fails the contrast gate in one
// theme, because the tokens are the only palette it is given.

import { bridgeClientScript } from './bridge'

// The palette handed to a sandboxed widget. Deliberately a SUBSET: enough to
// build a native-looking tile, small enough that the injected block stays tiny.
export const EXPORTED_TOKENS = [
  '--text-primary', '--text-secondary', '--text-tertiary',
  '--bg-elevated', '--bg-hover', '--bg-base',
  '--border-strong', '--border-emphasis',
  '--accent', '--accent-soft',
  '--status-success', '--status-warning', '--status-danger',
  '--font-sans', '--font-mono',
  '--radius-md', '--radius-full',
] as const

// Resolving sixteen custom properties means sixteen getComputedStyle reads, and
// every sandboxed widget on the rail would repeat them. The result depends only
// on the active theme, so it is cached under the theme key — which is also why
// `themeKey` is a parameter rather than something read inside: it makes the
// dependency explicit to the caller's memo instead of invisible.
const tokenCache = new Map<string, string>()

export function resolveTokens(themeKey: string): string {
  if (typeof document === 'undefined') return ''
  const hit = tokenCache.get(themeKey)
  if (hit !== undefined) return hit
  const cs = getComputedStyle(document.documentElement)
  const lines: string[] = []
  for (const t of EXPORTED_TOKENS) {
    const v = cs.getPropertyValue(t).trim()
    if (v) lines.push(`${t}: ${v};`)
  }
  const css = `:root{${lines.join('')}}`
  tokenCache.set(themeKey, css)
  return css
}

/** Test seam: the cache would otherwise survive a theme palette change in a test. */
export function clearTokenCache(): void {
  tokenCache.clear()
}

/**
 * Neutralise a `</script>` sequence inside injected JS.
 *
 * Without this, a widget whose SOURCE contains the literal `</script>` inside a
 * string would terminate the host's injected bridge block early, and the rest of
 * the bridge client would be parsed as markup — breaking the channel and
 * splicing attacker-chosen text into the document. It is applied to the injected
 * script, which is the only part the host writes into a `<script>` element.
 */
export function safeScript(js: string): string {
  // `$1` preserves the original casing. Rewriting `</SCRIPT` to lowercase would
  // also work as an escape, but it silently edits the widget author's string
  // literal — and a function whose job is "make this safe" must not also be
  // quietly changing the data.
  return js.replace(/<\/(script)/gi, '<\\/$1')
}

export function buildSandboxDocument(source: string, shellOrigin: string, tokenCss: string): string {
  return `<!doctype html><html><head><meta charset="utf-8">
<meta name="color-scheme" content="light dark">
<style>
${tokenCss}
html,body{margin:0;padding:0;height:100%;background:transparent;color:var(--text-primary);
  font-family:var(--font-sans);font-size:12px;-webkit-font-smoothing:antialiased;overflow:hidden}
*{box-sizing:border-box}
</style>
<script>${safeScript(bridgeClientScript(shellOrigin))}</script>
</head><body>${source}</body></html>`
}
