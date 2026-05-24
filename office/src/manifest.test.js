/**
 * manifest.test.js — vitest tests for /public/manifest.webmanifest
 *
 * Validates that the PWA manifest for Vulos Office:
 *   1. Parses as valid JSON.
 *   2. Contains the required fields: name, short_name, start_url, display, theme_color, icons.
 *   3. Has at least two icon entries with src, sizes, and type.
 *
 * Run with: npm test
 */

import { describe, it, expect } from 'vitest'
import { readFileSync } from 'fs'
import { resolve, dirname } from 'path'
import { fileURLToPath } from 'url'

const __dirname = dirname(fileURLToPath(import.meta.url))
const manifestPath = resolve(__dirname, '../public/manifest.webmanifest')

describe('Vulos Office manifest.webmanifest', () => {
  let manifest

  it('parses as valid JSON', () => {
    const raw = readFileSync(manifestPath, 'utf-8')
    expect(() => { manifest = JSON.parse(raw) }).not.toThrow()
    manifest = JSON.parse(raw)
  })

  it('has required string fields', () => {
    const raw = readFileSync(manifestPath, 'utf-8')
    manifest = JSON.parse(raw)

    expect(manifest.name).toBe('Vulos Office')
    expect(manifest.short_name).toBe('Office')
    expect(manifest.start_url).toBe('/')
    expect(manifest.display).toBe('standalone')
    expect(manifest.theme_color).toBe('#0f6a6c')
  })

  it('has at least two icon entries with src, sizes, and type', () => {
    const raw = readFileSync(manifestPath, 'utf-8')
    manifest = JSON.parse(raw)

    expect(Array.isArray(manifest.icons)).toBe(true)
    expect(manifest.icons.length).toBeGreaterThanOrEqual(2)

    for (const icon of manifest.icons) {
      expect(typeof icon.src).toBe('string')
      expect(icon.src.length).toBeGreaterThan(0)
      expect(typeof icon.sizes).toBe('string')
      expect(typeof icon.type).toBe('string')
    }
  })

  it('includes a 192x192 and a 512x512 icon', () => {
    const raw = readFileSync(manifestPath, 'utf-8')
    manifest = JSON.parse(raw)

    const sizes = manifest.icons.map((i) => i.sizes)
    expect(sizes).toContain('192x192')
    expect(sizes).toContain('512x512')
  })
})
