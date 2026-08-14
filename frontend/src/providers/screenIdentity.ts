// screenIdentity.ts — which physical screen is this browser instance on?
//
// # Why a URL parameter and not a query to the box
//
// Vulos renders its desktop in a browser, and a browser window belongs to ONE
// display. So a machine with two monitors runs TWO kiosk browsers — one per
// connected output — pointed at the same local server (roadmap/SCREENS.md
// records why that shape was chosen over one enormous viewport spanning both).
//
// Both instances then load byte-identical HTML from the same origin. Nothing in
// the page can tell them apart: `screen.width` is the same on two identical
// monitors, and there is no web API that reports "you are the window on
// HDMI-A-1". The only party that knows is the thing that launched them — the
// kiosk launcher in build.sh, which enumerated the outputs in the first place.
// It therefore states the answer in each browser's URL, and this module reads
// it back.
//
// # It is presentation only
//
// A URL parameter is attacker-controlled by definition: anyone can type
// ?screen=whatever. Nothing here grants access to anything, nothing is
// persisted under it, and the value is rendered as text, never as markup or a
// selector. It is still validated to a conservative charset so a hostile value
// cannot smuggle layout-breaking or lookalike content into the menu bar.
//
// # Absent means "one screen, exactly as before"
//
// A hand-opened tab, a phone on the LAN, the dev server — none of these carry
// the parameter, and all of them must behave precisely as they did before
// multi-screen existed. Every function here returns null in that case, and
// every caller treats null as single-screen.

/** Where this browser instance sits in the machine's set of screens. */
export interface ScreenIdentity {
  /** The compositor's output name, e.g. "HDMI-A-1" or "Virtual-1". */
  name: string
  /** 1-based position in the launcher's ordering of connected outputs. */
  index: number
  /** How many screens the kiosk launched browsers for. Always >= index. */
  total: number
}

/**
 * Output names come from DRM connectors, so they are short and boring:
 * HDMI-A-1, DP-2, eDP-1, Eleventh-Virtual-1. Anything else is not a name this
 * launcher produced, and is refused rather than rendered.
 */
const NAME_RE = /^[A-Za-z0-9][A-Za-z0-9_-]{0,31}$/

/**
 * parseScreenIdentity reads the identity out of a query string.
 *
 * Takes the string rather than reading `location` so it is testable without a
 * browser and so a caller can evaluate a URL it has in hand.
 *
 * Returns null — meaning "single screen, behave as before" — for anything that
 * is not a complete, self-consistent identity. Partial input is REFUSED rather
 * than half-honoured: a chip reading "Screen 3 of 1" is worse than no chip.
 */
export function parseScreenIdentity(search: string): ScreenIdentity | null {
  let params: URLSearchParams
  try {
    params = new URLSearchParams(search)
  } catch {
    return null
  }

  const name = params.get('screen')
  if (!name || !NAME_RE.test(name)) return null

  const total = toPositiveInt(params.get('screens'))
  const index = toPositiveInt(params.get('screenIndex'))
  if (total === null || index === null) return null
  // A screen cannot be the 3rd of 2. Refusing keeps every consumer free of
  // "what do I show when the numbers disagree" branches.
  if (index > total) return null

  return { name, index, total }
}

/**
 * toPositiveInt accepts only a plain decimal integer >= 1.
 *
 * Number() alone would accept "1e3", " 2 ", "0x2" and "Infinity", each of which
 * would then be rendered into the menu bar. parseInt alone would accept "2abc".
 * The explicit pattern is the cheapest way to mean what is meant.
 */
function toPositiveInt(raw: string | null): number | null {
  if (raw === null || !/^[0-9]{1,3}$/.test(raw)) return null
  const n = Number(raw)
  return n >= 1 ? n : null
}

/**
 * readScreenIdentity reads the identity of the CURRENT document.
 *
 * Guarded because the shell is also rendered outside a browser (tests, and any
 * future server-side render), where `location` does not exist. Missing means
 * single-screen, never an exception.
 */
export function readScreenIdentity(): ScreenIdentity | null {
  const loc = (globalThis as { location?: { search?: string } }).location
  if (!loc || typeof loc.search !== 'string') return null
  return parseScreenIdentity(loc.search)
}

/**
 * isMultiScreen answers the one question most callers actually have: is this
 * browser one of several showing the same box?
 *
 * A single-screen kiosk DOES carry the parameter (the launcher sets it either
 * way, so the two paths differ in as few places as possible), and it must be
 * treated as an ordinary single-screen session — no chip, no peer explanations.
 */
export function isMultiScreen(id: ScreenIdentity | null): boolean {
  return id !== null && id.total > 1
}

/**
 * screenWindowTitle builds the document title for a kiosk browser instance.
 *
 * This exists for the COMPOSITOR, not for a human. A multi-output kiosk runs
 * one browser per screen, and labwc places a window on a given output with:
 *
 *   <windowRule title="Vulos — HDMI-A-1" matchOnce="yes">
 *     <action name="MoveToOutput" output="HDMI-A-1" />
 *   </windowRule>
 *
 * A rule can only name a window it can tell apart, and every instance of this
 * shell is otherwise identical: same origin, same app, same title. So the
 * window announces which screen it believes it is on, and the compositor uses
 * that to put it there.
 *
 * The name embedded here is the DRM connector name that vulos-kiosk read from
 * /sys/class/drm and passed in as `screen=`, and it is the same string
 * MoveToOutput's `output` attribute wants. One name from one source, so the
 * two halves agree by construction rather than by someone keeping them in sync.
 *
 * Returns the base title unchanged when there is no multi-screen identity —
 * an ordinary tab must not have a connector name in its title bar.
 */
export function screenWindowTitle(id: ScreenIdentity | null, base: string): string {
  if (!isMultiScreen(id) || !id) return base
  return `${base} — ${id.name}`
}

/**
 * applyScreenWindowTitle stamps the CURRENT document's title with the connector
 * name this browser instance was launched for. It is the single writer of
 * document.title in this shell.
 *
 * # Why this is not a UI concern
 *
 * The title is a COMPOSITOR CONTRACT. labwc decides which physical output this
 * window belongs on by matching a windowRule against the title, so the title
 * has to hold from the moment the document exists — before setup, during setup,
 * at the login screen, and after login — regardless of which route is mounted.
 *
 * It used to be a side-effect of the top bar's screen chip, which mounts only
 * inside DesktopCanvas, i.e. only after setup AND after login. The first
 * two-display QEMU boot (roadmap/SCREENS-QEMU.md) landed on the setup wizard,
 * so no top bar ever mounted, so no title was ever set, so no rule matched and
 * the second monitor stayed black for the entire pre-login life of the session.
 * A window's identity to the compositor cannot depend on how far through login
 * the user has got.
 *
 * # Does nothing without a multi-screen identity
 *
 * An ordinary tab, a phone on the LAN, the dev server and a one-monitor kiosk
 * all get `null` from readScreenIdentity() (or a total of 1), and this leaves
 * document.title completely alone in that case — no connector name, and no
 * rewriting of the title the HTML shipped with. Only a browser the kiosk
 * launched for one of several outputs is touched.
 *
 * # Idempotent
 *
 * The base is taken as everything before the first em-dash separator, which is
 * how screenWindowTitle joins it, so applying twice yields the same string. It
 * returns the title it set, or null when it deliberately set nothing, so
 * "did nothing" is observable rather than assumed.
 */
export function applyScreenWindowTitle(): string | null {
  // Guarded exactly like readScreenIdentity: this module is also loaded where
  // there is no document (unit tests, any future server-side render), and a
  // missing document means single-screen, never an exception.
  const doc = (globalThis as { document?: { title?: unknown } }).document
  if (!doc || typeof doc.title !== 'string') return null

  const id = readScreenIdentity()
  if (!isMultiScreen(id)) return null

  const base = doc.title.split(' — ')[0] || 'Vulos'
  const next = screenWindowTitle(id, base)
  doc.title = next
  return next
}
