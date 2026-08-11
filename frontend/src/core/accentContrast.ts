/**
 * Legibility-corrected variants of a user-chosen accent colour.
 *
 * # Why this is code and not a palette value
 *
 * The accent is picked by the USER (ThemeProvider persists it; DEFAULT_ACCENT is
 * blue-500). So no value in a stylesheet can guarantee that accent-coloured TEXT
 * is readable — the colour is not ours to choose. What IS ours is the guarantee:
 * whatever they pick, the text drawn in it should meet WCAG AA against the
 * surface behind it.
 *
 * Two derived colours, because one value cannot do both jobs:
 *
 *   --accent-text   the accent moved just far enough to clear 4.5:1 against the
 *                   theme's least-forgiving surface. For headings, links, and
 *                   any label drawn IN the accent.
 *   --accent-solid  the accent moved until WHITE clears 4.5:1 on top of it. For
 *                   filled buttons. This moves in the OPPOSITE direction on a
 *                   dark theme, which is exactly why the two cannot be one token:
 *                   making accent text more legible on a dark page means
 *                   lightening it, and that makes white text on it worse.
 *
 * Measured defects this exists to close, all with the default blue on the light
 * theme: the desktop watermark at 2.80:1, the Settings section heading at
 * 3.46:1, and its "Save changes" button at 3.68:1 white-on-accent.
 */

export interface Rgb { r: number; g: number; b: number }

export function parseColor(input: string): Rgb | null {
  const s = input.trim()
  const hex = /^#?([0-9a-f]{3}|[0-9a-f]{6})$/i.exec(s)
  if (hex) {
    const h = hex[1]
    const full = h.length === 3 ? h.split('').map((c) => c + c).join('') : h
    return {
      r: parseInt(full.slice(0, 2), 16),
      g: parseInt(full.slice(2, 4), 16),
      b: parseInt(full.slice(4, 6), 16),
    }
  }
  const rgb = /^rgba?\(\s*([\d.]+)[,\s]+([\d.]+)[,\s]+([\d.]+)/i.exec(s)
  if (rgb) return { r: +rgb[1], g: +rgb[2], b: +rgb[3] }
  return null
}

export function toHex({ r, g, b }: Rgb): string {
  const c = (v: number) => Math.max(0, Math.min(255, Math.round(v))).toString(16).padStart(2, '0')
  return `#${c(r)}${c(g)}${c(b)}`
}

/** WCAG relative luminance. */
export function luminance({ r, g, b }: Rgb): number {
  const f = (v: number) => {
    const x = v / 255
    return x <= 0.03928 ? x / 12.92 : Math.pow((x + 0.055) / 1.055, 2.4)
  }
  return 0.2126 * f(r) + 0.7152 * f(g) + 0.0722 * f(b)
}

/** WCAG contrast ratio, always >= 1. */
export function contrast(a: Rgb, b: Rgb): number {
  const l1 = luminance(a)
  const l2 = luminance(b)
  return (Math.max(l1, l2) + 0.05) / (Math.min(l1, l2) + 0.05)
}

/* ── HSL, so adjustments keep the user's hue ─────────────────────────────── */

function toHsl({ r, g, b }: Rgb): [number, number, number] {
  const R = r / 255, G = g / 255, B = b / 255
  const max = Math.max(R, G, B), min = Math.min(R, G, B)
  const l = (max + min) / 2
  if (max === min) return [0, 0, l]
  const d = max - min
  const s = l > 0.5 ? d / (2 - max - min) : d / (max + min)
  let h: number
  if (max === R) h = ((G - B) / d + (G < B ? 6 : 0)) / 6
  else if (max === G) h = ((B - R) / d + 2) / 6
  else h = ((R - G) / d + 4) / 6
  return [h, s, l]
}

function fromHsl(h: number, s: number, l: number): Rgb {
  if (s === 0) return { r: l * 255, g: l * 255, b: l * 255 }
  const q = l < 0.5 ? l * (1 + s) : l + s - l * s
  const p = 2 * l - q
  const hue = (t: number) => {
    if (t < 0) t += 1
    if (t > 1) t -= 1
    if (t < 1 / 6) return p + (q - p) * 6 * t
    if (t < 1 / 2) return q
    if (t < 2 / 3) return p + (q - p) * (2 / 3 - t) * 6
    return p
  }
  return { r: hue(h + 1 / 3) * 255, g: hue(h) * 255, b: hue(h - 1 / 3) * 255 }
}

/**
 * Move `colour` along lightness — hue and saturation untouched — until it meets
 * `target` against `against`, searching in the direction that helps.
 *
 * Returns the original when it already passes, and the best it managed when even
 * pure black or white cannot reach the target (which happens for a very light
 * surface asking for a high ratio). Returning the closest attempt rather than
 * giving up keeps the caller's contract simple: it always gets a usable colour,
 * never null.
 */
export function adjustForContrast(colour: Rgb, against: Rgb, target: number): Rgb {
  if (contrast(colour, against) >= target) return colour
  const [h, s, l0] = toHsl(colour)
  // Darken if the surface is light, lighten if it is dark.
  const dir = luminance(against) > 0.5 ? -1 : 1
  let best = colour
  let bestRatio = contrast(colour, against)
  for (let step = 1; step <= 100; step++) {
    const l = l0 + dir * step * 0.01
    if (l < 0 || l > 1) break
    const cand = fromHsl(h, s, l)
    const ratio = contrast(cand, against)
    if (ratio > bestRatio) { best = cand; bestRatio = ratio }
    if (ratio >= target) return cand
  }
  return best
}

/** The accent as TEXT on `surface`. */
export function accentText(accent: string, surface: string, target = 4.5): string | null {
  const a = parseColor(accent)
  const s = parseColor(surface)
  if (!a || !s) return null
  return toHex(adjustForContrast(a, s, target))
}

/**
 * The accent as a FILLED SURFACE under `fg` (white by default).
 *
 * Note the inversion: here the accent is the BACKGROUND, so it must move away
 * from the foreground — the opposite direction from accentText on a dark theme.
 */
export function accentSolid(accent: string, fg = '#ffffff', target = 4.5): string | null {
  const a = parseColor(accent)
  const f = parseColor(fg)
  if (!a || !f) return null
  return toHex(adjustForContrast(a, f, target))
}

/**
 * Of several surfaces, the one `colour` reads worst against.
 *
 * Accent text does not sit on a single background: the desktop canvas, panels
 * and popovers are three different values, and on the light theme they get
 * LIGHTER in that order. Deriving against the canvas alone under-corrects —
 * measured, a value that cleared 4.5:1 on --bg-base came out at 4.18:1 on a
 * settings panel. Correcting for the worst one covers the rest by construction.
 */
export function worstSurfaceFor(colour: string, surfaces: string[]): string {
  const c = parseColor(colour)
  if (!c || surfaces.length === 0) return surfaces[0] ?? '#000000'
  let worst = surfaces[0]
  let worstRatio = Infinity
  for (const s of surfaces) {
    const p = parseColor(s)
    if (!p) continue
    const r = contrast(c, p)
    if (r < worstRatio) { worstRatio = r; worst = s }
  }
  return worst
}

/**
 * Flatten a translucent CSS colour over an opaque one, returning hex.
 *
 * Needed because the surfaces accent text lands on are not all opaque:
 * --accent-soft is `color-mix(accent N%, transparent)`, and a tint of the
 * accent's own hue is the hardest background the accent has to be read on.
 *
 * Resolution goes through a 1x1 canvas rather than a parser, because the value
 * may be color-mix(), oklch() or anything else CSS grows — the same reason the
 * contrast scanner stopped parsing colour strings by hand after oklch made it
 * silently skip half the dark theme.
 */
export function compositeOver(translucent: string, opaque: string): string | null {
  if (typeof document === 'undefined') return null
  const base = parseColor(opaque)
  if (!base || !translucent) return null
  try {
    const cv = document.createElement('canvas')
    cv.width = 1
    cv.height = 1
    const ctx = cv.getContext('2d', { willReadFrequently: true })
    if (!ctx) return null
    ctx.fillStyle = '#000'
    ctx.fillStyle = translucent
    ctx.clearRect(0, 0, 1, 1)
    ctx.fillRect(0, 0, 1, 1)
    const d = ctx.getImageData(0, 0, 1, 1).data
    const a = d[3] / 255
    return toHex({
      r: d[0] * a + base.r * (1 - a),
      g: d[1] * a + base.g * (1 - a),
      b: d[2] * a + base.b * (1 - a),
    })
  } catch {
    return null
  }
}
