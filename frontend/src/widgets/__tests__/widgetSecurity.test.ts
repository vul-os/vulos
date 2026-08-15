// widgetSecurity.test.ts — the boundary a malicious widget runs into.
//
// Every test here is an assertion about something a widget MUST NOT be able to
// do. They drive the bridge protocol directly rather than through an iframe: the
// properties are properties of `handleWidgetMessage`, and a test that needed a
// real frame would be slow enough that it would stop being run.
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import {
  BRIDGE_VERSION, bridgeClientScript, handleWidgetMessage, resetBridgeLimits,
  type BridgeHost,
} from '../host/bridge'
import { buildSandboxDocument, clearTokenCache, safeScript } from '../host/sandboxDoc'
import { hostAllowed, resetProxyProbe, setProxyAvailable, urlHost, widgetNet, probeProxy } from '../net'
import { widgetGet, widgetSet } from '../storage'
import { installMemoryStorage, uninstallMemoryStorage } from './memoryStorage'
import type { WidgetPermission } from '../types'

function host(over: Partial<BridgeHost> = {}): BridgeHost {
  return {
    instanceId: 'w1',
    granted: [],
    hosts: [],
    net: null,
    notify: null,
    openApp: null,
    setSetting: () => {},
    ...over,
  }
}

function msg(type: string, extra: Record<string, unknown> = {}) {
  return { v: BRIDGE_VERSION, type, id: 1, ...extra }
}

beforeEach(() => { installMemoryStorage(); resetBridgeLimits(); resetProxyProbe(); clearTokenCache() })
afterEach(() => { uninstallMemoryStorage(); vi.restoreAllMocks() })

describe('bridge — default deny', () => {
  const verbs: [string, Record<string, unknown>, WidgetPermission][] = [
    ['widget.storage.get', { key: 'k' }, 'storage'],
    ['widget.storage.set', { key: 'k', value: 'v' }, 'storage'],
    ['widget.storage.remove', { key: 'k' }, 'storage'],
    ['widget.storage.keys', {}, 'storage'],
    ['widget.net.get', { url: 'https://api.example.com/x' }, 'network'],
    ['widget.notify', { title: 'hi' }, 'notify'],
    ['widget.openApp', { appId: 'lilmail' }, 'launch'],
  ]

  it.each(verbs)('refuses %s without its permission', (type, extra) => {
    const replies: Record<string, unknown>[] = []
    const ok = handleWidgetMessage(host(), msg(type, extra), (o) => replies.push(o))
    expect(ok).toBe(false)
    expect(replies[0]?.error).toBe('denied')
  })

  it('refuses an unknown verb — the surface is closed, not extensible', () => {
    expect(handleWidgetMessage(host({ granted: ['storage', 'network', 'notify', 'launch'] }),
      msg('widget.evalPlease', { js: 'alert(1)' }), () => {})).toBe(false)
  })

  it('refuses a message of the wrong protocol version or shape', () => {
    expect(handleWidgetMessage(host(), { v: 99, type: 'widget.storage.keys' }, () => {})).toBe(false)
    expect(handleWidgetMessage(host(), 'not an object', () => {})).toBe(false)
    expect(handleWidgetMessage(host(), null, () => {})).toBe(false)
  })
})

describe('bridge — identity is never claimed', () => {
  it('scopes storage to the HOST\'s instance id, not one the widget names', () => {
    widgetSet('victim', 'secret', 'do not read me')
    const replies: Record<string, unknown>[] = []
    // The widget sends an instanceId. It is ignored: the host derives identity
    // from the frame the message arrived on.
    handleWidgetMessage(
      host({ granted: ['storage'] }),
      msg('widget.storage.get', { key: 'secret', instanceId: 'victim' }),
      (o) => replies.push(o),
    )
    expect(replies[0]?.value).toBeNull()

    handleWidgetMessage(host({ granted: ['storage'] }), msg('widget.storage.set', { key: 'k', value: 'mine' }), () => {})
    expect(widgetGet('w1', 'k')).toBe('mine')
    expect(widgetGet('victim', 'k')).toBeNull()
    expect(widgetGet('victim', 'secret')).toBe('do not read me')
  })
})

describe('bridge — network', () => {
  it('refuses a host outside the manifest allowlist even with the permission', () => {
    const net = { getJSON: vi.fn().mockResolvedValue({ ok: true, status: 200, data: {} }) }
    const replies: Record<string, unknown>[] = []
    const ok = handleWidgetMessage(
      host({ granted: ['network'], hosts: ['api.example.com'], net }),
      msg('widget.net.get', { url: 'https://evil.example.net/steal' }),
      (o) => replies.push(o),
    )
    expect(ok).toBe(false)
    expect(replies[0]?.error).toBe('blocked-host')
    expect(net.getJSON).not.toHaveBeenCalled()
  })

  it('refuses a non-http scheme', () => {
    const net = { getJSON: vi.fn() }
    for (const url of ['file:///etc/passwd', 'blob:https://x/y', 'javascript:alert(1)', 'data:text/html,x']) {
      const replies: Record<string, unknown>[] = []
      handleWidgetMessage(
        host({ granted: ['network'], hosts: ['api.example.com'], net }),
        msg('widget.net.get', { url }), (o) => replies.push(o),
      )
      expect(replies[0]?.error, url).toBe('blocked-host')
    }
    expect(net.getJSON).not.toHaveBeenCalled()
  })

  it('rate-limits repeated requests instead of queueing them', () => {
    const net = { getJSON: vi.fn().mockResolvedValue({ ok: true, status: 200, data: {} }) }
    const h = host({ granted: ['network'], hosts: ['api.example.com'], net })
    const url = 'https://api.example.com/q'
    expect(handleWidgetMessage(h, msg('widget.net.get', { url }), () => {}, 1_000)).toBe(true)
    const replies: Record<string, unknown>[] = []
    expect(handleWidgetMessage(h, msg('widget.net.get', { url }), (o) => replies.push(o), 1_100)).toBe(false)
    expect(replies[0]?.error).toBe('rate-limited')
    // …and allowed again once the window passes.
    expect(handleWidgetMessage(h, msg('widget.net.get', { url }), () => {}, 5_000)).toBe(true)
    expect(net.getJSON).toHaveBeenCalledTimes(2)
  })
})

describe('bridge — notify and launch', () => {
  it('rate-limits notifications', () => {
    const notify = vi.fn()
    const h = host({ granted: ['notify'], notify })
    expect(handleWidgetMessage(h, msg('widget.notify', { title: 'a' }), () => {}, 0)).toBe(true)
    expect(handleWidgetMessage(h, msg('widget.notify', { title: 'b' }), () => {}, 500)).toBe(false)
    expect(handleWidgetMessage(h, msg('widget.notify', { title: 'c' }), () => {}, 60_000)).toBe(true)
    expect(notify).toHaveBeenCalledTimes(2)
  })

  it('truncates notification text rather than letting a widget flood the centre', () => {
    const notify = vi.fn()
    handleWidgetMessage(host({ granted: ['notify'], notify }),
      msg('widget.notify', { title: 'x'.repeat(500), body: 'y'.repeat(5000) }), () => {})
    expect((notify.mock.calls[0][0] as string).length).toBe(120)
    expect((notify.mock.calls[0][1] as string).length).toBe(400)
  })

  it('accepts only an app ID, never a URL', () => {
    // A widget must not be able to navigate the desktop anywhere; it can only
    // name something the user already has.
    const openApp = vi.fn()
    const h = host({ granted: ['launch'], openApp })
    for (const appId of ['https://evil.example.net', '../admin', 'Has Caps', '', 'a b']) {
      const replies: Record<string, unknown>[] = []
      handleWidgetMessage(h, msg('widget.openApp', { appId }), (o) => replies.push(o))
      expect(replies[0]?.error, appId).toBe('bad-app')
    }
    expect(openApp).not.toHaveBeenCalled()
    handleWidgetMessage(h, msg('widget.openApp', { appId: 'lilmail' }), () => {})
    expect(openApp).toHaveBeenCalledWith('lilmail')
  })
})

describe('bridge client script', () => {
  it('addresses the handshake to the shell\'s exact origin, never "*"', () => {
    // If the frame is ever hosted somewhere other than the shell, the browser
    // drops the hello and the bridge simply never forms.
    const js = bridgeClientScript('https://box.example.org')
    expect(js).toContain('"https://box.example.org"')
    expect(js).not.toMatch(/postMessage\([^)]*,\s*['"]\*['"]/)
  })
})

describe('the sandbox document', () => {
  it('injects the bridge client and no stylesheet link', () => {
    const doc = buildSandboxDocument('<p>hi</p>', 'https://box.example.org', ':root{--accent:#00f;}')
    expect(doc).toContain('vulosWidget')
    expect(doc).toContain('--accent:#00f')
    expect(doc).toContain('<p>hi</p>')
    // A <link> would be a fetch from an opaque origin, and would let the frame
    // observe the shell's asset timing. The palette is injected as values.
    expect(doc).not.toContain('<link')
  })

  it('a widget source containing </script> cannot break out of the injected block', () => {
    // safeScript guards the block the HOST writes. A widget's own body is
    // markup, so its </script> closes its own tag — what must not happen is the
    // BRIDGE block ending early and the rest being parsed as markup.
    expect(safeScript('var s = "</script><img onerror=x>"')).not.toContain('</script>')
    expect(safeScript('a</SCRIPT>b')).toBe('a<\\/SCRIPT>b')
  })

  it('never grants the frame same-origin — checked on the source of truth', async () => {
    // The literal sandbox attribute lives in SandboxFrame.tsx. Reading the file
    // is the only way to assert it without a browser, and it is worth asserting:
    // adding `allow-same-origin` is a one-word change that silently hands every
    // sandboxed widget the shell's storage, cookies and DOM.
    const { readFileSync } = await import('node:fs')
    // Path relative to the repo root, matching src/shell/zLayers.test.ts — this
    // suite runs with the frontend/ directory as cwd.
    const src = readFileSync('src/widgets/host/SandboxFrame.tsx', 'utf8')
    expect(src).toContain('sandbox="allow-scripts"')
    // Matched on the ATTRIBUTE, not the word: the file discusses
    // `allow-same-origin` at length in its header, and a check that failed on
    // the prose would have to be deleted the first time someone documented the
    // rule — which is the opposite of what it is for. Every sandbox= value in
    // the file must be exactly "allow-scripts".
    const sandboxValues = [...src.matchAll(/sandbox=["']([^"']*)["']/g)].map((m) => m[1])
    expect(sandboxValues.length).toBeGreaterThan(0)
    for (const v of sandboxValues) expect(v).toBe('allow-scripts')
  })

  it('checks BOTH frame identity and origin before accepting a handshake', async () => {
    const { readFileSync } = await import('node:fs')
    const src = readFileSync('src/widgets/host/SandboxFrame.tsx', 'utf8')
    expect(src).toContain('e.source !== frame.contentWindow')
    expect(src).toContain("e.origin !== 'null'")
  })
})

describe('net — the box makes the request, or nobody does', () => {
  it('parses and rejects URLs', () => {
    expect(urlHost('https://API.Example.com/x')).toBe('api.example.com')
    expect(urlHost('http://api.example.com')).toBe('api.example.com')
    expect(urlHost('file:///etc/passwd')).toBeNull()
    expect(urlHost('not a url')).toBeNull()
  })

  it('matches hosts EXACTLY — no suffix, no wildcard', () => {
    expect(hostAllowed('https://api.example.com/x', ['api.example.com'])).toBe(true)
    // The classic subdomain-confusion pair.
    expect(hostAllowed('https://evil-api.example.com/x', ['api.example.com'])).toBe(false)
    expect(hostAllowed('https://api.example.com.evil.net/x', ['api.example.com'])).toBe(false)
    expect(hostAllowed('https://sub.api.example.com/x', ['api.example.com'])).toBe(false)
  })

  it('is NULL unless granted, allowlisted AND the box brokers requests', () => {
    setProxyAvailable(true)
    expect(widgetNet(['api.example.com'], { granted: false })).toBeNull()
    expect(widgetNet([], { granted: true })).toBeNull()
    setProxyAvailable(false)
    expect(widgetNet(['api.example.com'], { granted: true })).toBeNull()
    setProxyAvailable(true)
    expect(widgetNet(['api.example.com'], { granted: true })).not.toBeNull()
  })

  it('posts to the BOX, never to the third party', async () => {
    setProxyAvailable(true)
    const fetchImpl = vi.fn().mockResolvedValue({ ok: true, status: 200, json: async () => ({ price: 1 }) })
    const net = widgetNet(['api.example.com'], { granted: true, fetchImpl: fetchImpl as unknown as typeof fetch })!
    const r = await net.getJSON('https://api.example.com/quote?s=AAPL')
    expect(r.ok).toBe(true)
    expect(r.data).toEqual({ price: 1 })
    // The ONLY URL this module ever fetches is same-origin.
    const called = fetchImpl.mock.calls[0][0] as string
    expect(called).toBe('/api/widgets/fetch')
    expect(called.startsWith('http')).toBe(false)
  })

  it('refuses a disallowed host without calling fetch at all', async () => {
    setProxyAvailable(true)
    const fetchImpl = vi.fn()
    const net = widgetNet(['api.example.com'], { granted: true, fetchImpl: fetchImpl as unknown as typeof fetch })!
    expect(await net.getJSON('https://evil.example.net/x')).toMatchObject({ ok: false, error: 'blocked-host' })
    expect(fetchImpl).not.toHaveBeenCalled()
  })

  it('treats every ambiguous probe answer as NO', async () => {
    // Guessing "yes" costs a request that leaves the box. Guessing "no" costs a
    // widget showing its offline state.
    for (const impl of [
      () => Promise.resolve({ ok: false, status: 404, json: async () => ({}) }),
      () => Promise.resolve({ ok: true, status: 200, json: async () => ({ enabled: 'yes' }) }),
      () => Promise.resolve({ ok: true, status: 200, json: async () => { throw new Error('bad body') } }),
      () => Promise.reject(new Error('offline')),
    ]) {
      resetProxyProbe()
      expect(await probeProxy(impl as unknown as typeof fetch)).toBe(false)
    }
    resetProxyProbe()
    expect(await probeProxy((() => Promise.resolve({
      ok: true, status: 200, json: async () => ({ enabled: true }),
    })) as unknown as typeof fetch)).toBe(true)
  })

  it('the shipped source contains no third-party URL', async () => {
    // The strongest form of the sovereignty claim, checked mechanically: no file
    // in the widget tree names an external origin.
    const { readFileSync, readdirSync, statSync } = await import('node:fs')
    const { join } = await import('node:path')
    const root = 'src/widgets'
    const offenders: string[] = []
    const walk = (dir: string) => {
      for (const name of readdirSync(dir)) {
        const p = join(dir, name)
        if (statSync(p).isDirectory()) { walk(p); continue }
        if (!/\.(ts|tsx|css)$/.test(name)) continue
        if (/\.test\.tsx?$/.test(name)) continue // this file names example hosts on purpose
        const text = readFileSync(p, 'utf8')
        for (const m of text.matchAll(/https?:\/\/[a-z0-9.-]+/gi)) {
          const url = m[0]
          // Documentation links to the spec/standards are not requests.
          if (/react\.dev|developer\.mozilla\.org|www\.w3\.org/.test(url)) continue
          offenders.push(`${name}: ${url}`)
        }
      }
    }
    walk(root)
    expect(offenders).toEqual([])
  })
})
