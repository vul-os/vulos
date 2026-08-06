// no-broker-dep:allow-file: Settings UI copy for the RELAY-01 provider picker: comments and the
// 'ephor' PROVIDERS entry describe it as 'A supported alternative, not a
// dependency' right next to 'vulos' explicitly labelled '(default)' --
// this file IS the UI proof that vulos, not ephor, is the default.

import { useState, useEffect, useCallback, type ChangeEvent } from 'react'
import { requireStepUp } from '../../lib/stepup'
import { Section, Field, Card, InfoList, InfoRow, Pill, Banner, StatTile } from './ui'

function isRecord(x: unknown): x is Record<string, unknown> {
  return typeof x === 'object' && x !== null
}
function errorMessage(err: unknown, fallback: string): string {
  return (isRecord(err) && typeof err.message === 'string' ? err.message : '') || fallback
}
function errorCode(err: unknown): string | undefined {
  return isRecord(err) && typeof err.code === 'string' ? err.code : undefined
}

// ── Wire shapes (backend/cmd/server/routes_relayconfig.go, reachwire.go) ────

interface IceServer {
  urls: string[]
  username?: string
  credential?: string
}
interface TurnConfig {
  ice_servers?: IceServer[]
}
interface Libp2pConfig {
  relay_peers?: string[]
}
interface WireguardConfig {
  endpoint?: string
  network?: string
}
interface RelayConfig {
  provider: string
  turn?: TurnConfig
  libp2p?: Libp2pConfig
  wireguard?: WireguardConfig
}
function toIceServer(x: unknown): IceServer | null {
  if (!isRecord(x)) return null
  return {
    urls: Array.isArray(x.urls) ? x.urls.filter((u): u is string => typeof u === 'string') : [],
    username: typeof x.username === 'string' ? x.username : undefined,
    credential: typeof x.credential === 'string' ? x.credential : undefined,
  }
}
function toRelayConfig(x: unknown): RelayConfig | null {
  if (!isRecord(x) || typeof x.provider !== 'string') return null
  return {
    provider: x.provider,
    turn: isRecord(x.turn)
      ? { ice_servers: Array.isArray(x.turn.ice_servers) ? x.turn.ice_servers.map(toIceServer).filter((s): s is IceServer => s !== null) : undefined }
      : undefined,
    libp2p: isRecord(x.libp2p)
      ? { relay_peers: Array.isArray(x.libp2p.relay_peers) ? x.libp2p.relay_peers.filter((p): p is string => typeof p === 'string') : undefined }
      : undefined,
    wireguard: isRecord(x.wireguard)
      ? { endpoint: typeof x.wireguard.endpoint === 'string' ? x.wireguard.endpoint : undefined, network: typeof x.wireguard.network === 'string' ? x.wireguard.network : undefined }
      : undefined,
  }
}

interface IngressDescriptor {
  mode?: string
  detail?: string
}
interface EffectiveConfig {
  ingress?: IngressDescriptor
  ice_servers?: unknown[]
}
function toEffectiveConfig(x: unknown): EffectiveConfig | null {
  if (!isRecord(x)) return null
  return {
    ingress: isRecord(x.ingress)
      ? { mode: typeof x.ingress.mode === 'string' ? x.ingress.mode : undefined, detail: typeof x.ingress.detail === 'string' ? x.ingress.detail : undefined }
      : undefined,
    ice_servers: Array.isArray(x.ice_servers) ? x.ice_servers : undefined,
  }
}

interface RelayConfigResponse {
  config: RelayConfig
  effective: EffectiveConfig | null
}
function toRelayConfigResponse(x: unknown): RelayConfigResponse | null {
  if (!isRecord(x)) return null
  const config = toRelayConfig(x.config)
  if (!config) return null
  return { config, effective: toEffectiveConfig(x.effective) }
}

interface ReachEndpoint {
  name?: string
  url?: string
  region?: string
}
interface ReachEndpointStatus {
  endpoint?: ReachEndpoint
  healthy?: boolean
  down_for_seconds?: number
  last_error?: string
}
interface ReachLink {
  name?: string
  state?: string
  public_url?: string
}
interface ReachStatus {
  enabled?: boolean
  endpoints: ReachEndpointStatus[]
  links: ReachLink[]
}
function toReachEndpoint(x: unknown): ReachEndpoint {
  if (!isRecord(x)) return {}
  return {
    name: typeof x.name === 'string' ? x.name : undefined,
    url: typeof x.url === 'string' ? x.url : undefined,
    region: typeof x.region === 'string' ? x.region : undefined,
  }
}
function toReachEndpointStatus(x: unknown): ReachEndpointStatus | null {
  if (!isRecord(x)) return null
  return {
    endpoint: isRecord(x.endpoint) ? toReachEndpoint(x.endpoint) : undefined,
    healthy: typeof x.healthy === 'boolean' ? x.healthy : undefined,
    down_for_seconds: typeof x.down_for_seconds === 'number' ? x.down_for_seconds : undefined,
    last_error: typeof x.last_error === 'string' ? x.last_error : undefined,
  }
}
function toReachLink(x: unknown): ReachLink | null {
  if (!isRecord(x)) return null
  return {
    name: typeof x.name === 'string' ? x.name : undefined,
    state: typeof x.state === 'string' ? x.state : undefined,
    public_url: typeof x.public_url === 'string' ? x.public_url : undefined,
  }
}
function toReachStatus(x: unknown): ReachStatus | null {
  if (!isRecord(x)) return null
  return {
    enabled: typeof x.enabled === 'boolean' ? x.enabled : undefined,
    endpoints: Array.isArray(x.endpoints) ? x.endpoints.map(toReachEndpointStatus).filter((e): e is ReachEndpointStatus => e !== null) : [],
    links: Array.isArray(x.links) ? x.links.map(toReachLink).filter((l): l is ReachLink => l !== null) : [],
  }
}

interface TestResult {
  success: boolean
  latency_ms?: number
  detail?: string
}
function toTestResult(x: unknown): TestResult {
  if (!isRecord(x)) return { success: false }
  return {
    success: !!x.success,
    latency_ms: typeof x.latency_ms === 'number' ? x.latency_ms : undefined,
    detail: typeof x.detail === 'string' ? x.detail : undefined,
  }
}

interface SaveMsg {
  ok: boolean
  text: string
  canForce?: boolean
}

interface NodeDraft {
  url: string
  name: string
  token: string
  region: string
}

// ---------------------------------------------------------------------------
// RelayPanel — Settings -> Network -> Relay & Reachability (box-owner only
// to CHANGE; any signed-in user can view).
//
// RELAY-01: lets the owner pick the box's relay/TURN/rendezvous provider.
// Vulos's OWN built-in reverse tunnel (docs/REACH.md) is the DEFAULT and
// needs nothing external — a relay is `vulos relay serve`, the same binary in
// a role. Pier is a supported alternative. You can also bring your own
// STUN/TURN, a libp2p Circuit Relay v2 peer, a Tailscale/Headscale/Nebula
// WireGuard mesh, or turn the relay tunnel off entirely if this box has a
// static IP / port-forward.
//
// Reachability is really three independent concerns — app-media ICE (what
// makes Meet/Talk calls connect), box HTTP ingress (reaching this box from
// outside your NAT), and box<->box rendezvous/discovery. Picking libp2p or
// WireGuard here changes ingress/rendezvous ONLY — Meet/Talk calls keep
// using Pier's ICE (public STUN + this box's own TURN, if configured)
// automatically; this panel never lets you accidentally break call audio
// while trying to change how the box itself is reached.
//
// Endpoints (backend/cmd/server/routes_relayconfig.go):
//   GET  /api/relayconfig            — { config, effective }
//   POST /api/relayconfig            — (step-up) { provider, turn?, libp2p?, wireguard?, force? }
//   POST /api/relayconfig/reset      — (step-up) revert to the default
//   POST /api/relayconfig/test       — TCP-probe the active provider's endpoint
//
// Relay NODE set + live health (backend/cmd/server/reachwire.go):
//   GET  /api/network/reach          — { enabled, endpoints:[Status], links:[LinkStatus] }
//     This is the box's OWN configured relay nodes and their live tunnel
//     health, token-redacted. The node set itself (VULOS_RELAY_ENDPOINTS /
//     _FILE) is applied from the box's environment or a 0600 file — the bearer
//     grants deliberately never ride an HTTP surface — so this panel READS the
//     set here and helps you build the config entry to add a node, rather than
//     writing tokens over the network.
// ---------------------------------------------------------------------------

async function jsonFetch(url: string, opts?: RequestInit): Promise<unknown> {
  const r = await fetch(url, { credentials: 'same-origin', ...opts })
  const d: unknown = await r.json().catch(() => ({}))
  if (!r.ok) throw new Error((isRecord(d) && typeof d.error === 'string' && d.error) || `HTTP ${r.status}`)
  return d
}

// INGRESS_MODES maps the backend's machine tags (relayconfig.IngressDescriptor
// .Mode — see providers.go) to an owner-facing label. Nothing invented: these
// are exactly the tags the resolver emits; anything else falls through to the
// raw tag so a new backend mode is still shown honestly rather than hidden.
const INGRESS_MODES: Record<string, string> = {
  'relay-tunnel': 'Relay tunnel (dialled out — no inbound ports)',
  'none': 'No relay tunnel',
  'direct-portforward': 'Direct — static IP / port-forward',
  'libp2p-circuit-relay': 'libp2p Circuit Relay v2',
  'wireguard-mesh': 'WireGuard mesh',
}
const ingressLabel = (mode: string | undefined): string => (mode ? (INGRESS_MODES[mode] || mode) : 'Built-in default (call media only)')

// nodeEntryJSON builds the single VULOS_RELAY_ENDPOINTS array element for a
// node the owner wants to add, so they can paste it into their env/file. The
// token stays on their side — this panel never transmits it.
function nodeEntryJSON({ url, name, token, region }: NodeDraft): string {
  const e: { url: string; name: string; token: string; region?: string } = { url: url.trim(), name: name.trim(), token: token.trim() }
  if (region.trim()) e.region = region.trim()
  return JSON.stringify(e, null, 2)
}

interface ProviderOption {
  value: string
  label: string
  blurb: string
}

const PROVIDERS: ProviderOption[] = [
  {
    value: 'vulos',
    label: 'Vulos relay (default)',
    blurb:
      "Vulos's own built-in reverse tunnel: this box dials out to your relay and is reachable from anywhere, with no inbound ports. Plus public STUN and this box's TURN for calls. Configure relay endpoints with VULOS_RELAY_ENDPOINTS — list two under different operators and the box holds a tunnel to both at once.",
  },
  {
    value: 'ephor',
    label: 'Pier relay',
    blurb:
      'Use a Pier relay (github.com/vul-os/pier) instead of the built-in one. A supported alternative, not a dependency — it speaks the same rendezvous contract, so switching is a config change.',
  },
  { value: 'turn', label: 'Bring your own STUN/TURN', blurb: 'Your own coturn (or any STUN/TURN provider) for call media. Ingress + rendezvous stay on the built-in provider.' },
  { value: 'libp2p', label: 'libp2p Circuit Relay v2', blurb: 'Your own relay peer(s) for box reachability + discovery. Call media (ICE) is unaffected.' },
  { value: 'wireguard', label: 'WireGuard mesh (Tailscale/Headscale/Nebula)', blurb: 'Reach this box over your own mesh instead of the relay tunnel. Call media (ICE) is unaffected.' },
  { value: 'none', label: 'None (static IP / port-forward)', blurb: 'No relay tunnel — you manage your own inbound path. Call media (ICE) is unaffected.' },
]

interface TurnServerDraft {
  urls: string
  username: string
  credential: string
}

function emptyTurnServer(): TurnServerDraft {
  return { urls: '', username: '', credential: '' }
}

export default function RelayPanel() {
  const [config, setConfig] = useState<RelayConfig | null>(null)       // safe view from GET (config)
  const [effective, setEffective] = useState<EffectiveConfig | null>(null) // resolved snapshot from GET (effective)
  const [reach, setReach] = useState<ReachStatus | null>(null)         // { enabled, endpoints, links } from /api/network/reach
  const [loadError, setLoadError] = useState('')

  // Node-entry builder (produces JSON to paste into VULOS_RELAY_ENDPOINTS).
  const [showAddNode, setShowAddNode] = useState(false)
  const [nodeDraft, setNodeDraft] = useState<NodeDraft>({ url: '', name: '', token: '', region: '' })

  const [provider, setProvider] = useState('ephor')
  const [turnServers, setTurnServers] = useState<TurnServerDraft[]>([emptyTurnServer()])
  const [libp2pPeers, setLibp2pPeers] = useState('')
  const [wgEndpoint, setWgEndpoint] = useState('')
  const [wgNetwork, setWgNetwork] = useState('')

  const [saving, setSaving] = useState(false)
  const [saveMsg, setSaveMsg] = useState<SaveMsg | null>(null)
  const [testing, setTesting] = useState(false)
  const [testResult, setTestResult] = useState<TestResult | null>(null)

  const load = useCallback(() => {
    setLoadError('')
    jsonFetch('/api/relayconfig')
      .then((raw: unknown) => {
        const d = toRelayConfigResponse(raw)
        if (!d) throw new Error('malformed relay configuration')
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
      .catch(e => setLoadError(errorMessage(e, 'Could not load relay configuration.')))
    // Live node set + tunnel health is a separate, read-only, token-free route.
    jsonFetch('/api/network/reach').then((raw: unknown) => setReach(toReachStatus(raw))).catch(() => setReach(null))
  }, [])

  useEffect(() => { load() }, [load])

  const addTurnServer = () => setTurnServers(s => [...s, emptyTurnServer()])
  const removeTurnServer = (i: number) => setTurnServers(s => s.filter((_, idx) => idx !== i))
  const updateTurnServer = (i: number, field: keyof TurnServerDraft, value: string) =>
    setTurnServers(s => s.map((row, idx) => idx === i ? { ...row, [field]: value } : row))

  interface RelayPayload {
    provider: string
    force: boolean
    turn?: { ice_servers: { urls: string[]; username?: string; credential?: string }[] }
    libp2p?: { relay_peers: string[] }
    wireguard?: { endpoint: string; network?: string }
  }

  const buildPayload = (force: boolean): RelayPayload => {
    const payload: RelayPayload = { provider, force: !!force }
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

  const save = async (force: boolean) => {
    setSaving(true)
    setSaveMsg(null)
    try {
      await requireStepUp()
      await jsonFetch('/api/relayconfig', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(buildPayload(force)) })
      setSaveMsg({ ok: true, text: 'Relay configuration saved.' })
      load()
    } catch (e) {
      if (errorCode(e) === 'CANCELLED') { setSaving(false); return }
      const text = errorMessage(e, 'Could not save relay configuration.')
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
      if (errorCode(e) !== 'CANCELLED') setSaveMsg({ ok: false, text: errorMessage(e, 'Could not reset.') })
    } finally {
      setSaving(false)
    }
  }

  const runTest = async () => {
    setTesting(true)
    setTestResult(null)
    try {
      const r = await jsonFetch('/api/relayconfig/test', { method: 'POST' })
      setTestResult(toTestResult(r))
    } catch (e) {
      setTestResult({ success: false, detail: errorMessage(e, 'test failed') })
    } finally {
      setTesting(false)
    }
  }

  const activeMeta = PROVIDERS.find(p => p.value === config?.provider)

  return (
    <Section
      icon="relay"
      title="Relay & Reachability"
      desc="The built-in Vulos relay is the default reachability path — it just works, nothing to configure. You can bring your own instead: BYO STUN/TURN, a libp2p relay peer, a WireGuard mesh, or none at all if this box has a static IP. Pier is a supported alternative relay."
      actions={config && <Pill tone="accent">{activeMeta?.label?.replace(/ \(default\)/, '') || config.provider}</Pill>}
    >
      {loadError && <Banner tone="info">{loadError}</Banner>}

      {config && (
        <Card title="Current reachability" desc="What is resolving right now across the three reachability concerns.">
          <InfoList>
            <InfoRow label="Active provider" value={activeMeta?.label || config.provider} />
            <InfoRow label="Ingress mode" value={ingressLabel(effective?.ingress?.mode)} />
            <InfoRow label="Ingress detail" value={effective?.ingress?.detail || '—'} mono={!!effective?.ingress?.detail} />
            <InfoRow label="Call media (ICE) servers" value={`${effective?.ice_servers?.length ?? 0} configured`} />
          </InfoList>
        </Card>
      )}

      {(config?.provider === 'vulos' || config?.provider === 'ephor') && (
        <RelayNodes
          reach={reach}
          showAdd={showAddNode}
          setShowAdd={setShowAddNode}
          draft={nodeDraft}
          setDraft={setNodeDraft}
        />
      )}

      <Card title="Reachability provider" desc="Ingress and rendezvous only — call media (ICE) is never broken by switching here.">
        <div className="space-y-2.5">
        {PROVIDERS.map(p => {
          const on = provider === p.value
          return (
            <label
              key={p.value}
              className={`flex items-start gap-3 rounded-xl border px-4 py-3.5 cursor-pointer transition-all ${
                on
                  ? 'accent-border bg-[var(--accent-soft)] shadow-sm'
                  : 'border-[var(--border-default)] bg-[var(--bg-surface)] hover:border-[var(--border-strong)] hover:bg-[var(--bg-hover)]/40'
              }`}
            >
              <input
                type="radio"
                name="relay-provider"
                value={p.value}
                checked={on}
                onChange={() => { setProvider(p.value); setSaveMsg(null) }}
                className="mt-0.5 accent-[var(--accent)]"
              />
              <span className="min-w-0">
                <span className="flex items-center gap-2 text-sm font-medium text-[var(--text-primary)]">
                  {p.label}
                  {config?.provider === p.value && <Pill tone="success">Active</Pill>}
                </span>
                <span className="block text-xs text-[var(--text-tertiary)] mt-1 leading-relaxed">{p.blurb}</span>
              </span>
            </label>
          )
        })}
        </div>
      </Card>

      {provider === 'turn' && (
        <div className="rounded-xl border border-[var(--border-default)] bg-[var(--bg-surface)] p-4 mb-5">
          <p className="text-xs text-[var(--text-faint)] mb-3">
            One or more STUN/TURN servers (comma-separate multiple URLs per row, e.g.
            <code className="mx-1 text-[12px]">turn:relay.example.org:3478?transport=udp, turn:relay.example.org:3478?transport=tcp</code>).
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

// ── RelayNodes — the box's OWN configured relay nodes and their live tunnel
// health, read from GET /api/network/reach (token-redacted). The set is applied
// from the box environment (VULOS_RELAY_ENDPOINTS / _FILE), so this view is
// read-only + a config-entry builder — it never transmits a bearer grant. ────
interface RelayNodesProps {
  reach: ReachStatus | null
  showAdd: boolean
  setShowAdd: (v: boolean) => void
  draft: NodeDraft
  setDraft: (fn: (d: NodeDraft) => NodeDraft) => void
}

function RelayNodes({ reach, showAdd, setShowAdd, draft, setDraft }: RelayNodesProps) {
  const endpoints = reach?.endpoints || []
  const links = reach?.links || []
  const namedLinks = links.filter((l): l is ReachLink & { name: string } => typeof l.name === 'string')
  const linkByName: Record<string, ReachLink> = Object.fromEntries(namedLinks.map(l => [l.name, l]))
  const count = endpoints.length
  const healthy = endpoints.filter(e => e.healthy).length
  const upLinks = links.filter(l => l.state === 'up').length

  return (
    <Card
      title="Your relay nodes"
      desc="The relays this box dials out to for reachability, and their live tunnel health."
      aside={count > 0 && <Pill tone={upLinks > 0 ? 'success' : count ? 'warning' : 'neutral'}>{upLinks} tunnel{upLinks === 1 ? '' : 's'} up</Pill>}
    >
      <Banner tone="info">
        Vulos the project runs no relay, rendezvous, or hosted infrastructure — these are relays <em>you</em> run.
        A relay is a box with a public IP running <code className="mono text-[12px]">vulos relay serve</code> (a cheap VPS is plenty);
        this box dials out to it and opens no inbound ports.
      </Banner>

      {count === 0 ? (
        <div className="mt-4">
          <Banner tone="warning" title="No relay nodes configured">
            This box holds no relay tunnels. That is correct if it has a public IP or only needs its LAN.
            Otherwise it is unreachable from outside your network — add a node below and apply it via
            <code className="mono text-[12px] mx-1">VULOS_RELAY_ENDPOINTS</code>.
          </Banner>
        </div>
      ) : (
        <>
          <div className="grid grid-cols-3 gap-2.5 mt-4 mb-4">
            <StatTile label="Nodes" value={count} icon="relay" />
            <StatTile label="Healthy" value={`${healthy}/${count}`} tone={healthy === count ? 'success' : 'warning'} />
            <StatTile label="Tunnels up" value={upLinks} tone={upLinks > 0 ? 'success' : 'danger'} />
          </div>
          <div className="rounded-xl border border-[var(--border-default)] overflow-hidden divide-y divide-[var(--border-subtle)]">
            {endpoints.map((st, i) => {
              const ep = st.endpoint || {}
              const link: ReachLink = (ep.name ? linkByName[ep.name] : undefined) || {}
              const down = !st.healthy
              return (
                <div key={i} className="px-4 py-3 bg-[var(--bg-surface)]">
                  <div className="flex items-center justify-between gap-3">
                    <span className="text-sm font-medium text-[var(--text-primary)] truncate">
                      {ep.name || '—'}<span className="text-[var(--text-faint)]"> @ {ep.url}</span>
                    </span>
                    <Pill tone={down ? 'danger' : link.state === 'up' ? 'success' : 'warning'}>
                      {down ? `backoff ${st.down_for_seconds || 0}s` : link.state || (st.healthy ? 'healthy' : 'down')}
                    </Pill>
                  </div>
                  <div className="mt-1 flex flex-wrap gap-x-4 gap-y-0.5 text-[12px] text-[var(--text-muted)]">
                    {ep.region && <span>region {ep.region}</span>}
                    {link.public_url && <span className="mono truncate">→ {link.public_url}</span>}
                    {st.last_error && <span className="text-[var(--status-danger)] truncate">last error: {st.last_error}</span>}
                  </div>
                </div>
              )
            })}
          </div>
          {count === 1 && (
            <div className="mt-3">
              <Banner tone="warning" title="Single relay = single point of failure">
                With one relay, losing that node (or its operator) takes this box's remote reachability offline.
                List a second node under a <strong>different operator/region</strong> and the box holds a tunnel to
                every one at once, preferring the best by latency/health.
              </Banner>
            </div>
          )}
        </>
      )}

      <RelayNodeGuide showAdd={showAdd} setShowAdd={setShowAdd} draft={draft} setDraft={setDraft} count={count} />
    </Card>
  )
}

// ── RelayNodeGuide — when to add nodes (redundancy / capacity) and a builder
// that produces the JSON config entry to paste into VULOS_RELAY_ENDPOINTS. No
// token ever leaves the browser; applying the set stays a box-side operation. ─
interface RelayNodeGuideProps {
  showAdd: boolean
  setShowAdd: (v: boolean) => void
  draft: NodeDraft
  setDraft: (fn: (d: NodeDraft) => NodeDraft) => void
  count: number
}

function RelayNodeGuide({ showAdd, setShowAdd, draft, setDraft, count }: RelayNodeGuideProps) {
  const [copied, setCopied] = useState(false)
  const ready = draft.url.trim() && draft.name.trim() && draft.token.trim()
  const entry = ready ? nodeEntryJSON(draft) : ''
  const upd = (k: keyof NodeDraft) => (e: ChangeEvent<HTMLInputElement>) => { setDraft(d => ({ ...d, [k]: e.target.value })); setCopied(false) }
  const copy = () => { navigator.clipboard?.writeText(entry).then(() => setCopied(true)).catch(() => {}) }

  return (
    <div className="mt-4">
      <div className="rounded-xl border border-[var(--border-default)] bg-[var(--bg-elevated)]/40 px-4 py-3 text-xs text-[var(--text-tertiary)] leading-relaxed">
        <p className="font-medium text-[var(--text-secondary)] mb-1">When to add a node</p>
        <p>Add a relay when you have only one (remove the single point of failure), when a node consistently
        shows <em>backoff</em>, or when all your nodes share one operator/region — spread them so no single
        operator can take you offline. Relays hold tunnels, not load-balanced traffic, so nodes are for
        <strong> redundancy and reach</strong>, not raw throughput; a couple of well-placed VPSes cover most fleets.
        To remove a node, drop its entry from your endpoints file and restart the box.</p>
      </div>

      <button onClick={() => setShowAdd(!showAdd)} className="btn-secondary text-xs mt-3">
        {showAdd ? 'Hide node builder' : count > 0 ? '+ Add another relay node' : '+ Add a relay node'}
      </button>

      {showAdd && (
        <div className="mt-3 rounded-xl border border-[var(--border-default)] bg-[var(--bg-surface)] p-4">
          <p className="text-[12px] text-[var(--text-faint)] mb-3 leading-relaxed">
            Mint a grant on your relay with <code className="mono">vulos relay grant &lt;name&gt;</code>, fill it in here,
            then add the generated entry to your <code className="mono">VULOS_RELAY_ENDPOINTS</code> array (or 0600
            endpoints file) and restart. The token stays on your machine — this builder never sends it anywhere.
          </p>
          <div className="grid grid-cols-1 sm:grid-cols-2 gap-2 mb-3">
            <input className="input text-sm" placeholder="https://relay.example.com" value={draft.url} onChange={upd('url')} />
            <input className="input text-sm" placeholder="box name (one DNS label)" value={draft.name} onChange={upd('name')} />
            <input className="input text-sm" type="password" placeholder="bearer token from the grant" value={draft.token} onChange={upd('token')} />
            <input className="input text-sm" placeholder="region label (optional)" value={draft.region} onChange={upd('region')} />
          </div>
          {ready ? (
            <>
              <pre className="text-[12px] mono bg-[var(--bg-elevated)] rounded-lg p-3 overflow-x-auto text-[var(--text-secondary)]">{entry}</pre>
              <button onClick={copy} className="btn-secondary text-xs mt-2">{copied ? 'Copied' : 'Copy entry'}</button>
            </>
          ) : (
            <p className="text-[12px] text-[var(--text-faint)]">Fill in the relay URL, name and token to generate the config entry.</p>
          )}
        </div>
      )}
    </div>
  )
}
