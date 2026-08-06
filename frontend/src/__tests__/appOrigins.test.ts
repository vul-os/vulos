import { describe, it, expect, beforeEach } from 'vitest'
import {
  iframeSandboxForURL,
  isDistinctOrigin,
  expectedFrameOrigin,
  gatewayAppId,
  appOriginURL,
  resolveAppFrameURL,
  appFrameURLFor,
  setOriginConfig,
  type OriginLocation,
} from '../core/AppOrigins'

// ORIGIN-01. The single most important assertion in this file: an app served from
// the SHELL's origin never receives allow-same-origin. That token, on a
// shell-origin frame, is a full shell compromise — it hands the app window.top,
// the shell's localStorage, its cookies, and the gateway's injected auth headers.

const SHELL = 'http://localhost:3000' // jsdom's default origin
const loc = (hostname: string, port = '', protocol = 'http:'): OriginLocation => ({ hostname, port, protocol })

describe('iframeSandboxForURL — the allow-same-origin gate', () => {
  it('NEVER grants allow-same-origin to a frame on the shell origin', () => {
    const shellOriginUrls = [
      '/app/clock/',
      '/app/calculator/',
      '/app/text-editor/',
      '/app/text-editor/',
      '/app/weather/',
      '/apps/browser/',
      '/api/ai-apps/ai-123/html',
      `${SHELL}/app/clock/`,
      // Tricky: an absolute URL that resolves back to the shell's own origin.
      'http://localhost:3000/app/clock/',
    ]
    for (const url of shellOriginUrls) {
      const sandbox = iframeSandboxForURL(url)
      expect(sandbox, `url ${url} must not get allow-same-origin`).not.toContain('allow-same-origin')
      expect(sandbox).toContain('allow-scripts')
    }
  })

  it('grants allow-same-origin only to a genuinely distinct origin', () => {
    const sandbox = iframeSandboxForURL('http://clock--default.box.example.com/')
    expect(sandbox).toContain('allow-same-origin')
    // On that frame "same origin" is the APP's origin, not the shell's.
    expect(isDistinctOrigin('http://clock--default.box.example.com/')).toBe(true)
  })

  it('withholds allow-same-origin from unresolvable / opaque URLs', () => {
    for (const url of ['', 'about:blank', 'data:text/html,<b>x', 'blob:http://localhost:3000/abc', 'not a url']) {
      expect(iframeSandboxForURL(url), `url ${url}`).not.toContain('allow-same-origin')
    }
  })

  it('is not fooled by a URL that merely CONTAINS the shell origin as a substring', () => {
    // The check is on the parsed ORIGIN, never on string containment — so a URL
    // that embeds our origin in its path/query is correctly seen as a foreign
    // origin, and a malformed one (an invalid port) is unresolvable and therefore
    // gets no allow-same-origin. Both outcomes are safe; neither is "it's us".
    expect(isDistinctOrigin('http://evil.com/?x=http://localhost:3000')).toBe(true)

    // "localhost:3000.evil.com" is not a valid URL at all (":3000.evil.com" is not
    // a port), so it fails to parse. The gate must FAIL CLOSED on that: no origin,
    // no allow-same-origin.
    expect(isDistinctOrigin('http://localhost:3000.evil.com/')).toBe(false)
    expect(iframeSandboxForURL('http://localhost:3000.evil.com/')).not.toContain('allow-same-origin')
  })
})

describe('expectedFrameOrigin — what the bridge will accept', () => {
  it('is the literal string "null" for a shell-origin (opaque) frame', () => {
    // A sandboxed frame without allow-same-origin reports origin 'null'.
    expect(expectedFrameOrigin('/app/clock/')).toBe('null')
  })

  it('is the app origin for a per-app-origin frame', () => {
    expect(expectedFrameOrigin('http://clock--default.box.example.com/'))
      .toBe('http://clock--default.box.example.com')
  })
})

describe('gatewayAppId', () => {
  it('extracts the id from gateway path-prefix urls only', () => {
    expect(gatewayAppId('/app/clock/')).toBe('clock')
    expect(gatewayAppId('/app/text-editor/deep/path')).toBe('text-editor')
    expect(gatewayAppId('/app/clock')).toBe('clock')
    expect(gatewayAppId('/apps/browser/')).toBeNull()
    expect(gatewayAppId('/api/ai-apps/x/html')).toBeNull()
    expect(gatewayAppId('https://elsewhere/app/clock/')).toBeNull()
    // Traversal / hostile ids never yield an id.
    expect(gatewayAppId('/app/../../etc/')).toBeNull()
    expect(gatewayAppId('/app/UPPER/')).toBeNull()
  })
})

describe('appOriginURL — refuses to mint an origin it cannot stand behind', () => {
  beforeEach(() => {
    setOriginConfig({ enabled: true, base_domain: 'box.example.com', profile: 'default' })
  })

  it('mints the app origin when the shell is served at the base domain', () => {
    expect(appOriginURL('clock', undefined, loc('box.example.com')))
      .toBe('http://clock--default.box.example.com')
    expect(appOriginURL('clock', undefined, loc('box.example.com', '8080')))
      .toBe('http://clock--default.box.example.com:8080')
  })

  it('returns null when the shell is browsed at some OTHER host', () => {
    // An alias / tunnel hostname / bare IP: the server would not route
    // {app}--default.{thatHost} to the gateway, and the app's frame-ancestors
    // would name the base-domain origin rather than the one actually framing it.
    // Minting an origin here produces a broken frame, so we decline and fall back
    // to the path prefix (opaque) instead.
    expect(appOriginURL('clock', undefined, loc('192.168.1.50', '8080'))).toBeNull()
    expect(appOriginURL('clock', undefined, loc('some-alias.internal'))).toBeNull()
    expect(appOriginURL('clock', undefined, loc('vulos.local'))).toBeNull()
  })

  it('returns null when the deployment cannot serve per-app origins at all', () => {
    setOriginConfig({ enabled: false, base_domain: '', profile: 'default' })
    expect(appOriginURL('clock', undefined, loc('box.example.com'))).toBeNull()
  })

  it('refuses hostile / ambiguous app ids', () => {
    expect(appOriginURL('a--b', undefined, loc('box.example.com'))).toBeNull()
    expect(appOriginURL('a.b', undefined, loc('box.example.com'))).toBeNull()
    expect(appOriginURL('-x', undefined, loc('box.example.com'))).toBeNull()
    expect(appOriginURL('UPPER', undefined, loc('box.example.com'))).toBeNull()
  })
})

describe('resolveAppFrameURL / appFrameURLFor', () => {
  it('rehomes gateway urls onto the app origin, preserving sub-paths', () => {
    const cfg = setOriginConfig({ enabled: true, base_domain: 'box.example.com', profile: 'default' })
    expect(resolveAppFrameURL('/app/clock/', cfg, loc('box.example.com')))
      .toBe('http://clock--default.box.example.com/')
    expect(resolveAppFrameURL('/app/text-editor/view/1', cfg, loc('box.example.com')))
      .toBe('http://text-editor--default.box.example.com/view/1')
    expect(appFrameURLFor('weather', cfg, loc('box.example.com')))
      .toBe('http://weather--default.box.example.com/')
  })

  it('leaves non-gateway urls alone', () => {
    const cfg = setOriginConfig({ enabled: true, base_domain: 'box.example.com', profile: 'default' })
    expect(resolveAppFrameURL('/api/ai-apps/ai-1/html', cfg, loc('box.example.com')))
      .toBe('/api/ai-apps/ai-1/html')
    expect(resolveAppFrameURL('/apps/browser/', cfg, loc('box.example.com')))
      .toBe('/apps/browser/')
  })

  it('falls back to the path prefix when origins are unavailable — apps keep working', () => {
    const cfg = setOriginConfig({ enabled: false, base_domain: '', profile: 'default' })
    expect(resolveAppFrameURL('/app/clock/', cfg)).toBe('/app/clock/')
    expect(appFrameURLFor('clock', cfg)).toBe('/app/clock/')
    // ...and that fallback frame gets NO allow-same-origin.
    expect(iframeSandboxForURL('/app/clock/')).not.toContain('allow-same-origin')
  })
})
