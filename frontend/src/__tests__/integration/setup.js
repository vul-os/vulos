// Vitest setup for the wave-28 integration suite (src/__tests__/integration/**).
//
// Starts the MSW mock backend before the suite, resets handlers between tests
// (so per-test `server.use(...)` overrides don't leak), and stops it at the end.
// `onUnhandledRequest: 'bypass'` keeps unrelated fetches quiet rather than
// throwing — the integration tests only assert on the endpoints they mock.

import '@testing-library/jest-dom'
import { afterAll, afterEach, beforeAll } from 'vitest'
import { server } from './msw/server.js'

beforeAll(() => server.listen({ onUnhandledRequest: 'bypass' }))
afterEach(() => server.resetHandlers())
afterAll(() => server.close())

// jsdom lacks a few browser APIs the shell touches during these flows.
if (!window.matchMedia) {
  window.matchMedia = (q) => ({
    matches: false, media: q, onchange: null,
    addListener() {}, removeListener() {},
    addEventListener() {}, removeEventListener() {}, dispatchEvent() { return false },
  })
}
if (!window.scrollTo) window.scrollTo = () => {}
if (!Element.prototype.scrollIntoView) Element.prototype.scrollIntoView = () => {}
if (!Element.prototype.scrollTo) Element.prototype.scrollTo = () => {}
