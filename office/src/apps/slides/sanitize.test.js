/**
 * Unit tests: DOMPurify stripping of XSS payloads in slide content.
 *
 * These tests run in a jsdom environment (configured in vite.config.js).
 * Run with: npm test
 */

import { describe, it, expect } from 'vitest'
import DOMPurify from 'dompurify'

const PURIFY_CONFIG = {
  USE_PROFILES: { html: true },
  FORBID_TAGS: ['script', 'iframe', 'object', 'embed', 'form', 'input', 'button'],
  FORBID_ATTR: ['onerror', 'onload', 'onclick', 'onmouseover', 'onfocus', 'onblur',
                'onchange', 'onsubmit', 'onkeydown', 'onkeyup', 'onkeypress'],
}

const sanitize = (html) => DOMPurify.sanitize(html ?? '', PURIFY_CONFIG)

describe('slide content sanitization', () => {
  it('strips <script> tags entirely', () => {
    const result = sanitize('<p>Hello</p><script>alert("xss")</script>')
    expect(result).not.toContain('<script>')
    expect(result).not.toContain('alert')
    expect(result).toContain('Hello')
  })

  it('strips inline event handlers (onclick)', () => {
    const result = sanitize('<p onclick="alert(1)">Click me</p>')
    expect(result).not.toContain('onclick')
    expect(result).toContain('Click me')
  })

  it('strips javascript: URLs', () => {
    const result = sanitize('<a href="javascript:alert(1)">link</a>')
    expect(result).not.toContain('javascript:')
  })

  it('strips <iframe> tags', () => {
    const result = sanitize('<iframe src="https://evil.example.com"></iframe>')
    expect(result).not.toContain('<iframe')
  })

  it('strips onerror event handlers', () => {
    const result = sanitize('<img src="x" onerror="alert(1)">')
    expect(result).not.toContain('onerror')
  })

  it('preserves safe Tiptap HTML (paragraphs, bold, lists)', () => {
    const safe = '<p><strong>Bold</strong> and <em>italic</em></p><ul><li>Item</li></ul>'
    const result = sanitize(safe)
    expect(result).toContain('<strong>Bold</strong>')
    expect(result).toContain('<em>italic</em>')
    expect(result).toContain('<ul>')
    expect(result).toContain('<li>Item</li>')
  })

  it('handles null/undefined gracefully', () => {
    expect(() => sanitize(null)).not.toThrow()
    expect(() => sanitize(undefined)).not.toThrow()
    expect(sanitize(null)).toBe('')
    expect(sanitize(undefined)).toBe('')
  })
})
