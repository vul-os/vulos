import { describe, expect, it } from 'vitest'
import { archCompat, normalizeArch, requiredArches, isUniversalArch } from '../arch'

/**
 * The architecture comparison, tested in both spellings on both sides.
 *
 * This is the whole reason arch.ts exists as a module rather than a line of
 * code inside a component. The comparison it replaced was
 * `app.arch.includes(systemArch)` — a raw string match — and its failure mode
 * is the dangerous kind: `["x86_64"]` never matches `"amd64"`, so a Flathub app
 * on an amd64 box reads as incompatible, consistently, with no error anywhere.
 * Nothing crashes, nothing logs, and roughly three quarters of the desktop
 * catalogue quietly becomes uninstallable.
 */

describe('normalizeArch', () => {
  it('folds both ecosystems onto the Debian spelling registry.json is written in', () => {
    // Debian / dpkg / apt          Flatpak / uname -m / OCI
    expect(normalizeArch('amd64')).toBe('amd64')
    expect(normalizeArch('x86_64')).toBe('amd64')
    expect(normalizeArch('x86-64')).toBe('amd64')
    expect(normalizeArch('x64')).toBe('amd64')

    expect(normalizeArch('arm64')).toBe('arm64')
    expect(normalizeArch('aarch64')).toBe('arm64')

    expect(normalizeArch('armhf')).toBe('armhf')
    expect(normalizeArch('armv7l')).toBe('armhf')

    expect(normalizeArch('i386')).toBe('i386')
    expect(normalizeArch('i686')).toBe('i386')
  })

  it('is tolerant of the casing and whitespace a server might send', () => {
    expect(normalizeArch('X86_64')).toBe('amd64')
    expect(normalizeArch('  aarch64 ')).toBe('arm64')
  })

  it('passes an unrecognised architecture through instead of guessing', () => {
    // The alternative is worse in both directions: mapping an unknown to
    // "universal" offers an install that cannot work, and mapping it to a
    // sentinel hides an app that might.
    expect(normalizeArch('ppc64el')).toBe('ppc64el')
    expect(normalizeArch('s390x')).toBe('s390x')
  })
})

describe('archCompat', () => {
  it('offers an x86_64-only app on an amd64 box, in either spelling', () => {
    // Steam, Chrome, Spotify, Zoom, VS Code, Discord and Slack are all in this
    // shape — Flathub metadata, x86_64-only.
    expect(archCompat(['x86_64'], 'amd64')).toBe('yes')
    expect(archCompat(['amd64'], 'x86_64')).toBe('yes')
    expect(archCompat(['x86_64'], 'x86_64')).toBe('yes')
    expect(archCompat(['amd64'], 'amd64')).toBe('yes')
  })

  it('does NOT offer an x86_64-only app on an arm64 box, in either spelling', () => {
    // The founder's requirement, stated as a test: an arm64 box must not offer
    // an install that cannot succeed.
    expect(archCompat(['x86_64'], 'arm64')).toBe('no')
    expect(archCompat(['x86_64'], 'aarch64')).toBe('no')
    expect(archCompat(['amd64'], 'arm64')).toBe('no')
    expect(archCompat(['amd64'], 'aarch64')).toBe('no')
  })

  it('offers a multi-arch app on both boxes', () => {
    expect(archCompat(['amd64', 'arm64'], 'arm64')).toBe('yes')
    expect(archCompat(['amd64', 'arm64'], 'amd64')).toBe('yes')
    expect(archCompat(['x86_64', 'aarch64'], 'arm64')).toBe('yes')
  })

  it('treats an app with no declared architecture as running anywhere', () => {
    // Web apps and services: the registry declares `"arch": []` for these, and
    // most of the catalogue's web tier looks like this.
    expect(archCompat([], 'arm64')).toBe('yes')
    expect(archCompat(undefined, 'arm64')).toBe('yes')
    expect(archCompat(['all'], 'arm64')).toBe('yes')
    expect(archCompat(['any'], 'arm64')).toBe('yes')
    expect(archCompat(['noarch'], 'riscv64')).toBe('yes')
  })

  it('says "unknown" rather than guessing when the box has not reported its architecture', () => {
    // Both alternatives are defects with a user-visible cost. Answering "yes"
    // is what the code did before this module and it offers installs that fail
    // in apt. Answering "no" marks the whole catalogue unavailable for the
    // moment before the box replies — and permanently on any backend that does
    // not report architecture at all, which is every backend today.
    expect(archCompat(['amd64'], null)).toBe('unknown')
    expect(archCompat(['x86_64'], null)).toBe('unknown')
    // ...but an app that needs no particular architecture is still installable
    // on a box whose architecture is unknown, because nothing about the machine
    // can make it not so.
    expect(archCompat([], null)).toBe('yes')
    expect(archCompat(['all'], null)).toBe('yes')
  })

  it('rejects an architecture neither side recognises rather than falling open', () => {
    expect(archCompat(['ppc64el'], 'amd64')).toBe('no')
    expect(archCompat(['ppc64el'], 'ppc64el')).toBe('yes')
  })
})

describe('isUniversalArch', () => {
  it('knows the words that mean "no particular machine"', () => {
    expect(isUniversalArch('all')).toBe(true)
    expect(isUniversalArch('any')).toBe(true)
    expect(isUniversalArch('noarch')).toBe(true)
    expect(isUniversalArch('amd64')).toBe(false)
  })
})

describe('requiredArches', () => {
  it('canonicalises for display so one requirement does not read as two', () => {
    // Flathub metadata merged with Debian's produces exactly this.
    expect(requiredArches(['x86_64', 'amd64'])).toEqual(['amd64'])
    expect(requiredArches(['amd64', 'aarch64'])).toEqual(['amd64', 'arm64'])
  })

  it('drops universal markers, which are not a requirement to state', () => {
    expect(requiredArches(['all'])).toEqual([])
    expect(requiredArches([])).toEqual([])
    expect(requiredArches(undefined)).toEqual([])
  })
})
