/**
 * Architecture: what the BOX decided, narrowed off the wire.
 *
 * ── What this file used to be, and why none of it is left ───────────────────
 *
 * It held the decision: `archCompat(app.arch, boxArch)`, an alias table folding
 * `x86_64` onto `amd64`, a set of words meaning "runs anywhere", and
 * `requiredArches` to canonicalise the list for display. All of it was correct,
 * and all of it was the SECOND implementation of a policy the box already owns.
 *
 * It got there by patching a defect one layer too high. The original comparison
 * was `app.arch.includes(systemArch)` — a raw string match in which a Flathub
 * entry declaring `x86_64` never matched an `amd64` box, so most of the desktop
 * catalogue read as incompatible on every box, silently and consistently. The
 * fix was to fold the spellings here and source the box's architecture from the
 * server. That closed the instance. It left the DECISION in the browser, where
 * it could not see any of:
 *
 *   - whether a kernel binfmt_misc handler is registered for a foreign ELF;
 *   - whether box64 or qemu-user is on the box's PATH, which is not the same
 *     question — box64 is invoked by name and registers nothing in a container;
 *   - WHICH of those two would serve an app, which decides whether it gets
 *     graphics at all (measured: box64 bound the host's own aarch64 Mesa;
 *     qemu-user could not obtain a GL visual);
 *   - which architectures the INSTALLED flatpak will actually resolve refs for;
 *   - whether the app's delivery mechanism can be emulated at all, which is a
 *     property of the recipe and never reaches this side of the wire.
 *
 * A browser cannot observe one of those. So the answer is computed on the box —
 * services/appnet/arch.go, EvaluateArch — and arrives per entry as
 * `availability`. This file narrows it and nothing more. There is deliberately
 * no normalisation, no alias table and no comparison here: a second fold that
 * agrees with the server today is the shape that disagrees with it later.
 *
 * ── The one thing still fetched, and why it is not a decision ───────────────
 *
 * fetchBoxArch reads the box's own architecture for the HEADER LABEL, so the
 * hub can name the machine before the catalogue has loaded, or when the
 * catalogue is empty. It is a fact for display. Nothing switches on it.
 */

/**
 * The rungs of roadmap/DISTRO-SOURCED-APPS.md §1, as the box reports them.
 *
 * Rung 2 (native, distribution-sourced) has no state of its own: it produces a
 * native install and is reported as `native`. It is parked in any case.
 */
export type ArchState = 'native' | 'emulated' | 'other-instance' | 'unavailable'

const ARCH_STATES: ArchState[] = ['native', 'emulated', 'other-instance', 'unavailable']

/**
 * The box's answer for one app. Every string is shown to a person VERBATIM —
 * the hub composes no sentence of its own about architecture, so there is one
 * place the wording can be got wrong and one place a test can catch it.
 *
 * Mirrors services/appnet/arch.go's ArchAvailability.
 */
export interface ArchAvailability {
  /** Which rung. */
  state: ArchState
  /** Whether an install may be offered here. Emulated-and-opted-in is TRUE. */
  installable: boolean
  /** Whether that install would run translated. */
  requiresEmulation: boolean
  /** The badge that heads the detail panel. Empty for a native app — rung 1 is
   *  "Install button, no badge", and a badge on every app that simply works is
   *  noise on every card in the catalogue. */
  badge: string
  /** The same fact at card width: "Needs amd64", "Runs emulated", "On studio-box". */
  cardBadge: string
  /** The sentence. */
  detail: string
  /** The BOX's architecture, Debian spelling, as the box reports itself. */
  boxArch: string
  /** True when the entry declares no architecture at all — which means NOBODY
   *  CHECKED, not "runs anywhere". */
  undeclared: boolean
  /** The architectures the app needs, already folded to one spelling and
   *  de-duplicated by the box. Rendered, never compared. */
  needs: string[]
}

function isRecord(x: unknown): x is Record<string, unknown> {
  return typeof x === 'object' && x !== null
}

function str(x: unknown): string {
  return typeof x === 'string' ? x : ''
}

/**
 * Narrow `availability` off a registry entry.
 *
 * Returns null when the box did not answer — an older backend, a response shape
 * that changed, a field that failed to marshal. Null is NOT folded into either
 * verdict, and both foldings are worse than the gap:
 *
 *  - folding it to "unavailable" marks the whole catalogue unrunnable on any
 *    backend that does not send the field, which is every backend before this
 *    field existed;
 *  - folding it to "available" is the lie the user pays for — the install is
 *    offered, runs, and fails.
 *
 * The hub renders a null as "no compatibility claim": the app is offered, with
 * no badge and no sentence attached to it. That is the same honest behaviour
 * the old `'unknown'` compat value had, kept for the same reason.
 */
export function toArchAvailability(x: unknown): ArchAvailability | null {
  if (!isRecord(x)) return null
  const raw = x.availability
  if (!isRecord(raw)) return null
  const state = str(raw.state)
  // An unrecognised state is a null, not a default. A future rung this build
  // has never heard of must not be silently rendered as one of the four it
  // knows — least of all as the one that hides an install button.
  if (!ARCH_STATES.includes(state as ArchState)) return null
  return {
    state: state as ArchState,
    installable: raw.installable === true,
    requiresEmulation: raw.requires_emulation === true,
    badge: str(raw.badge),
    cardBadge: str(raw.card_badge),
    detail: str(raw.detail),
    boxArch: str(raw.box_arch),
    undeclared: raw.undeclared === true,
    needs: Array.isArray(raw.needs) ? raw.needs.filter((a): a is string => typeof a === 'string') : [],
  }
}

/**
 * The box's own architecture, for the header label.
 *
 *     GET /api/system/arch  ->  200 {"arch": "amd64", "supported": ["amd64"]}
 *
 * Debian spelling, because that is what registry.json is written in and what
 * every `availability.box_arch` carries. It is returned AS SENT: a fold here
 * would be the second implementation this file exists to have deleted, and the
 * value is never compared to anything — it is printed.
 */
export async function fetchBoxArch(): Promise<string | null> {
  try {
    const res = await fetch('/api/system/arch')
    if (res.ok) {
      const data: unknown = await res.json()
      if (isRecord(data) && typeof data.arch === 'string') {
        const a = data.arch.trim()
        if (a) return a
      }
    }
  } catch {
    // No endpoint, no network: the header simply does not name the box.
  }
  return null
}
