import { useState, useEffect, useCallback, type ReactNode } from 'react'
import { requireStepUp } from '../../lib/stepup'

// ---------------------------------------------------------------------------
// CDNPanel — Settings -> Network -> CDN (box-owner only).
//
// Configure which CDN vendor fronts this box (Cloudflare / Fastly / Bunny) —
// origin host, Host header, authenticated-origin-pulls (mTLS) flag — and,
// optionally, an origin-firewall that would restrict inbound 80/443 to that
// vendor's published egress IP ranges.
//
// Endpoints (backend/cmd/server/routes_cdn.go):
//   GET    /api/cdn/config
//   PUT    /api/cdn/config
//   DELETE /api/cdn/config
//   GET    /api/cdn/ranges?provider=...
//   POST   /api/cdn/ranges/refresh
//   GET    /api/cdn/firewall
//   POST   /api/cdn/firewall/enable   (step-up)
//   POST   /api/cdn/firewall/disable
//
// IMPORTANT — this box does not yet install live firewall rules. Enabling
// the firewall here generates a real nftables ruleset and shows exactly what
// it contains, but only RECORDS it — nothing is applied to this box's actual
// network filter. The backend is the source of truth for that distinction
// (see routes_cdn.go's doc comment); this panel just surfaces it honestly
// rather than implying more than what's true.
// ---------------------------------------------------------------------------

function isRecord(x: unknown): x is Record<string, unknown> {
  return typeof x === 'object' && x !== null
}

function errorMessage(err: unknown): string {
  return isRecord(err) && typeof err.message === 'string' ? err.message : String(err)
}

function errorCode(err: unknown): string | undefined {
  return isRecord(err) && typeof err.code === 'string' ? err.code : undefined
}

interface SectionProps {
  title: ReactNode
  desc?: ReactNode
  children?: ReactNode
}
function Section({ title, desc, children }: SectionProps) {
  return (
    <div>
      <header className="mb-5 pb-4 border-b border-[var(--border-default)]">
        <h2 className="text-xl font-semibold tracking-tight text-[var(--text-primary)]">{title}</h2>
        {desc && <p className="mt-1 text-sm text-[var(--text-tertiary)] leading-relaxed max-w-[68ch]">{desc}</p>}
      </header>
      {children}
    </div>
  )
}

interface FieldProps {
  label: ReactNode
  hint?: ReactNode
  children?: ReactNode
}
function Field({ label, hint, children }: FieldProps) {
  return (
    <div className="mb-3">
      <label className="block text-xs text-[var(--text-muted)] mb-1">{label}</label>
      {children}
      {hint && <p className="text-[12px] text-[var(--text-faint)] mt-1">{hint}</p>}
    </div>
  )
}

async function jsonFetch(url: string, opts?: RequestInit): Promise<unknown> {
  const r = await fetch(url, { credentials: 'same-origin', ...opts })
  const d: unknown = await r.json().catch(() => ({}))
  if (!r.ok) throw new Error((isRecord(d) && typeof d.error === 'string' && d.error) || `HTTP ${r.status}`)
  return d
}

const PROVIDERS = [
  { value: 'cloudflare', label: 'Cloudflare' },
  { value: 'fastly', label: 'Fastly' },
  { value: 'bunny', label: 'Bunny' },
]

// Wire shapes, narrowed field-by-field rather than trusted wholesale (see
// backend/cmd/server/routes_cdn.go's cdnConfigView / cdn.BuiltStatus /
// cdnIPRangeView for the authoritative JSON these mirror).
interface CdnConfig {
  provider?: string
  origin_host?: string
  host_header?: string
  mtls_enabled?: boolean
  firewall_enabled?: boolean
  ssh_port?: number
  extra_allow_ports?: number[]
}
function toCdnConfig(x: unknown): CdnConfig {
  if (!isRecord(x)) return {}
  return {
    provider: typeof x.provider === 'string' ? x.provider : undefined,
    origin_host: typeof x.origin_host === 'string' ? x.origin_host : undefined,
    host_header: typeof x.host_header === 'string' ? x.host_header : undefined,
    mtls_enabled: typeof x.mtls_enabled === 'boolean' ? x.mtls_enabled : undefined,
    firewall_enabled: typeof x.firewall_enabled === 'boolean' ? x.firewall_enabled : undefined,
    ssh_port: typeof x.ssh_port === 'number' ? x.ssh_port : undefined,
    extra_allow_ports: Array.isArray(x.extra_allow_ports) ? x.extra_allow_ports.filter((n): n is number => typeof n === 'number') : undefined,
  }
}

interface CdnStatus {
  configured?: boolean
  firewall_enabled?: boolean
  provider?: string
  cidr_count?: number
  ranges_fetched_at?: string
  ruleset?: string
  warning?: string
  live_enforcement?: boolean
}
function toCdnStatus(x: unknown): CdnStatus {
  if (!isRecord(x)) return {}
  return {
    configured: typeof x.configured === 'boolean' ? x.configured : undefined,
    firewall_enabled: typeof x.firewall_enabled === 'boolean' ? x.firewall_enabled : undefined,
    provider: typeof x.provider === 'string' ? x.provider : undefined,
    cidr_count: typeof x.cidr_count === 'number' ? x.cidr_count : undefined,
    ranges_fetched_at: typeof x.ranges_fetched_at === 'string' ? x.ranges_fetched_at : undefined,
    ruleset: typeof x.ruleset === 'string' ? x.ruleset : undefined,
    warning: typeof x.warning === 'string' ? x.warning : undefined,
    live_enforcement: typeof x.live_enforcement === 'boolean' ? x.live_enforcement : undefined,
  }
}

interface CdnIPRange {
  cidr: string
  fetched_at: string
}
function toRangesMap(x: unknown): Record<string, CdnIPRange[]> {
  if (!isRecord(x) || !isRecord(x.ranges)) return {}
  const out: Record<string, CdnIPRange[]> = {}
  for (const [provider, list] of Object.entries(x.ranges)) {
    if (!Array.isArray(list)) continue
    out[provider] = list.filter(isRecord).map((r) => ({
      cidr: typeof r.cidr === 'string' ? r.cidr : '',
      fetched_at: typeof r.fetched_at === 'string' ? r.fetched_at : '',
    }))
  }
  return out
}

// POST /api/cdn/firewall/enable → { status: BuiltStatus, note: string }
function toEnableResponse(x: unknown): { status: CdnStatus; note?: string } {
  if (!isRecord(x)) return { status: {} }
  return {
    status: toCdnStatus(x.status),
    note: typeof x.note === 'string' ? x.note : undefined,
  }
}

export default function CDNPanel() {
  const [cfg, setCfg] = useState<CdnConfig | null>(null)
  const [loadError, setLoadError] = useState('')

  const [provider, setProvider] = useState('cloudflare')
  const [originHost, setOriginHost] = useState('')
  const [hostHeader, setHostHeader] = useState('')
  const [mtls, setMtls] = useState(false)
  const [sshPort, setSshPort] = useState('')
  const [extraPorts, setExtraPorts] = useState('')

  const [saving, setSaving] = useState(false)
  const [saveError, setSaveError] = useState('')
  const [saveOk, setSaveOk] = useState(false)

  const [ranges, setRanges] = useState<Record<string, CdnIPRange[]> | null>(null)
  const [refreshing, setRefreshing] = useState(false)

  const [status, setStatus] = useState<CdnStatus | null>(null)
  const [fwBusy, setFwBusy] = useState(false)
  const [fwError, setFwError] = useState('')
  const [fwNote, setFwNote] = useState('')
  const [showRuleset, setShowRuleset] = useState(false)

  const load = useCallback(() => {
    setLoadError('')
    Promise.all([
      jsonFetch('/api/cdn/config'),
      jsonFetch('/api/cdn/firewall'),
    ])
      .then(([cRaw, stRaw]) => {
        const c = toCdnConfig(cRaw)
        setCfg(c)
        setStatus(toCdnStatus(stRaw))
        if (c.provider) setProvider(c.provider)
        setOriginHost(c.origin_host || '')
        setHostHeader(c.host_header || '')
        setMtls(!!c.mtls_enabled)
        setSshPort(c.ssh_port ? String(c.ssh_port) : '')
        setExtraPorts((c.extra_allow_ports || []).join(', '))
      })
      .catch((e: unknown) => setLoadError(errorMessage(e) || 'Could not load CDN settings.'))
  }, [])

  useEffect(() => { load() }, [load])

  const loadRanges = useCallback(() => {
    jsonFetch('/api/cdn/ranges')
      .then((d) => setRanges(toRangesMap(d)))
      .catch(() => {})
  }, [])

  useEffect(() => { loadRanges() }, [loadRanges])

  const parsedExtraPorts = () =>
    extraPorts
      .split(',')
      .map(s => s.trim())
      .filter(Boolean)
      .map(Number)
      .filter(n => Number.isInteger(n) && n > 0 && n <= 65535)

  const save = async () => {
    setSaving(true)
    setSaveError('')
    setSaveOk(false)
    try {
      const body = {
        provider,
        origin_host: originHost.trim(),
        host_header: hostHeader.trim(),
        mtls_enabled: mtls,
        ssh_port: sshPort ? Number(sshPort) : 0,
        extra_allow_ports: parsedExtraPorts(),
      }
      const saved = await jsonFetch('/api/cdn/config', {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(body),
      })
      setCfg(toCdnConfig(saved))
      setSaveOk(true)
      const st = await jsonFetch('/api/cdn/firewall')
      setStatus(toCdnStatus(st))
    } catch (e) {
      setSaveError(errorMessage(e) || 'Could not save CDN config.')
    } finally {
      setSaving(false)
    }
  }

  const refreshRanges = async () => {
    setRefreshing(true)
    try {
      await jsonFetch('/api/cdn/ranges/refresh', { method: 'POST' })
      await loadRanges()
      const st = await jsonFetch('/api/cdn/firewall')
      setStatus(toCdnStatus(st))
    } catch (e) {
      setLoadError(errorMessage(e) || 'Could not refresh CDN IP ranges.')
    } finally {
      setRefreshing(false)
    }
  }

  const enableFirewall = async () => {
    if (!window.confirm(
      'Enable the origin firewall preview?\n\n' +
      'This box does not yet install live firewall rules — this generates and ' +
      'records the ruleset that WOULD restrict inbound ports 80/443 to your ' +
      `${provider} CDN's published IP ranges. Nothing on this box's network ` +
      'filter changes. Loopback, established connections, SSH, and any extra ' +
      'admin ports you listed are always left open regardless.'
    )) return
    setFwBusy(true)
    setFwError('')
    setFwNote('')
    try {
      await requireStepUp()
      const d = toEnableResponse(await jsonFetch('/api/cdn/firewall/enable', { method: 'POST' }))
      setStatus(d.status)
      setFwNote(d.note || '')
      setShowRuleset(true)
    } catch (e) {
      if (errorCode(e) !== 'CANCELLED') setFwError(errorMessage(e) || 'Could not enable the CDN firewall.')
    } finally {
      setFwBusy(false)
    }
  }

  const disableFirewall = async () => {
    setFwBusy(true)
    setFwError('')
    setFwNote('')
    try {
      await jsonFetch('/api/cdn/firewall/disable', { method: 'POST' })
      const st = await jsonFetch('/api/cdn/firewall')
      setStatus(toCdnStatus(st))
    } catch (e) {
      setFwError(errorMessage(e) || 'Could not disable the CDN firewall.')
    } finally {
      setFwBusy(false)
    }
  }

  const rangeCount = ranges && ranges[provider] ? ranges[provider].length : 0

  return (
    <Section
      title="CDN"
      desc="Point a CDN of your choice (Cloudflare, Fastly, or Bunny) at this box, and optionally preview an origin firewall that would restrict inbound web traffic to that CDN's published address ranges."
    >
      {loadError && (
        <div className="rounded-xl border border-[var(--border-strong)] bg-[var(--bg-elevated)] text-[var(--text-secondary)] px-4 py-3 mb-5 text-sm" role="alert">
          {loadError}
        </div>
      )}

      <div className="rounded-xl border border-[var(--border-default)] bg-[var(--bg-surface)] p-4 mb-6 space-y-3">
        <h3 className="text-sm font-medium text-[var(--text-primary)]">CDN vendor + origin</h3>

        <Field label="Provider">
          <select value={provider} onChange={e => setProvider(e.target.value)} className="input text-sm py-2 w-full">
            {PROVIDERS.map(p => <option key={p.value} value={p.value}>{p.label}</option>)}
          </select>
        </Field>

        <Field label="Origin host" hint="The hostname this box's origin is reachable at (what the CDN points to).">
          <input
            type="text"
            value={originHost}
            onChange={e => setOriginHost(e.target.value)}
            placeholder="origin.example.org"
            autoComplete="off"
            spellCheck={false}
            className="input text-sm py-2 font-mono w-full"
          />
        </Field>

        <Field label="Host header" hint="Optional. The Host header the CDN should send when it requests this origin; leave blank to use the origin host.">
          <input
            type="text"
            value={hostHeader}
            onChange={e => setHostHeader(e.target.value)}
            placeholder="example.org"
            autoComplete="off"
            spellCheck={false}
            className="input text-sm py-2 font-mono w-full"
          />
        </Field>

        <label className="flex items-center gap-2 text-sm text-[var(--text-secondary)] cursor-pointer">
          <input type="checkbox" checked={mtls} onChange={e => setMtls(e.target.checked)} />
          Authenticated origin pulls (mTLS) — informational flag only; this box does not enforce it yet.
        </label>

        <div className="pt-2 border-t border-[var(--border-default)]">
          <p className="text-xs text-[var(--text-muted)] mb-2">Origin-firewall safety inputs — always allowed regardless of firewall state:</p>
          <Field label="SSH port" hint="Defaults to 22 if left blank. Always allowed by any generated ruleset.">
            <input
              type="number"
              min="1"
              max="65535"
              value={sshPort}
              onChange={e => setSshPort(e.target.value)}
              placeholder="22"
              className="input text-sm py-2 w-32"
            />
          </Field>
          <Field label="Extra always-allow ports" hint="Comma-separated. E.g. a dedicated admin dashboard port. Optional.">
            <input
              type="text"
              value={extraPorts}
              onChange={e => setExtraPorts(e.target.value)}
              placeholder="9090, 9091"
              className="input text-sm py-2 font-mono w-full"
            />
          </Field>
        </div>

        {saveError && <p className="text-xs text-danger" role="alert">{saveError}</p>}
        {saveOk && !saveError && <p className="text-xs text-success">Saved.</p>}
        <button
          onClick={save}
          disabled={saving || !originHost.trim()}
          className="btn-primary text-sm disabled:opacity-40 disabled:cursor-not-allowed"
        >
          {saving ? 'Saving…' : 'Save CDN config'}
        </button>
      </div>

      <div className="rounded-xl border border-[var(--border-default)] bg-[var(--bg-surface)] p-4 mb-6">
        <div className="flex items-center justify-between mb-2">
          <h3 className="text-sm font-medium text-[var(--text-primary)]">
            {PROVIDERS.find(p => p.value === provider)?.label || provider} IP ranges
          </h3>
          <button onClick={refreshRanges} disabled={refreshing} className="btn-secondary text-xs px-2.5 py-1 disabled:opacity-40">
            {refreshing ? 'Refreshing…' : 'Refresh now'}
          </button>
        </div>
        <p className="text-xs text-[var(--text-faint)] mb-2">
          {rangeCount > 0
            ? `${rangeCount} CIDR range${rangeCount === 1 ? '' : 's'} cached (auto-refreshed every 4 hours).`
            : 'No ranges cached yet — refreshed automatically shortly after startup, or click Refresh now.'}
        </p>
        {rangeCount > 0 && (
          <div className="max-h-32 overflow-y-auto rounded-lg bg-[var(--bg-elevated)] px-3 py-2">
            <code className="text-[12px] font-mono text-[var(--text-tertiary)] whitespace-pre-wrap break-all">
              {(ranges?.[provider] || []).map(r => r.cidr).join('\n')}
            </code>
          </div>
        )}
      </div>

      <div className="rounded-xl border border-warning-soft bg-warning-soft p-4">
        <h3 className="text-sm font-medium text-[var(--text-primary)] mb-1">Origin firewall — preview only</h3>
        <p className="text-xs text-warning mb-3 leading-relaxed">
          This box does not yet install live firewall rules. Enabling below generates a real ruleset
          that would restrict inbound ports 80 and 443 to your CDN's IP ranges — and records it for
          review — but does not touch this box's actual network filter. Loopback, established
          connections, your SSH port, and any extra admin ports above are always left open in the
          generated ruleset, on top of that: nothing is applied at all in this build.
        </p>

        {status && (
          <div className="text-xs text-[var(--text-secondary)] space-y-1 mb-3">
            <div>Status: <strong>{status.firewall_enabled ? 'enabled (dry run)' : 'disabled'}</strong></div>
            {status.warning && <div className="text-[var(--text-faint)]">{status.warning}</div>}
            {status.cidr_count !== undefined && status.cidr_count > 0 && <div>{status.cidr_count} CIDR(s) would be allowed on 80/443.</div>}
          </div>
        )}

        {fwError && <p className="text-xs text-danger mb-2" role="alert">{fwError}</p>}
        {fwNote && <p className="text-xs text-[var(--text-muted)] mb-2">{fwNote}</p>}

        <div className="flex gap-2 mb-3">
          <button onClick={enableFirewall} disabled={fwBusy || !cfg?.provider} className="btn-primary text-xs px-3 py-1.5 disabled:opacity-40">
            {fwBusy ? 'Working…' : 'Enable (dry run)'}
          </button>
          <button onClick={disableFirewall} disabled={fwBusy} className="btn-secondary text-xs px-3 py-1.5 disabled:opacity-40">
            Disable
          </button>
          {status?.ruleset && (
            <button onClick={() => setShowRuleset(v => !v)} className="btn-secondary text-xs px-3 py-1.5">
              {showRuleset ? 'Hide ruleset' : 'Show ruleset'}
            </button>
          )}
        </div>

        {showRuleset && status?.ruleset && (
          <pre className="text-[12px] font-mono text-[var(--text-tertiary)] bg-[var(--bg-elevated)] rounded-lg px-3 py-2 overflow-x-auto whitespace-pre">
            {status.ruleset}
          </pre>
        )}
      </div>
    </Section>
  )
}
