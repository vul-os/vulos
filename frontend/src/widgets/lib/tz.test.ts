// tz.test.ts — the boundary cases that break naive world clocks.
//
// Every assertion here is a bug a hand-rolled offset model actually has. They
// are written against FIXED INSTANTS rather than `new Date()` so they mean the
// same thing in January and in July, and in any zone CI happens to run in.
import { describe, it, expect } from 'vitest'
import {
  CITY_PRESETS, dayDelta, dayLabel, dayPhase, formatOffset, formatZoneDate,
  formatZoneTime, isValidTimeZone, labelForZone, localTimeZone, zoneHourFraction,
  zoneOffsetMinutes, zoneParts,
} from './tz'

const JAN = new Date('2026-01-15T12:00:00Z')
const JUL = new Date('2026-07-15T12:00:00Z')

describe('zoneOffsetMinutes — DST, in both hemispheres at once', () => {
  it('moves New York between EST and EDT', () => {
    expect(zoneOffsetMinutes(JAN, 'America/New_York')).toBe(-300) // UTC-5
    expect(zoneOffsetMinutes(JUL, 'America/New_York')).toBe(-240) // UTC-4
  })

  it('moves Sydney the OPPOSITE way in the same months', () => {
    // This is the pair that makes a fixed offset table wrong all year: when New
    // York springs forward Sydney falls back, so the NY↔Sydney gap is 14h, 15h
    // or 16h depending on the date and is 16h for only ~3 weeks a year.
    expect(zoneOffsetMinutes(JAN, 'Australia/Sydney')).toBe(660) // UTC+11 AEDT
    expect(zoneOffsetMinutes(JUL, 'Australia/Sydney')).toBe(600) // UTC+10 AEST
  })

  it('gives the NY↔Sydney gap as 16h in January and 14h in July', () => {
    const janGap = (zoneOffsetMinutes(JAN, 'Australia/Sydney')! - zoneOffsetMinutes(JAN, 'America/New_York')!) / 60
    const julGap = (zoneOffsetMinutes(JUL, 'Australia/Sydney')! - zoneOffsetMinutes(JUL, 'America/New_York')!) / 60
    expect(janGap).toBe(16)
    expect(julGap).toBe(14)
  })

  it('switches New York exactly at the spring-forward instant', () => {
    // 2026-03-08 07:00 UTC is 02:00 EST → 03:00 EDT.
    expect(zoneOffsetMinutes(new Date('2026-03-08T06:59:00Z'), 'America/New_York')).toBe(-300)
    expect(zoneOffsetMinutes(new Date('2026-03-08T07:00:00Z'), 'America/New_York')).toBe(-240)
  })
})

describe('zoneOffsetMinutes — offsets that are not whole hours', () => {
  it('handles a half-hour zone', () => {
    expect(zoneOffsetMinutes(JAN, 'Asia/Kolkata')).toBe(330) // +05:30
    expect(zoneOffsetMinutes(JUL, 'Asia/Kolkata')).toBe(330) // no DST, ever
  })

  it('handles a quarter-hour zone', () => {
    expect(zoneOffsetMinutes(JAN, 'Asia/Kathmandu')).toBe(345) // +05:45
  })

  it('handles a quarter-hour zone that ALSO observes DST', () => {
    expect(zoneOffsetMinutes(JAN, 'Pacific/Chatham')).toBe(825) // +13:45
    expect(zoneOffsetMinutes(JUL, 'Pacific/Chatham')).toBe(765) // +12:45
  })
})

describe('formatOffset', () => {
  it('prints whole and fractional hours the way a human writes them', () => {
    expect(formatOffset(-300)).toBe('-5')
    expect(formatOffset(330)).toBe('+5:30')
    expect(formatOffset(345)).toBe('+5:45')
    expect(formatOffset(825)).toBe('+13:45')
    expect(formatOffset(0)).toBe('+0')
    expect(formatOffset(null)).toBe('')
  })
})

describe('dayDelta — the date over there is not the date here', () => {
  it('is +1 when it is already tomorrow in Sydney', () => {
    // 2026-08-15 03:30 UTC = 2026-08-14 23:30 in New York, 2026-08-15 13:30 in
    // Sydney. Same instant, different DATES — a clock showing only HH:MM would
    // silently report the wrong day.
    const t = new Date('2026-08-15T03:30:00Z')
    expect(zoneParts(t, 'America/New_York')!.day).toBe(14)
    expect(zoneParts(t, 'Australia/Sydney')!.day).toBe(15)
    expect(dayDelta(t, 'Australia/Sydney', 'America/New_York')).toBe(1)
    expect(dayDelta(t, 'America/New_York', 'Australia/Sydney')).toBe(-1)
  })

  it('is 0 when both zones are on the same date despite a big offset', () => {
    // 2026-08-15 12:00 UTC = 08:00 NY, 22:00 Sydney — 14 hours apart, same date.
    const t = new Date('2026-08-15T12:00:00Z')
    expect(dayDelta(t, 'Australia/Sydney', 'America/New_York')).toBe(0)
  })

  it('handles a half-hour zone crossing midnight', () => {
    // 18:45 UTC + 05:30 = 00:15 the NEXT day in Kolkata.
    const t = new Date('2026-08-15T18:45:00Z')
    const p = zoneParts(t, 'Asia/Kolkata')!
    expect(p.day).toBe(16)
    expect(p.hour).toBe(0)
    expect(p.minute).toBe(15)
    expect(dayDelta(t, 'Asia/Kolkata', 'UTC')).toBe(1)
  })

  it('handles a quarter-hour zone crossing midnight', () => {
    // 18:20 UTC + 05:45 = 00:05 the next day in Kathmandu.
    const p = zoneParts(new Date('2026-08-15T18:20:00Z'), 'Asia/Kathmandu')!
    expect(p.day).toBe(16)
    expect(p.hour).toBe(0)
    expect(p.minute).toBe(5)
  })

  it('crosses a year boundary', () => {
    // 2026-12-31 22:00 UTC is already 2027-01-01 09:00 in Sydney.
    const t = new Date('2026-12-31T22:00:00Z')
    const p = zoneParts(t, 'Australia/Sydney')!
    expect(p.year).toBe(2027)
    expect(p.month).toBe(1)
    expect(p.day).toBe(1)
    expect(dayDelta(t, 'Australia/Sydney', 'UTC')).toBe(1)
  })

  it('reports midnight as hour 0, never hour 24', () => {
    // hourCycle h23 vs hour12:false. With the h24 cycle midnight renders as
    // "24", which sorts AFTER 23:00 and would put the dial's hour hand at noon.
    const p = zoneParts(new Date('2026-08-15T00:00:00Z'), 'UTC')!
    expect(p.hour).toBe(0)
  })
})

describe('dayLabel', () => {
  it('says nothing when the dates agree', () => {
    expect(dayLabel(0)).toBe('')
    expect(dayLabel(null)).toBe('')
  })
  it('names the adjacent days and counts the rest', () => {
    expect(dayLabel(1)).toBe('Tomorrow')
    expect(dayLabel(-1)).toBe('Yesterday')
    expect(dayLabel(2)).toBe('+2 days')
    expect(dayLabel(-3)).toBe('-3 days')
  })
})

describe('bad input never throws', () => {
  it('rejects unknown and malformed zone ids', () => {
    expect(isValidTimeZone('Mars/Olympus')).toBe(false)
    expect(isValidTimeZone('')).toBe(false)
    expect(isValidTimeZone(null)).toBe(false)
    expect(isValidTimeZone(42)).toBe(false)
    expect(isValidTimeZone('America/New_York')).toBe(true)
  })

  it('returns null rather than throwing for an unknown zone', () => {
    // A RangeError here would be a white screen for the entire desktop: this
    // widget renders on the shell's tree and persisted settings outlive tzdata
    // entries.
    expect(zoneParts(JAN, 'Mars/Olympus')).toBeNull()
    expect(zoneOffsetMinutes(JAN, 'Mars/Olympus')).toBeNull()
    expect(dayDelta(JAN, 'Mars/Olympus', 'UTC')).toBeNull()
    expect(zoneHourFraction(JAN, 'Mars/Olympus')).toBeNull()
    expect(dayPhase(JAN, 'Mars/Olympus')).toBeNull()
    expect(formatZoneTime(JAN, 'Mars/Olympus')).toBe('--:--')
    expect(formatZoneDate(JAN, 'Mars/Olympus')).toBe('')
  })

  it('returns null for an invalid Date', () => {
    expect(zoneParts(new Date('nonsense'), 'UTC')).toBeNull()
  })
})

describe('presentation helpers', () => {
  it('formats the time in the named zone, not the local one', () => {
    // Pinned to en-GB so the assertion does not depend on the CI box's locale.
    expect(formatZoneTime(JAN, 'UTC', { locale: 'en-GB' })).toBe('12:00')
    expect(formatZoneTime(JAN, 'Asia/Kolkata', { locale: 'en-GB' })).toBe('17:30')
    expect(formatZoneTime(JAN, 'Asia/Kathmandu', { locale: 'en-GB' })).toBe('17:45')
  })

  it('gives the fractional hour used by the dial', () => {
    expect(zoneHourFraction(new Date('2026-08-15T06:30:00Z'), 'UTC')).toBeCloseTo(6.5, 5)
    expect(zoneHourFraction(new Date('2026-08-15T06:30:00Z'), 'Asia/Kolkata')).toBeCloseTo(12, 5)
  })

  it('classifies the local hour, not the box\'s hour', () => {
    // The same instant is night in one place and daytime in another — which is
    // the entire reason the phase is computed per zone rather than from the
    // box's clock. 02:00 UTC is 03:00 in London (BST) and 11:00 in Tokyo.
    const t = new Date('2026-08-15T02:00:00Z')
    expect(dayPhase(t, 'Europe/London')).toBe('night')
    expect(dayPhase(t, 'Asia/Tokyo')).toBe('day')
    // …and twelve hours later they have swapped: 14:00 UTC is 15:00 in London
    // and 23:00 in Tokyo.
    const later = new Date('2026-08-15T14:00:00Z')
    expect(dayPhase(later, 'Europe/London')).toBe('day')
    expect(dayPhase(later, 'Asia/Tokyo')).toBe('night')
    // The twilight band, at 18:30 local.
    expect(dayPhase(new Date('2026-08-15T17:30:00Z'), 'Europe/London')).toBe('twilight')
  })

  it('derives a readable label for any zone', () => {
    expect(labelForZone('America/New_York')).toBe('New York')
    expect(labelForZone('Australia/Sydney')).toBe('Sydney')
    // Not in the preset list — falls back to the last path segment.
    expect(labelForZone('America/Argentina/Ushuaia')).toBe('Ushuaia')
    expect(labelForZone('Africa/Porto-Novo')).toBe('Porto-Novo')
  })

  it('always resolves a usable local zone', () => {
    expect(isValidTimeZone(localTimeZone())).toBe(true)
  })
})

describe('the city preset list', () => {
  it('names only zones this platform actually knows', () => {
    // A preset the platform cannot resolve would render as a dead row in the
    // settings picker — and this list is the one place zone ids are typed by
    // hand, so it is exactly where a typo hides.
    for (const c of CITY_PRESETS) {
      expect(isValidTimeZone(c.zone), `${c.label} → ${c.zone}`).toBe(true)
    }
  })

  it('includes the two the founder named, and the awkward offsets', () => {
    const zones = CITY_PRESETS.map((c) => c.zone)
    expect(zones).toContain('America/New_York')
    expect(zones).toContain('Australia/Sydney')
    expect(zones).toContain('Asia/Kolkata')   // +05:30
    expect(zones).toContain('Asia/Kathmandu') // +05:45
    expect(zones).toContain('Pacific/Chatham') // +12:45/+13:45
  })
})
