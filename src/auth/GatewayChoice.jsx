// GatewayChoice.jsx — GATEWAY-01: pick the control-plane ("gateway") this OS
// talks to. Managed users keep the one-click "Use Vulos Cloud" default; a
// self-hoster running their own OSS control plane (vulos-management) can expand
// "Use my own gateway", enter its URL, and connection-test it before continuing.
//
// The component is deliberately dumb + reusable: it owns the small choice/URL/
// test state and reports it up via onChange({ mode, url, tested }). Both the
// first-boot wizard and Settings embed it. Persisting the chosen gateway to the
// backend is the host's job (setGateway), done right before the cloud login /
// enroll runs so brokering targets it.

import { useState } from 'react'

// ─── API helpers (exported for tests + hosts) ─────────────────────────────────

// getGateway returns { url, source, default, env_url, configured } — the OS's
// current effective gateway.
export async function getGateway() {
  const res = await fetch('/api/gateway')
  return res.json().catch(() => ({}))
}

// checkGateway validates + reachability-tests a candidate URL WITHOUT persisting.
// Returns { ok, url } on success or { ok:false, error } with guidance.
export async function checkGateway(url) {
  const res = await fetch('/api/gateway/check', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ url }),
  })
  return res.json().catch(() => ({ ok: false, error: 'Could not reach the server.' }))
}

// setGateway persists the override (owner-gated / first-boot-open on the backend).
// Returns { ok, url, source } on success or { error } on failure.
export async function setGateway(url) {
  const res = await fetch('/api/gateway', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ url }),
  })
  const data = await res.json().catch(() => ({}))
  return { status: res.status, ok: res.ok, ...data }
}

// clearGateway reverts to env/default.
export async function clearGateway() {
  const res = await fetch('/api/gateway', { method: 'DELETE' })
  const data = await res.json().catch(() => ({}))
  return { status: res.status, ok: res.ok, ...data }
}

// normalizeHttps is a light client-side pre-check mirroring the backend: an
// https:// URL with a host. The backend is authoritative (it also SSRF-scopes
// and reachability-tests); this only gives instant feedback.
function looksLikeHttpsURL(v) {
  try {
    const u = new URL(v.trim())
    return u.protocol === 'https:' && !!u.host
  } catch {
    return false
  }
}

// ─── Component ─────────────────────────────────────────────────────────────────

// GatewayChoice renders the Vulos-Cloud-vs-own-gateway selector.
//
// Props:
//   value:    { mode: 'cloud'|'custom', url: string, tested: boolean }
//   onChange: (next) => void  — parent owns the state (lives in setup config)
//   idPrefix: string          — unique id/testid prefix when >1 instance mounts
export default function GatewayChoice({ value, onChange, idPrefix = 'gw' }) {
  const mode = value?.mode || 'cloud'
  const url = value?.url || ''
  const tested = !!value?.tested
  const [expanded, setExpanded] = useState(mode === 'custom')
  const [testing, setTesting] = useState(false)
  const [result, setResult] = useState(null) // { ok, error }

  const set = (patch) => onChange({ mode, url, tested, ...patch })

  const useCloud = () => {
    setExpanded(false)
    setResult(null)
    set({ mode: 'cloud', url: '', tested: false })
  }

  const useCustom = () => {
    setExpanded(true)
    set({ mode: 'custom', tested: false })
  }

  const onUrlChange = (v) => {
    setResult(null)
    // A URL edit invalidates any prior successful test.
    set({ mode: 'custom', url: v, tested: false })
  }

  const runTest = async () => {
    if (!looksLikeHttpsURL(url)) {
      setResult({ ok: false, error: 'Enter a full https:// URL, e.g. https://cp.example.org' })
      set({ tested: false })
      return
    }
    setTesting(true)
    setResult(null)
    try {
      const data = await checkGateway(url)
      if (data.ok) {
        setResult({ ok: true })
        set({ mode: 'custom', url: data.url || url, tested: true })
      } else {
        setResult({ ok: false, error: data.error || 'That gateway is not reachable.' })
        set({ tested: false })
      }
    } catch {
      setResult({ ok: false, error: 'Could not reach the server to run the test.' })
      set({ tested: false })
    } finally {
      setTesting(false)
    }
  }

  return (
    <div data-testid={`${idPrefix}-gateway-choice`}>
      <button
        type="button"
        onClick={() => setExpanded(v => !v)}
        aria-expanded={expanded}
        className="flex items-center gap-2 text-xs text-neutral-500 hover:text-neutral-300 transition-colors"
      >
        <svg
          viewBox="0 0 16 16"
          className={`w-3.5 h-3.5 transition-transform ${expanded ? 'rotate-90' : ''}`}
          fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round"
        >
          <path d="M6 4l4 4-4 4" />
        </svg>
        {mode === 'custom' && url
          ? <>Gateway: <span className="font-mono text-neutral-400 break-all">{url}</span></>
          : 'Control plane — using Vulos Cloud (advanced)'}
      </button>

      {expanded && (
        <div className="mt-3 bg-neutral-900/50 border border-neutral-800/50 rounded-xl p-4 space-y-3 animate-[fadeIn_0.15s_ease-out]">
          <p className="text-xs text-neutral-500 leading-relaxed">
            The gateway is the control plane your device authenticates against. Leave
            it on <strong className="text-neutral-300">Vulos Cloud</strong> unless you run
            your own <strong className="text-neutral-300">Vulos control plane</strong>.
          </p>

          {/* Choice radios */}
          <div className="grid grid-cols-1 sm:grid-cols-2 gap-2">
            <button
              type="button"
              onClick={useCloud}
              aria-pressed={mode === 'cloud'}
              className="text-left px-3 py-2.5 rounded-lg border-2 transition-all"
              style={mode === 'cloud'
                ? { borderColor: 'color-mix(in srgb, var(--accent) 55%, transparent)', background: 'color-mix(in srgb, var(--accent) 10%, transparent)' }
                : { borderColor: 'var(--border-default)', background: 'var(--bg-surface)' }}
            >
              <div className="text-sm font-medium" style={{ color: 'var(--text-primary)' }}>Use Vulos Cloud</div>
              <div className="text-[11px]" style={{ color: 'var(--text-muted)' }}>Default — recommended</div>
            </button>
            <button
              type="button"
              onClick={useCustom}
              aria-pressed={mode === 'custom'}
              className="text-left px-3 py-2.5 rounded-lg border-2 transition-all"
              style={mode === 'custom'
                ? { borderColor: 'color-mix(in srgb, var(--accent) 55%, transparent)', background: 'color-mix(in srgb, var(--accent) 10%, transparent)' }
                : { borderColor: 'var(--border-default)', background: 'var(--bg-surface)' }}
            >
              <div className="text-sm font-medium" style={{ color: 'var(--text-primary)' }}>Use my own gateway</div>
              <div className="text-[11px]" style={{ color: 'var(--text-muted)' }}>Self-hosted control plane</div>
            </button>
          </div>

          {mode === 'custom' && (
            <div className="space-y-2">
              <label htmlFor={`${idPrefix}-gateway-url`} className="block text-xs text-neutral-500">
                Gateway URL
              </label>
              <div className="flex gap-2">
                <input
                  id={`${idPrefix}-gateway-url`}
                  type="url"
                  inputMode="url"
                  value={url}
                  onChange={e => onUrlChange(e.target.value)}
                  placeholder="https://cp.example.org"
                  autoComplete="off"
                  spellCheck={false}
                  className="input text-sm py-2.5 font-mono flex-1"
                  aria-describedby={`${idPrefix}-gateway-help`}
                />
                <button
                  type="button"
                  onClick={runTest}
                  disabled={testing || !url}
                  className="btn-secondary text-xs px-3 whitespace-nowrap disabled:opacity-40 disabled:cursor-not-allowed"
                >
                  {testing ? 'Testing…' : 'Test'}
                </button>
              </div>
              <p id={`${idPrefix}-gateway-help`} className="text-[11px] text-neutral-600">
                Must be an https:// address. It is connection-tested before it can be used.
              </p>

              {result?.ok && (
                <div className="flex items-center gap-2 text-xs text-success" role="status">
                  <span className="inline-block w-2 h-2 rounded-full bg-success" aria-hidden="true" />
                  Gateway reachable — you can continue.
                </div>
              )}
              {result && !result.ok && (
                <div className="flex items-start gap-2 text-xs text-danger" role="alert">
                  <span className="inline-block w-2 h-2 rounded-full bg-danger mt-1" aria-hidden="true" />
                  <span>{result.error}</span>
                </div>
              )}
            </div>
          )}
        </div>
      )}
    </div>
  )
}
