// tz.ts — time-zone arithmetic for the world-clock widget, done through the
// platform's own IANA database instead of hand-rolled offsets.
//
// WHY THIS FILE EXISTS AT ALL
//
// The naive world clock is `new Date(Date.now() + offsetHours * 3600_000)`, and
// it is wrong four different ways at once:
//
//   1. It bakes in a FIXED offset, so it is wrong for half the year in every
//      zone that observes DST — and the two zones the founder named are the
//      worst possible pair, because New York and Sydney observe DST in OPPOSITE
//      hemispheres. Their gap is 14h, 15h or 16h depending on the date, and it
//      is 16h for only about three weeks a year.
//   2. It assumes offsets are whole hours. Kolkata is +05:30, Kathmandu is
//      +05:45, Chatham is +12:45. A number-of-hours model cannot express them.
//   3. It confuses "the time over there" with "the date over there". At 22:00
//      in New York it is already TOMORROW in Sydney, and a clock that renders
//      only HH:MM silently tells the user the wrong day.
//   4. It shifts the timestamp instead of the presentation, so every downstream
//      `getDay()`/`getDate()` reads a lie.
//
// Everything here therefore goes through `Intl.DateTimeFormat` with an explicit
// `timeZone`, which is backed by the platform's IANA tzdata and updates with the
// OS. We never construct a shifted Date and never store an offset: the offset is
// DERIVED from the formatted civil time whenever it is needed for display.
//
// Nothing in this module touches the network, storage or the DOM. It is a pure
// function of (instant, zone id), which is what makes the boundary cases in
// tz.test.ts actually assertable.

/** A civil ("wall clock") date-time as read off a clock in some zone. */
export interface ZoneParts {
  year: number
  month: number // 1-12, NOT the 0-11 that Date uses
  day: number
  hour: number // 0-23
  minute: number
  second: number
}

const DAY_MS = 86_400_000

// Intl.DateTimeFormat construction is not free and a clock rail re-renders every
// second, so formatters are memoised per (zone, shape). The map is bounded in
// practice by the number of zones a user pins.
const partsFormatters = new Map<string, Intl.DateTimeFormat>()

/**
 * True when the platform's tzdata knows this zone id.
 *
 * `Intl.DateTimeFormat` throws a RangeError for an unknown `timeZone`, and an
 * uncaught RangeError inside a render is a white screen for the whole shell —
 * this widget lives on the desktop, so a bad zone id in persisted user settings
 * must degrade to "unknown zone", never to a blank OS. Every entry point below
 * routes through this guard and returns null rather than throwing.
 */
export function isValidTimeZone(timeZone: unknown): timeZone is string {
  if (typeof timeZone !== 'string' || timeZone.length === 0) return false
  try {
    new Intl.DateTimeFormat('en-US', { timeZone })
    return true
  } catch {
    return false
  }
}

/**
 * The civil date-time in `timeZone` at instant `date`, or null for a bad zone.
 *
 * `hourCycle: 'h23'` rather than `hour12: false`: with `hour12: false` several
 * engines render midnight as hour "24" (the h24 cycle), which would make
 * midnight compare as a LATER hour than 23:00 and break both the day-boundary
 * label and the analog dial. h23 pins midnight to 0.
 */
export function zoneParts(date: Date, timeZone: string): ZoneParts | null {
  if (!isValidTimeZone(timeZone)) return null
  if (!(date instanceof Date) || Number.isNaN(date.getTime())) return null
  let fmt = partsFormatters.get(timeZone)
  if (!fmt) {
    fmt = new Intl.DateTimeFormat('en-US', {
      timeZone,
      hourCycle: 'h23',
      year: 'numeric',
      month: '2-digit',
      day: '2-digit',
      hour: '2-digit',
      minute: '2-digit',
      second: '2-digit',
      era: 'short',
    })
    partsFormatters.set(timeZone, fmt)
  }
  const out: Record<string, number> = {}
  let bc = false
  for (const p of fmt.formatToParts(date)) {
    if (p.type === 'era') { bc = p.value === 'BC' || p.value === 'B' }
    else if (p.type !== 'literal') out[p.type] = Number(p.value)
  }
  if (
    !Number.isFinite(out.year) || !Number.isFinite(out.month) || !Number.isFinite(out.day) ||
    !Number.isFinite(out.hour) || !Number.isFinite(out.minute) || !Number.isFinite(out.second)
  ) return null
  return {
    // The proleptic year: Intl reports year 1 BC as "1 BC", and Date.UTC's
    // astronomical numbering calls it 0. Only reachable with an absurd clock
    // skew, but converting through Date.UTC below would otherwise be off by
    // exactly one year and silently so.
    year: bc ? 1 - out.year : out.year,
    month: out.month,
    day: out.day,
    hour: out.hour,
    minute: out.minute,
    second: out.second,
  }
}

/**
 * The zone's UTC offset AT THIS INSTANT, in minutes east of UTC.
 *
 * Derived, never tabulated: we ask the platform what the wall clock reads in the
 * zone, reinterpret those civil fields as if they were UTC, and subtract the
 * real instant. The difference IS the offset that was in force at that instant,
 * so DST is handled by construction — there is no table here to go stale and no
 * "is it summer" branch to get backwards in the southern hemisphere.
 *
 * Returns minutes (not hours) so +05:30 and +05:45 are exactly representable.
 */
export function zoneOffsetMinutes(date: Date, timeZone: string): number | null {
  const p = zoneParts(date, timeZone)
  if (!p) return null
  const asUTC = Date.UTC(p.year, p.month - 1, p.day, p.hour, p.minute, p.second)
  // Drop sub-second precision from the instant so the subtraction yields a whole
  // number of minutes; formatToParts has no milliseconds to give back.
  const instant = Math.floor(date.getTime() / 1000) * 1000
  return Math.round((asUTC - instant) / 60_000)
}

/** "+5:30", "-4", "+0", "+12:45" — the offset as a human reads it. */
export function formatOffset(minutes: number | null): string {
  if (minutes === null || !Number.isFinite(minutes)) return ''
  const sign = minutes < 0 ? '-' : '+'
  const abs = Math.abs(minutes)
  const h = Math.floor(abs / 60)
  const m = abs % 60
  return m === 0 ? `${sign}${h}` : `${sign}${h}:${String(m).padStart(2, '0')}`
}

/** The zone's civil date as a whole-day number, for comparing dates across zones. */
function civilDayNumber(p: ZoneParts): number {
  return Date.UTC(p.year, p.month - 1, p.day) / DAY_MS
}

/**
 * How many CALENDAR DAYS ahead (+) or behind (-) `timeZone` is of `referenceZone`
 * at this instant. 0 when both are on the same date.
 *
 * This is the question a world clock actually has to answer and the one a raw
 * offset comparison gets wrong: what matters is not "is the offset bigger" but
 * "does the date on the wall differ", and those come apart around midnight. At
 * 23:30 New York on the 3rd it is 14:30 Sydney on the FOURTH (+1), while at
 * 09:00 New York on the 3rd it is 00:00 Sydney on the fourth — also +1 — and at
 * 21:00 Sydney on the 3rd it is 06:00 New York on the SAME 3rd (0). Comparing
 * dates, not times, is the only formulation that holds at every hour.
 */
export function dayDelta(date: Date, timeZone: string, referenceZone: string): number | null {
  const a = zoneParts(date, timeZone)
  const b = zoneParts(date, referenceZone)
  if (!a || !b) return null
  return civilDayNumber(a) - civilDayNumber(b)
}

/** The box's own zone id, with a defined answer when the platform won't say. */
export function localTimeZone(): string {
  try {
    const tz = Intl.DateTimeFormat().resolvedOptions().timeZone
    return isValidTimeZone(tz) ? tz : 'UTC'
  } catch {
    return 'UTC'
  }
}

/**
 * "Today" / "Tomorrow" / "Yesterday" / "+2 days" relative to the reference zone.
 * Empty string when the dates agree — a clock showing the same date as the box
 * should say nothing rather than shout "Today" on every tile.
 */
export function dayLabel(delta: number | null): string {
  if (delta === null || delta === 0) return ''
  if (delta === 1) return 'Tomorrow'
  if (delta === -1) return 'Yesterday'
  return delta > 0 ? `+${delta} days` : `${delta} days`
}

/** HH:MM in the zone, in the viewer's locale conventions (12h or 24h). */
export function formatZoneTime(
  date: Date,
  timeZone: string,
  opts: { seconds?: boolean; locale?: string } = {},
): string {
  if (!isValidTimeZone(timeZone)) return '--:--'
  try {
    return new Intl.DateTimeFormat(opts.locale, {
      timeZone,
      hour: 'numeric',
      minute: '2-digit',
      ...(opts.seconds ? { second: '2-digit' } : {}),
    }).format(date)
  } catch {
    return '--:--'
  }
}

/** "Mon, 4 Aug" in the zone — the date over THERE, not the date here. */
export function formatZoneDate(date: Date, timeZone: string, locale?: string): string {
  if (!isValidTimeZone(timeZone)) return ''
  try {
    return new Intl.DateTimeFormat(locale, {
      timeZone,
      weekday: 'short',
      month: 'short',
      day: 'numeric',
    }).format(date)
  } catch {
    return ''
  }
}

/**
 * Fractional hours since local midnight in the zone (0 ≤ h < 24), for the analog
 * dial and the day/night shading. Null for a bad zone.
 */
export function zoneHourFraction(date: Date, timeZone: string): number | null {
  const p = zoneParts(date, timeZone)
  if (!p) return null
  return p.hour + p.minute / 60 + p.second / 3600
}

/**
 * Rough day/night classification from the zone's local hour, used only to tint a
 * clock face. Deliberately NOT sunrise/sunset: real solar times need a latitude
 * and longitude for every city, which is a data set this widget has no business
 * shipping and no way to keep honest. A tile that says "night" at 03:00 local is
 * telling the truth about the clock; it is not claiming to know the sun.
 */
export function dayPhase(date: Date, timeZone: string): 'day' | 'night' | 'twilight' | null {
  const h = zoneHourFraction(date, timeZone)
  if (h === null) return null
  if (h >= 7 && h < 18) return 'day'
  if ((h >= 6 && h < 7) || (h >= 18 && h < 20)) return 'twilight'
  return 'night'
}

/** A city a user can pin. `label` is what the tile shows; `zone` is the IANA id. */
export interface ClockCity {
  label: string
  zone: string
}

/**
 * The pick-list offered in the widget's settings. Not an exhaustive tzdata dump
 * (that is ~600 entries and unusable in a 250px rail) — a curated set of the
 * places people actually pin, chosen to cover every awkward offset shape:
 * half-hour (Kolkata), quarter-hour (Kathmandu, Chatham), opposite-hemisphere
 * DST (Sydney/Auckland vs New York/London) and a no-DST zone (Johannesburg).
 * A user is not limited to it: any valid IANA id can be typed in.
 */
export const CITY_PRESETS: ClockCity[] = [
  { label: 'New York', zone: 'America/New_York' },
  { label: 'Sydney', zone: 'Australia/Sydney' },
  { label: 'London', zone: 'Europe/London' },
  { label: 'Los Angeles', zone: 'America/Los_Angeles' },
  { label: 'Chicago', zone: 'America/Chicago' },
  { label: 'São Paulo', zone: 'America/Sao_Paulo' },
  { label: 'Lagos', zone: 'Africa/Lagos' },
  { label: 'Johannesburg', zone: 'Africa/Johannesburg' },
  { label: 'Nairobi', zone: 'Africa/Nairobi' },
  { label: 'Berlin', zone: 'Europe/Berlin' },
  { label: 'Dubai', zone: 'Asia/Dubai' },
  { label: 'Karachi', zone: 'Asia/Karachi' },
  { label: 'Mumbai', zone: 'Asia/Kolkata' },
  { label: 'Kathmandu', zone: 'Asia/Kathmandu' },
  { label: 'Singapore', zone: 'Asia/Singapore' },
  { label: 'Shanghai', zone: 'Asia/Shanghai' },
  { label: 'Tokyo', zone: 'Asia/Tokyo' },
  { label: 'Auckland', zone: 'Pacific/Auckland' },
  { label: 'Chatham', zone: 'Pacific/Chatham' },
  { label: 'UTC', zone: 'UTC' },
]

/** A readable label for an arbitrary zone id: "Asia/Kolkata" → "Kolkata". */
export function labelForZone(zone: string): string {
  const preset = CITY_PRESETS.find((c) => c.zone === zone)
  if (preset) return preset.label
  const tail = zone.split('/').pop() || zone
  return tail.replace(/_/g, ' ')
}
