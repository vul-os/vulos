import { describe, it, expect } from 'vitest'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'

// In the phone multitasking preview the File Explorer showed filenames
// truncated to about three characters — "De…", "Do…", "Mu…" — while Size and
// Modified kept their full width. A file list where no name is readable is not
// a file list.
//
// The cause: Size (w-16) and Modified (w-32) are both shrink-0, so they always
// claim ~192px, while Name is flex-1 min-w-0 and absorbs the entire squeeze.
//
// Asserted against the SOURCE rather than a render, deliberately: jsdom does not
// do layout, so a rendering test could not observe the truncation that motivated
// this and would pass whatever the classes said. Reading the markup at least
// pins the decision — and the comment above the header explains it to the next
// person, which a class name alone does not.

const SRC = resolve(__dirname, '../FileManager.tsx')

describe('FileManager columns on a narrow window', () => {
  it('drops Modified via a CONTAINER query, not a viewport breakpoint', () => {
    const src = readFileSync(SRC, 'utf8')

    // A file window can be narrow on a wide screen, so the viewport is the
    // wrong thing to measure. `sm:`/`md:` here would leave the phone-preview
    // case broken while looking fixed on a resized browser.
    const modifiedCols = src.match(/w-32 shrink-0/g) ?? []
    expect(modifiedCols.length).toBeGreaterThan(0)

    const guarded = src.match(/@md:block w-32 shrink-0/g) ?? []
    expect(
      guarded.length,
      'every Modified column must be @container-guarded, or header and rows fall out of alignment on a narrow window',
    ).toBe(modifiedCols.length)
  })

  it('establishes a container for those queries to resolve against', () => {
    // An @md: class with no @container ancestor silently never matches, so the
    // column would always show and the fix would be inert.
    //
    // Matched inside a className, NOT anywhere in the file. The first version
    // asserted `src.toContain('@container')` and SURVIVED a mutation that
    // removed the class — because the explanatory comment above the header
    // contains the word. A test satisfied by its own prose checks nothing.
    const src = readFileSync(SRC, 'utf8')
    expect(
      /className="@container[ "]/.test(src),
      'no element carries the @container class, so every @md: guard below it is inert',
    ).toBe(true)
  })

  it('keeps Size, which is a quarter the width and more useful when space is scarce', () => {
    const src = readFileSync(SRC, 'utf8')
    expect(src).toContain('w-16 shrink-0')
    expect(src).not.toContain('@md:block w-16')
  })
})
