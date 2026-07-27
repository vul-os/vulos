import { useState, useEffect, useCallback } from 'react'
import { requireStepUp } from '../../lib/stepup'

// ---------------------------------------------------------------------------
// RelayPanel — Settings -> Network -> Relay & Reachability (box-owner only
// to CHANGE; any signed-in user can view).
//
// RELAY-01: lets the owner pick the box's relay/TURN/rendezvous provider.
// Vulos's OWN built-in reverse tunnel (docs/REACH.md) is the DEFAULT and
// needs nothing external — a relay is `vulos relay serve`, the same binary in
// a role. Ephor is a supported alternative. You can also bring your own
// STUN/TURN, a libp2p Circuit Relay v2 peer, a Tailscale/Headscale/Nebula
// WireGuard mesh, or turn the relay tunnel off entirely if this box has a
// static IP / port-forward.
//
// Reachability is really three independent concerns — app-media ICE (what
// makes Meet/Talk calls connect), box HTTP ingress (reaching this box from
// outside your NAT), and box<->box rendezvous/discovery. Picking libp2p or
// WireGuard here changes ingress/rendezvous ONLY — Meet/Talk calls keep
// using Ephor's ICE (public STUN + this box's own TURN, if configured)
// automatically; this panel never lets you accidentally break call audio
// while trying to change how the box itself is reached.
//
// Endpoints (backend/cmd/server/routes_relayconfig.go):
//   GET  /api/relayconfig            — { config, effective }
//   POST /api/relayconfig            — (step-up) { provider, turn?, libp2p?, wireguard?, force? }
//   POST /api/relayconfig/reset      — (step-up) revert to the default
//   POST /api/relayconfig/test       — TCP-probe the active provider's endpoint
// ---------------------------------------------------------------------------

function Section({ title, desc, children }) {
  return (
    <div>
      <header className="mb-5 pb-4 border-b border-[var(--border-default)]">
        <h2 className="text-xl font-semibold tracking-tight text-[var(--text-primary)]">{title}</h2>
        {desc && <p className="mt-1 text-sm text-[var(--text-tertiary)] leading-relaxed">{desc}</p>}
      </header>
      {children}
    </div>
  )
}

function Field({ label, hint, children }) {
  return (
    <div className="mb-3">
      <label className="block text-xs text-[var(--text-muted)] mb-1">{label}</label>
      {children}
      {hint && <p className="text-[11px] text-[var(--text-faint)] mt-1">{hint}</p>}
    </div>
  )
}

async function jsonFetch(url, opts) {
  const r = await fetch(url, { credentials: 'same-origin', ...opts })
  const d = await r.json().catch(() => ({}))
  if (!r.ok) throw new Error(d.error || `HTTP ${r.status}`)
  return d
}

const PROVIDERS = [
  {
    value: 'vulos',
    label: 'Vulos relay (default)',
    blurb:
      "Vulos's own built-in reverse tunnel: this box dials out to your relay and is reachable from anywhere, with no inbound ports. Plus public STUN and this box's TURN for calls. Configure relay endpoints with VULOS_RELAY_ENDPOINTS — list two under different operators and the box holds a tunnel to both at once.",
  },
  {
    value: 'ephor',
    label: 'Ephor relay',
    blurb:
      'Use an Ephor relay (github.com/vul-os/ephor) instead of the built-in one. A supported alternative, not a dependency — it speaks the same rendezvous contract, so switching is a config change.',
  },
  { value: 'turn', label: 'Bring your own STUN/TURN', blurb: 'Your own coturn (or any STUN/TURN provider) for call media. Ingress + rendezvous stay on the built-in provider.' },
  { value: 'libp2p', label: 'libp2p Circuit Relay v2', blurb: 'Your own relay peer(s) for box reachability + discovery. Call media (ICE) is unaffected.' },
  { value: 'wireguard', label: 'WireGuard mesh (Tailscale/Headscale/Nebula)', blurb: 'Reach this box over your own mesh instead of the relay tunnel. Call media (ICE) is unaffected.' },
  { value: 'none', label: 'None (static IP / port-forward)', blurb: 'No relay tunnel — you manage your own inbound path. Call media (ICE) is unaffected.' },
]

function emptyTurnServer() {
  return { urls: '', username: '', credential: '' }
}

export default function RelayPanel() {
  const [config, setConfig] = useState(null)       // safe view from GET (config)
  const [effective, setEffective] = useState(null) // resolved snapshot from GET (effective)
  const [loadError, setLoadError] = useState('')

  const [provider, setProvider] = useState('ephor')
  const [turnServers, setTurnServers] = useState([emptyTurnServer()])
  const [libp2pPeers, setLibp2pPeers] = useState('')
  const [wgEndpoint, setWgEndpoint] = useState('')
  const [wgNetwork, setWgNetwork] = useState('')

  const [saving, setSaving] = useState(false)
  const [saveMsg, setSaveMsg] = useState(null) // { ok, text }
  const [testing, setTesting] = useState(false)
  const [testResult, setTestResult] = useState(null)

  const load = useCallback(() => {
    setLoadError('')
    jsonFetch('/api/relayconfig')
      .then(d => {
        setConfig(d.config)
        setEffective(d.effective)
        setProvider(d.config.provider)
        const servers = d.config.turn?.ice_servers || []
        setTurnServers(servers.length
          ? servers.map(s => ({ urls: (s.urls || []).join(', '), username: s.username || '', credential: '' }))
          : [emptyTurnServer()])
        setLibp2pPeers((d.config.libp2p?.relay_peers || []).join('\n'))
        setWgEndpoint(d.config.wireguard?.endpoint || '')
        setWgNetwork(d.config.wireguard?.network || '')
      })
      .catch(e => setLoadError(e.message || 'Could not load relay configuration.'))
  }, [])

  useEffect(() => { load() }, [load])

  const addTurnServer = () => setTurnServers(s => [...s, emptyTurnServer()])
  const removeTurnServer = (i) => setTurnServers(s => s.filter((_, idx) => idx !== i))
  const updateTurnServer = (i, field, value) =>
    setTurnServers(s => s.map((row, idx) => idx === i ? { ...row, [field]: value } : row))

  const buildPayload = (force) => {
    const payload = { provider, force: !!force }
    if (provider === 'turn') {
      payload.turn = {
        ice_servers: turnServers
          .filter(s => s.urls.trim())
          .map(s => ({
            urls: s.urls.split(',').map(u => u.trim()).filter(Boolean),
            username: s.username.trim() || undefined,
            credential: s.credential.trim() || undefined,
          })),
      }
    }
    if (provider === 'libp2p') {
      payload.libp2p = {
        relay_peers: libp2pPeers.split('\n').map(p => p.trim()).filter(Boolean),
      }
    }
    if (provider === 'wireguard') {
      payload.wireguard = { endpoint: wgEndpoint.trim(), network: wgNetwork.trim() || undefined }
    }
    return payload
  }

  const save = async (force) => {
    setSaving(true)
    setSaveMsg(null)
    try {
      await requireStepUp()
      await jsonFetch('/api/relayconfig', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(buildPayload(force)) })
      setSaveMsg({ ok: true, text: 'Relay configuration saved.' })
      load()
    } catch (e) {
      if (e?.code === 'CANCELLED') { setSaving(false); return }
      const text = e.message || 'Could not save relay configuration.'
      // The backend suggests retrying with force=true when a health probe
      // (turn/wireguard only) fails but the owner knows the endpoint is fine
      // (e.g. only reachable from clients, or DNS not propagated yet).
      const canForce = !force && /force=true/.test(text)
      setSaveMsg({ ok: false, text, canForce })
    } finally {
      setSaving(false)
    }
  }

  const resetToEphor = async () => {
    setSaving(true)
    setSaveMsg(null)
    try {
      await requireStepUp()
      await jsonFetch('/api/relayconfig/reset', { method: 'POST' })
      setSaveMsg({ ok: true, text: 'Reverted to the ephor default.' })
      load()
    } catch (e) {
      if (e?.code !== 'CANCELLED') setSaveMsg({ ok: false, text: e.message || 'Could not reset.' })
    } finally {
      setSaving(false)
    }
  }

  const runTest = async () => {
    setTesting(true)
    setTestResult(null)
    try {
      const r = await jsonFetch('/api/relayconfig/test', { method: 'POST' })
      setTestResult(r)
    } catch (e) {
      setTestResult({ success: false, detail: e.message })
    } finally {
      setTesting(false)
    }
  }

  const activeMeta = PROVIDERS.find(p => p.value === config?.provider)

  return (
    <Section
      title="Relay & Reachability"
      desc="Ephor is Vulos's default relay/TURN path — it just works, nothing to configure. You can bring your own instead: BYO STUN/TURN, a libp2p relay peer, a WireGuard mesh, or none at all if this box has a static IP."
    >
      {loadError && (
        <div className="rounded-xl border border-[var(--border-strong)] bg-[var(--bg-elevated)] text-[var(--text-secondary)] px-4 py-3 mb-5 text-sm" role="alert">
          {loadError}
        </div>
      )}

      {config && (
        <div className="space-y-px rounded-xl overflow-hidden border border-[var(--border-default)] mb-5">
          <div className="flex items-center justify-between px-4 py-3 bg-[var(--bg-surface)]">
            <span className="text-xs text-[var(--text-muted)]">Active provider</span>
            <span className="text-sm text-[var(--text-primary)]">{activeMeta?.label || config.provider}</span>
          </div>
          <div className="flex items-center justify-between px-4 py-3 bg-[var(--bg-surface)]">
            <span className="text-xs text-[var(--text-muted)]">Box ingress</span>
            <span className="text-sm text-[var(--text-secondary)] text-right">{effective?.ingress?.mode}{effective?.ingress?.detail ? ` — ${effective.ingress.detail}` : ''}</span>
          </div>
          <div className="flex items-center justify-between px-4 py-3 bg-[var(--bg-surface)]">
            <span className="text-xs text-[var(--text-muted)]">Call media (ICE) servers</span>
            <span className="text-sm text-[var(--text-secondary)]">{effective?.ice_servers?.length ?? 0} configured</span>
          </div>
        </div>
      )}

      <div className="space-y-2 mb-5">
        {PROVIDERS.map(p => (
          <label
            key={p.value}
            className={`flex items-start gap-3 rounded-xl border px-4 py-3 cursor-pointer transition-colors ${
              provider === p.value
                ? 'border-[var(--accent)] bg-[var(--bg-elevated)]'
                : 'border-[var(--border-default)] bg-[var(--bg-surface)] hover:border-[var(--border-strong)]'
            }`}
          >
            <input
              type="radio"
              name="relay-provider"
              value={p.value}
              checked={provider === p.value}
              onChange={() => { setProvider(p.value); setSaveMsg(null) }}
              className="mt-1"
            />
            <span>
              <span className="block text-sm font-medium text-[var(--text-primary)]">{p.label}</span>
              <span className="block text-xs text-[var(--text-faint)] mt-0.5 leading-relaxed">{p.blurb}</span>
            </span>
          </label>
        ))}
      </div>

      {provider === 'turn' && (
        <div className="rounded-xl border border-[var(--border-default)] bg-[var(--bg-surface)] p-4 mb-5">
          <p className="text-xs text-[var(--text-faint)] mb-3">
            One or more STUN/TURN servers (comma-separate multiple URLs per row, e.g.
            <code className="mx-1 text-[11px]">turn:relay.example.org:3478?transport=udp, turn:relay.example.org:3478?transport=tcp</code>).
            The credential is write-only — it is never shown again once saved.
          </p>
          {turnServers.map((s, i) => (
            <div key={i} className="grid grid-cols-1 sm:grid-cols-[1fr_auto] gap-2 mb-3 items-start">
              <div className="grid grid-cols-1 sm:grid-cols-3 gap-2">
                <input className="input text-sm" placeholder="turn:host:3478" value={s.urls} onChange={e => updateTurnServer(i, 'urls', e.target.value)} />
                <input className="input text-sm" placeholder="username (optional)" value={s.username} onChange={e => updateTurnServer(i, 'username', e.target.value)} />
                <input className="input text-sm" type="password" placeholder="credential (optional)" value={s.credential} onChange={e => updateTurnServer(i, 'credential', e.target.value)} />
              </div>
              {turnServers.length > 1 && (
                <button onClick={() => removeTurnServer(i)} className="btn-secondary text-xs px-2 h-fit">Remove</button>
              )}
            </div>
          ))}
          <button onClick={addTurnServer} className="btn-secondary text-xs">+ Add another server</button>
        </div>
      )}

      {provider === 'libp2p' && (
        <div className="rounded-xl border border-[var(--border-default)] bg-[var(--bg-surface)] p-4 mb-5">
          <p className="text-xs text-[var(--text-muted)] mb-3">
            Today this selection is reported on the federation status page but not yet actuated —
            saving it does not re-route real box ingress or rendezvous traffic through libp2p.
            Only call-media (ICE) provider selection is live end-to-end.
          </p>
          <Field label="Relay peer multiaddrs (one per line)" hint="Each must include a /p2p/<peer-id> component, e.g. /dns4/relay.example.org/tcp/4001/p2p/12D3KooW...">
            <textarea
              className="input text-sm font-mono"
              rows={4}
              value={libp2pPeers}
              onChange={e => setLibp2pPeers(e.target.value)}
              placeholder={'/dns4/relay.example.org/tcp/4001/p2p/12D3KooW...'}
              spellCheck={false}
            />
          </Field>
        </div>
      )}

      {provider === 'wireguard' && (
        <div className="rounded-xl border border-[var(--border-default)] bg-[var(--bg-surface)] p-4 mb-5">
          <p className="text-xs text-[var(--text-muted)] mb-3">
            Today this selection is reported on the federation status page but not yet actuated —
            saving it does not re-route real box ingress through your mesh. Only call-media (ICE)
            provider selection is live end-to-end.
          </p>
          <Field label="Coordinator endpoint" hint="Your Tailscale/Headscale/Nebula coordinator — host:port or an https URL. No keys are stored here; the mesh manages its own.">
            <input className="input text-sm" placeholder="headscale.example.org:8080" value={wgEndpoint} onChange={e => setWgEndpoint(e.target.value)} />
          </Field>
          <Field label="Network label (optional)">
            <input className="input text-sm" placeholder="my-tailnet" value={wgNetwork} onChange={e => setWgNetwork(e.target.value)} />
          </Field>
        </div>
      )}

      {provider === 'none' && (
        <div className="flex items-start gap-2 bg-warning-soft border border-warning-soft rounded-xl px-4 py-3 mb-5">
          <svg viewBox="0 0 20 20" fill="currentColor" className="w-4 h-4 text-warning mt-0.5 shrink-0">
            <path fillRule="evenodd" d="M8.485 2.495c.673-1.167 2.357-1.167 3.03 0l6.28 10.875c.673 1.167-.17 2.625-1.516 2.625H3.72c-1.347 0-2.189-1.458-1.515-2.625L8.485 2.495zM10 5a.75.75 0 01.75.75v3.5a.75.75 0 01-1.5 0v-3.5A.75.75 0 0110 5zm0 9a1 1 0 100-2 1 1 0 000 2z" clipRule="evenodd" />
          </svg>
          <p className="text-xs text-warning leading-relaxed">
            With no relay tunnel, this box is only reachable from outside your network if you've
            forwarded a port to it (or it has a static public IP). Remote access will stop working
            until you do — or you switch back to ephor.
          </p>
        </div>
      )}

      {saveMsg && (
        <div className={`flex items-start gap-2 text-xs mb-3 ${saveMsg.ok ? 'text-[var(--status-success)]' : 'text-danger'}`} role={saveMsg.ok ? 'status' : 'alert'}>
          <span className={`inline-block w-2 h-2 rounded-full mt-1 ${saveMsg.ok ? 'bg-[var(--status-success)]' : 'bg-[var(--status-danger)]'}`} aria-hidden="true" />
          <span>
            {saveMsg.text}
            {saveMsg.canForce && (
              <button onClick={() => save(true)} className="ml-2 underline decoration-dotted">Save anyway</button>
            )}
          </span>
        </div>
      )}

      <div className="flex flex-wrap gap-2 items-center">
        <button onClick={() => save(false)} disabled={saving} className="btn-primary text-sm disabled:opacity-40">
          {saving ? 'Saving…' : 'Save'}
        </button>
        <button onClick={runTest} disabled={testing} className="btn-secondary text-sm disabled:opacity-40">
          {testing ? 'Testing…' : 'Test active provider'}
        </button>
        {config?.provider !== 'ephor' && (
          <button onClick={resetToEphor} disabled={saving} className="btn-secondary text-sm disabled:opacity-40">
            Reset to ephor
          </button>
        )}
      </div>

      {testResult && (
        <div className={`mt-3 text-xs rounded px-3 py-2 ${testResult.success ? 'bg-[var(--status-success-soft)] text-[var(--status-success)]' : 'bg-[var(--status-danger-soft)] text-[var(--status-danger)]'}`}>
          {testResult.success ? 'Reachable' : 'Unreachable'}
          {typeof testResult.latency_ms === 'number' ? ` — latency ${testResult.latency_ms} ms` : ''}
          {testResult.detail ? ` — ${testResult.detail}` : ''}
        </div>
      )}
    </Section>
  )
}
