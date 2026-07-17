import { useState, useEffect, useCallback } from 'react'
import { getGateway, checkGateway, setGateway, clearGateway } from '../../auth/GatewayChoice'

// ---------------------------------------------------------------------------
// GATEWAY-01 — Control plane ("gateway") settings (owner-facing).
//
// Shows the control plane this OS authenticates against, with a live health
// indicator, and lets the owner repoint it at their own OSS control plane
// (vulos-management). Same validation + reachability test as first-boot. Because
// identity/enrollment are gateway-scoped, changing the gateway is warned to
// require re-linking/re-enrolling the Vulos account.
//
// Managed users never need this — the default is Vulos Cloud and everything
// keeps working untouched.
// ---------------------------------------------------------------------------

// Mirrors the shared Settings masthead (see core/Settings.jsx Section) so the
// Control Plane panel reads identically to every other settings panel.
function Section({ title, children }) {
  return (
    <div>
      <header className="mb-5 pb-4 border-b border-[var(--border-default)]">
        <h2 className="text-xl font-semibold tracking-tight text-[var(--text-primary)]">{title}</h2>
      </header>
      {children}
    </div>
  )
}

const SOURCE_LABEL = {
  default: 'Vulos Cloud (default)',
  env: 'Environment variable',
  configured: 'Custom gateway (configured)',
}

function looksLikeHttpsURL(v) {
  try {
    const u = new URL(v.trim())
    return u.protocol === 'https:' && !!u.host
  } catch {
    return false
  }
}

export default function GatewayPanel() {
  const [info, setInfo] = useState(null)      // { url, source, default, configured }
  const [loadErr, setLoadErr] = useState(false)
  const [health, setHealth] = useState('checking') // 'checking' | 'ok' | 'down'

  const [editing, setEditing] = useState(false)
  const [url, setUrl] = useState('')
  const [testing, setTesting] = useState(false)
  const [tested, setTested] = useState(false)
  const [saving, setSaving] = useState(false)
  const [msg, setMsg] = useState(null)        // { ok, text }

  // Probe the CURRENT gateway for the live health dot.
  const probeHealth = useCallback((target) => {
    if (!target) { setHealth('down'); return }
    setHealth('checking')
    checkGateway(target)
      .then(r => setHealth(r.ok ? 'ok' : 'down'))
      .catch(() => setHealth('down'))
  }, [])

  const load = useCallback(() => {
    getGateway()
      .then(data => {
        if (!data || !data.url) { setLoadErr(true); return }
        setInfo(data)
        setLoadErr(false)
        probeHealth(data.url)
      })
      .catch(() => setLoadErr(true))
  }, [probeHealth])

  useEffect(() => { load() }, [load])

  const startEdit = () => {
    setUrl(info?.configured || '')
    setTested(false)
    setMsg(null)
    setEditing(true)
  }

  const cancelEdit = () => {
    setEditing(false)
    setMsg(null)
  }

  const runTest = async () => {
    if (!looksLikeHttpsURL(url)) {
      setMsg({ ok: false, text: 'Enter a full https:// URL, e.g. https://cp.example.org' })
      setTested(false)
      return
    }
    setTesting(true)
    setMsg(null)
    try {
      const r = await checkGateway(url)
      if (r.ok) {
        setTested(true)
        setUrl(r.url || url)
        setMsg({ ok: true, text: 'Gateway reachable.' })
      } else {
        setTested(false)
        setMsg({ ok: false, text: r.error || 'That gateway is not reachable.' })
      }
    } catch {
      setTested(false)
      setMsg({ ok: false, text: 'Could not reach the server to run the test.' })
    } finally {
      setTesting(false)
    }
  }

  const save = async () => {
    if (!tested) {
      setMsg({ ok: false, text: 'Test the connection before saving.' })
      return
    }
    setSaving(true)
    try {
      const r = await setGateway(url)
      if (r.ok) {
        setEditing(false)
        setMsg(null)
        load()
      } else if (r.status === 403) {
        setMsg({ ok: false, text: 'Only the box owner can change the gateway.' })
      } else {
        setMsg({ ok: false, text: r.error || 'Could not save the gateway.' })
      }
    } catch {
      setMsg({ ok: false, text: 'Could not reach the server to save.' })
    } finally {
      setSaving(false)
    }
  }

  const resetToCloud = async () => {
    setSaving(true)
    try {
      const r = await clearGateway()
      if (r.ok) {
        setEditing(false)
        setMsg(null)
        load()
      } else if (r.status === 403) {
        setMsg({ ok: false, text: 'Only the box owner can change the gateway.' })
      } else {
        setMsg({ ok: false, text: r.error || 'Could not reset the gateway.' })
      }
    } catch {
      setMsg({ ok: false, text: 'Could not reach the server to reset.' })
    } finally {
      setSaving(false)
    }
  }

  const healthTone = health === 'ok' ? 'bg-green-400' : health === 'down' ? 'bg-red-400' : 'bg-amber-400'
  const healthText = health === 'ok' ? 'Reachable' : health === 'down' ? 'Unreachable' : 'Checking…'

  return (
    <Section title="Control Plane">
      <p className="text-xs text-neutral-600 mb-5 leading-relaxed">
        The gateway is the control plane your device authenticates against for cloud
        sign-in, account enrollment, and instance routing. Managed devices use
        <strong className="text-neutral-400"> Vulos Cloud</strong>. Point it at your own
        <span className="font-mono"> vulos-management</span> control plane if you self-host.
      </p>

      {loadErr && (
        <div className="rounded-xl border border-neutral-700/50 bg-neutral-800/40 text-neutral-300 px-4 py-3 mb-5 text-sm" role="alert">
          Could not load the gateway configuration.
        </div>
      )}

      {info && (
        <div className="space-y-px rounded-xl overflow-hidden border border-neutral-800/50 mb-5">
          <div className="flex items-center justify-between px-4 py-3 bg-neutral-900/40">
            <span className="text-xs text-neutral-500">Current gateway</span>
            <span className="text-sm text-neutral-200 font-mono break-all text-right max-w-[65%]">{info.url}</span>
          </div>
          <div className="flex items-center justify-between px-4 py-3 bg-neutral-900/40">
            <span className="text-xs text-neutral-500">Source</span>
            <span className="text-sm text-neutral-300">{SOURCE_LABEL[info.source] || info.source}</span>
          </div>
          <div className="flex items-center justify-between px-4 py-3 bg-neutral-900/40">
            <span className="text-xs text-neutral-500">Status</span>
            <span className="flex items-center gap-2 text-sm text-neutral-300">
              <span className={`inline-block w-2 h-2 rounded-full ${healthTone}`} aria-hidden="true" />
              {healthText}
            </span>
          </div>
        </div>
      )}

      {!editing && (
        <div className="flex flex-wrap gap-2">
          <button onClick={startEdit} className="btn-secondary text-sm">Change gateway</button>
          {info?.source === 'configured' && (
            <button onClick={resetToCloud} disabled={saving} className="btn-secondary text-sm disabled:opacity-40">
              {saving ? 'Resetting…' : 'Reset to Vulos Cloud'}
            </button>
          )}
        </div>
      )}

      {editing && (
        <div className="rounded-xl border border-neutral-800/60 bg-neutral-900/50 p-4 space-y-3">
          {/* Warning: gateway is identity-scoped. */}
          <div className="flex items-start gap-2 bg-warning-soft border border-warning-soft rounded-xl px-4 py-3">
            <svg viewBox="0 0 20 20" fill="currentColor" className="w-4 h-4 text-warning mt-0.5 shrink-0">
              <path fillRule="evenodd" d="M8.485 2.495c.673-1.167 2.357-1.167 3.03 0l6.28 10.875c.673 1.167-.17 2.625-1.516 2.625H3.72c-1.347 0-2.189-1.458-1.515-2.625L8.485 2.495zM10 5a.75.75 0 01.75.75v3.5a.75.75 0 01-1.5 0v-3.5A.75.75 0 0110 5zm0 9a1 1 0 100-2 1 1 0 000 2z" clipRule="evenodd" />
            </svg>
            <p className="text-xs text-warning leading-relaxed">
              Changing the gateway repoints all cloud brokering. Your Vulos account link and
              device enrollment are scoped to the current gateway, so you may need to
              re-link / re-enroll your account after switching.
            </p>
          </div>

          <label htmlFor="gw-settings-url" className="block text-xs text-neutral-500">Gateway URL</label>
          <div className="flex gap-2">
            <input
              id="gw-settings-url"
              type="url"
              inputMode="url"
              value={url}
              onChange={e => { setUrl(e.target.value); setTested(false); setMsg(null) }}
              placeholder="https://cp.example.org"
              autoComplete="off"
              spellCheck={false}
              className="input text-sm py-2.5 font-mono flex-1"
            />
            <button
              onClick={runTest}
              disabled={testing || !url}
              className="btn-secondary text-xs px-3 whitespace-nowrap disabled:opacity-40 disabled:cursor-not-allowed"
            >
              {testing ? 'Testing…' : 'Test'}
            </button>
          </div>

          {msg && (
            <div className={`flex items-start gap-2 text-xs ${msg.ok ? 'text-emerald-400' : 'text-danger'}`} role={msg.ok ? 'status' : 'alert'}>
              <span className={`inline-block w-2 h-2 rounded-full mt-1 ${msg.ok ? 'bg-emerald-400' : 'bg-red-400'}`} aria-hidden="true" />
              <span>{msg.text}</span>
            </div>
          )}

          <div className="flex flex-wrap gap-2 pt-1">
            <button onClick={save} disabled={saving || !tested} className="btn-primary text-sm disabled:opacity-40 disabled:cursor-not-allowed">
              {saving ? 'Saving…' : 'Save gateway'}
            </button>
            <button onClick={cancelEdit} disabled={saving} className="btn-secondary text-sm">Cancel</button>
          </div>
        </div>
      )}
    </Section>
  )
}
