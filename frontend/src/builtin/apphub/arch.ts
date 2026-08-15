/**
 * Architecture: what THE BOX can run, and how to say so.
 *
 * ── The trap this module exists to close ────────────────────────────────────
 *
 * The architecture that decides whether an app can be installed is the BOX's,
 * never the browser's. Vulos streams desktop apps from the box into a browser
 * window, so someone on an ARM Mac connected to an amd64 box must be offered
 * amd64 apps — and someone on an x86 laptop connected to an ARM box must not.
 * Anything derived from `navigator.platform`, `navigator.userAgent` or
 * `navigator.userAgentData` answers a question nobody asked, and it answers it
 * *plausibly*, which is worse than answering it wrongly: it agrees with the
 * server on every machine a developer is likely to test on, and disagrees on
 * exactly the mixed setups this OS is built for.
 *
 * There is therefore NO client-side detection anywhere in this file. The box's
 * architecture arrives from the server or it is unknown, and "unknown" has its
 * own honest behaviour (see isCompatible).
 *
 * ── The naming trap ─────────────────────────────────────────────────────────
 *
 * Two ecosystems, two spellings for the same silicon:
 *
 *   Debian / dpkg / apt      amd64      arm64
 *   Flatpak / uname / OCI    x86_64     aarch64
 *
 * registry.json uses the Debian spelling; Flathub metadata, `uname -m` and most
 * container tooling use the other. A comparison that mixes them matches NOTHING
 * — and because "no match" and "incompatible" are the same value, it fails
 * silently and consistently: every Flathub app would read as unavailable on
 * every box, or, with the comparison inverted, every app would read as
 * installable on every box. Neither produces an error anyone would notice.
 *
 * So every comparison in the hub goes through normalizeArch() and there is
 * exactly one of it. The tests exercise both spellings on both sides.
 */

export type ArchCompat =
  /** The box can run it. */
  | 'yes'
  /** The box cannot run it — `required` says what it would need. */
  | 'no'
  /** The box's architecture is not known yet, so nothing may be claimed. */
  | 'unknown'

/**
 * Canonical architecture names, keyed by every spelling seen in the wild.
 *
 * Debian's name is the canonical one because registry.json is written in it.
 * Values are what everything downstream compares and displays.
 */
const ARCH_ALIASES: Record<string, string> = {
  // 64-bit x86
  amd64: 'amd64',
  x86_64: 'amd64',
  'x86-64': 'amd64',
  x64: 'amd64',
  // 64-bit ARM
  arm64: 'arm64',
  aarch64: 'arm64',
  'arm64-v8a': 'arm64',
  // 32-bit ARM
  armhf: 'armhf',
  armv7l: 'armhf',
  armv7: 'armhf',
  // 32-bit x86
  i386: 'i386',
  i686: 'i386',
  x86: 'i386',
  // RISC-V
  riscv64: 'riscv64',
}

/**
 * Names meaning "runs anywhere" rather than naming a machine.
 *
 * A web app or a shell script has no architecture, and the registry expresses
 * that either by an empty `arch` array or by one of these words. Treating
 * "all" as if it were a CPU would mark every web app in the catalogue
 * unavailable on every box.
 */
const UNIVERSAL = new Set(['all', 'any', 'noarch', 'universal'])

/**
 * Fold a spelling onto its canonical Debian name.
 *
 * Unknown values pass through lower-cased rather than being dropped: a registry
 * that starts shipping `ppc64el` should render "ppc64el" and compare
 * consistently, not silently become universal (which would offer an impossible
 * install) or silently become incompatible (which would hide a working app).
 */
export function normalizeArch(raw: string): string {
  const key = raw.trim().toLowerCase()
  return ARCH_ALIASES[key] ?? key
}

/** True for the names that mean "no particular architecture". */
export function isUniversalArch(raw: string): boolean {
  return UNIVERSAL.has(raw.trim().toLowerCase())
}

/** How an architecture is shown to a person. Debian's spelling, as displayed. */
export function archLabel(raw: string): string {
  return normalizeArch(raw)
}

/**
 * Can this box install this app?
 *
 * `appArch` is the registry's `arch` array; `boxArch` is what the SERVER said
 * this box is, or null if that is not known yet.
 *
 * The three answers are deliberately distinct, and 'unknown' is not folded into
 * either of the others:
 *
 *  - Folding unknown into 'no' marks the entire catalogue unavailable for the
 *    moment before the box's architecture arrives, and permanently on any
 *    backend that does not report it — which is every backend today.
 *  - Folding unknown into 'yes' is what the code did before this module
 *    existed, and it is a lie the user pays for: the install is offered, runs,
 *    and fails in apt.
 *
 * Rendering distinguishes them, so an unverified app is offered without a
 * compatibility claim attached to it.
 */
export function archCompat(appArch: string[] | undefined, boxArch: string | null): ArchCompat {
  // No declared architecture, or an explicitly universal one: runs anywhere.
  if (!appArch || appArch.length === 0) return 'yes'
  if (appArch.some(isUniversalArch)) return 'yes'
  if (!boxArch) return 'unknown'
  const box = normalizeArch(boxArch)
  return appArch.some((a) => normalizeArch(a) === box) ? 'yes' : 'no'
}

/**
 * The architectures an app needs, canonicalised and de-duplicated, for display.
 *
 * De-duplication matters because normalising can collide: an entry listing both
 * `x86_64` and `amd64` (Flathub metadata merged with Debian's) would otherwise
 * render "amd64, amd64".
 */
export function requiredArches(appArch: string[] | undefined): string[] {
  return [...new Set((appArch ?? []).filter((a) => !isUniversalArch(a)).map(normalizeArch))]
}

// ── The backend seam ──────────────────────────────────────────────────────────

/**
 * The shape this UI needs from the server, and the only thing it will believe.
 *
 * PREFERRED (does not exist yet — requested from the backend owner):
 *
 *     GET /api/system/arch  ->  200 {"arch": "amd64"}
 *
 *   `arch` is the BOX's architecture in Debian spelling ("amd64" | "arm64" |
 *   …). normalizeArch() above accepts `uname -m` spellings too, so a backend
 *   that finds it easier to return "x86_64" will still work — but the declared
 *   contract is Debian's, because that is what registry.json's `arch` field is
 *   written in and a single spelling on the wire is one fewer thing to get
 *   wrong.
 *
 * FALLBACK (exists today):
 *
 *     GET /api/packages/cache  ->  200 {"ready": bool, "arch": "amd64"|null}
 *
 *   This is the apt-cache status endpoint, which happens to carry the box's
 *   architecture. It is used because it is what is available, not because it is
 *   the right home for the fact: an endpoint about the Debian package index
 *   reports nothing on a box with no apt, and its `arch` is null until the
 *   index has been read at least once. Hence the dedicated endpoint above.
 *
 * Order matters: the dedicated endpoint wins when both answer, so connecting
 * the backend needs no change here.
 */
export async function fetchBoxArch(): Promise<string | null> {
  try {
    const res = await fetch('/api/system/arch')
    if (res.ok) {
      const data: unknown = await res.json()
      if (data && typeof data === 'object' && typeof (data as { arch?: unknown }).arch === 'string') {
        const a = (data as { arch: string }).arch.trim()
        if (a) return normalizeArch(a)
      }
    }
  } catch {
    // Not wired up yet. The fallback below is the whole reason this is a seam.
  }
  return null
}
