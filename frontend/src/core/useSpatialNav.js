/**
 * useSpatialNav — D-pad / arrow-key spatial navigation for the TV device profile.
 *
 * Active ONLY when the document root carries data-device-profile="tv".
 * Operates on all standard focusable elements without per-app rewiring.
 *
 * Navigation algorithm:
 *   For each arrow key press, collect every focusable element that is
 *   (a) visible in the viewport and (b) positioned in the pressed direction
 *   relative to the currently-focused element's centre.  Among those
 *   candidates, pick the one with the smallest weighted distance:
 *     score = axial_distance + 2 * lateral_distance
 *   This strongly prefers movement along the intended axis while still
 *   allowing diagonal recovery when no element is perfectly aligned.
 *
 * Enter key → clicks the currently-focused element (activates buttons,
 * links, etc.) or, if nothing is focused yet, focuses the first focusable.
 */

import { useEffect } from 'react'

// ─── Constants ──────────────────────────────────────────────────────────────

const FOCUSABLE_SELECTORS = [
  'a[href]',
  'button:not([disabled])',
  'input:not([disabled])',
  'select:not([disabled])',
  'textarea:not([disabled])',
  '[tabindex]:not([tabindex="-1"])',
  '[contenteditable="true"]',
].join(', ')

// Weight penalty for off-axis distance vs on-axis distance.
// Higher values = stricter straight-line preference.
const LATERAL_WEIGHT = 2.5

// ─── Helpers ────────────────────────────────────────────────────────────────

/** Centre point of a DOMRect. */
function centre(rect) {
  return {
    x: rect.left + rect.width / 2,
    y: rect.top + rect.height / 2,
  }
}

/** True when the element is visible and inside the current viewport. */
function isVisible(el) {
  const rect = el.getBoundingClientRect()
  if (rect.width === 0 && rect.height === 0) return false
  if (rect.bottom < 0 || rect.top > window.innerHeight) return false
  if (rect.right < 0 || rect.left > window.innerWidth) return false
  // Rough computed-style check — avoids fully hidden elements
  const style = window.getComputedStyle(el)
  if (style.display === 'none' || style.visibility === 'hidden') return false
  if (parseFloat(style.opacity) === 0) return false
  return true
}

/**
 * Given the current element's centre and a direction, score a candidate
 * element.  Returns Infinity if the candidate is not in the target
 * half-plane.
 */
function scoreCandidate(fromCentre, toRect, direction) {
  const to = centre(toRect)
  const dx = to.x - fromCentre.x
  const dy = to.y - fromCentre.y

  let axial, lateral
  switch (direction) {
    case 'ArrowRight':
      if (dx <= 0) return Infinity
      axial = dx
      lateral = Math.abs(dy)
      break
    case 'ArrowLeft':
      if (dx >= 0) return Infinity
      axial = -dx
      lateral = Math.abs(dy)
      break
    case 'ArrowDown':
      if (dy <= 0) return Infinity
      axial = dy
      lateral = Math.abs(dx)
      break
    case 'ArrowUp':
      if (dy >= 0) return Infinity
      axial = -dy
      lateral = Math.abs(dx)
      break
    default:
      return Infinity
  }

  return axial + LATERAL_WEIGHT * lateral
}

/**
 * Return all focusable elements that are currently visible.
 */
function getFocusables(root) {
  return Array.from(root.querySelectorAll(FOCUSABLE_SELECTORS)).filter(isVisible)
}

/**
 * Find the best focusable in the given direction relative to `current`.
 * Falls back to a viewport-edge element when no candidate is found
 * (wrapping is intentionally NOT done to preserve TV UX predictability).
 */
function findBest(current, direction, focusables) {
  const fromRect = current.getBoundingClientRect()
  const from = centre(fromRect)

  let bestEl = null
  let bestScore = Infinity

  for (const el of focusables) {
    if (el === current) continue
    const rect = el.getBoundingClientRect()
    const score = scoreCandidate(from, rect, direction)
    if (score < bestScore) {
      bestScore = score
      bestEl = el
    }
  }

  return bestEl
}

// ─── Hook ────────────────────────────────────────────────────────────────────

export function useSpatialNav() {
  useEffect(() => {
    function isTVProfile() {
      return document.documentElement.dataset.deviceProfile === 'tv'
    }

    function handleKeyDown(e) {
      // Guard: only active in TV profile
      if (!isTVProfile()) return

      const { key } = e
      const root = document.documentElement

      // ── Enter → activate focused element ──────────────────────────────────
      if (key === 'Enter') {
        const focused = document.activeElement
        if (!focused || focused === document.body || focused === root) {
          // Nothing focused yet — move focus to first focusable
          const first = getFocusables(root)[0]
          if (first) {
            e.preventDefault()
            first.focus()
          }
          return
        }
        // Let naturally-clickable elements handle their own Enter (inputs,
        // textareas, selects) — only synthesise click for others.
        const tag = focused.tagName
        if (tag === 'INPUT' || tag === 'TEXTAREA' || tag === 'SELECT') return
        e.preventDefault()
        focused.click()
        return
      }

      // ── Arrow keys → spatial navigation ───────────────────────────────────
      if (!['ArrowUp', 'ArrowDown', 'ArrowLeft', 'ArrowRight'].includes(key)) return

      e.preventDefault()

      const focusables = getFocusables(root)
      if (focusables.length === 0) return

      const current = document.activeElement

      // If nothing is focused (or body/root), focus the first element
      if (!current || current === document.body || current === root) {
        focusables[0].focus({ preventScroll: false })
        return
      }

      const target = findBest(current, key, focusables)
      if (target) {
        target.focus({ preventScroll: false })
        // Ensure the element is scrolled into view if inside a scrollable container
        target.scrollIntoView({ block: 'nearest', inline: 'nearest' })
      }
    }

    window.addEventListener('keydown', handleKeyDown)
    return () => window.removeEventListener('keydown', handleKeyDown)
  }, [])
}
