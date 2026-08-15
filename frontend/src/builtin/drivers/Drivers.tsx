import { useState, useEffect } from 'react'

function isRecord(x: unknown): x is Record<string, unknown> {
  return typeof x === 'object' && x !== null
}

/** Message from a thrown value — errors here are `unknown` in strict mode's
 *  catch clauses, never blindly assumed to be an Error. */
function errMessage(err: unknown): string {
  return err instanceof Error ? err.message : String(err)
}

/** A hardware device row from GET /api/drivers. */
interface DriverDevice {
  id: string
  name?: string
  vendor?: string
  bus?: string
  class?: string
  driver?: string
  driver_state?: string
  module?: string
}

function isDriverDevice(x: unknown): x is DriverDevice {
  return isRecord(x) && typeof x.id === 'string'
}

/** A loaded kernel module row from GET /api/drivers. */
interface KernelModule {
  name: string
  size: number
  used_by?: string
}

function isKernelModule(x: unknown): x is KernelModule {
  return isRecord(x) && typeof x.name === 'string' && typeof x.size === 'number'
}

interface DriversStatus {
  devices: DriverDevice[]
  modules: KernelModule[]
  kernel?: string
}

/** Narrows the untrusted GET /api/drivers response into a {@link DriversStatus},
 *  dropping any device/module row that doesn't have its required fields. */
function toStatus(x: unknown): DriversStatus {
  if (!isRecord(x)) return { devices: [], modules: [] }
  return {
    devices: Array.isArray(x.devices) ? x.devices.filter(isDriverDevice) : [],
    modules: Array.isArray(x.modules) ? x.modules.filter(isKernelModule) : [],
    kernel: typeof x.kernel === 'string' ? x.kernel : undefined,
  }
}

interface ActionMsg {
  text: string
  type: 'info' | 'ok' | 'err'
}

const classIcons: Record<string, string> = {
  display: '🖥', network: '📡', audio: '🔊', storage: '💾',
  usb: '🔌', bridge: '🔗', serial: '📟', other: '⚙',
}

const classLabels: Record<string, string> = {
  display: 'Display', network: 'Network', audio: 'Audio', storage: 'Storage',
  usb: 'USB', bridge: 'Bridge', serial: 'Serial', other: 'Other',
}

export default function Drivers() {
  const [status, setStatus] = useState<DriversStatus | null>(null)
  const [loading, setLoading] = useState(true)
  const [tab, setTab] = useState('devices')
  const [modFilter, setModFilter] = useState('')
  const [actionMsg, setActionMsg] = useState<ActionMsg | null>(null)
  // BUILTIN-2: a failed scan used to be indistinguishable from a finished one.
  const [loadError, setLoadError] = useState<string | null>(null)

  const refresh = () => {
    setLoading(true)
    setLoadError(null)
    // The `!r.ok` check is the load-bearing half of this fix. /api/drivers
    // answering 500 with a JSON error body used to parse cleanly, and
    // toStatus() maps any unrecognised shape to { devices: [], modules: [] } —
    // so a box whose driver service was down rendered "No devices detected",
    // telling the user their hardware does not exist. A wrong answer stated
    // confidently is worse than an error.
    fetch('/api/drivers')
      .then(r => {
        if (!r.ok) throw new Error(`HTTP ${r.status}`)
        return r.json()
      })
      .then((s: unknown) => {
        setStatus(toStatus(s))
        setLoading(false)
      })
      .catch((e: unknown) => {
        // Leaving `status` null with `loading` false made every content branch
        // below false, so the body rendered as an empty rectangle under the
        // header — no data, no message, no reason.
        setLoadError(errMessage(e))
        setLoading(false)
      })
  }

  useEffect(() => { refresh() }, [])

  const loadMod = async (name: string) => {
    setActionMsg({ text: `Loading ${name}...`, type: 'info' })
    try {
      const res = await fetch('/api/drivers/load', {
        method: 'POST', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ module: name }),
      })
      if (!res.ok) {
        const body: unknown = await res.json()
        throw new Error(isRecord(body) && typeof body.error === 'string' ? body.error : 'Request failed')
      }
      setActionMsg({ text: `${name} loaded`, type: 'ok' })
      refresh()
    } catch (e) {
      setActionMsg({ text: `Failed: ${errMessage(e)}`, type: 'err' })
    }
  }

  const unloadMod = async (name: string) => {
    setActionMsg({ text: `Unloading ${name}...`, type: 'info' })
    try {
      const res = await fetch('/api/drivers/unload', {
        method: 'POST', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ module: name }),
      })
      if (!res.ok) {
        const body: unknown = await res.json()
        throw new Error(isRecord(body) && typeof body.error === 'string' ? body.error : 'Request failed')
      }
      setActionMsg({ text: `${name} unloaded`, type: 'ok' })
      refresh()
    } catch (e) {
      setActionMsg({ text: `Failed: ${errMessage(e)}`, type: 'err' })
    }
  }

  // Group devices by class
  const grouped: Record<string, DriverDevice[]> = {}
  if (status?.devices) {
    for (const d of status.devices) {
      const cls = d.class || 'other'
      if (!grouped[cls]) grouped[cls] = []
      grouped[cls].push(d)
    }
  }

  const filteredModules = status?.modules?.filter(
    m => !modFilter || m.name.toLowerCase().includes(modFilter.toLowerCase())
  ) || []

  return (
    <div className="h-full flex flex-col bg-neutral-950 text-neutral-200">
      {/* Header */}
      <div className="shrink-0 border-b border-neutral-800/50 px-4 sm:px-5 pt-4 pb-3">
        <div className="flex items-start justify-between gap-3 mb-3">
          <div className="min-w-0">
            <h1 className="text-base font-semibold tracking-tight">Additional Drivers</h1>
            <p className="text-xs text-neutral-500 mt-0.5 truncate">
              Hardware devices &amp; kernel modules{status?.kernel ? ` — ${status.kernel}` : ''}
            </p>
          </div>
          <button onClick={refresh} disabled={loading}
            className="shrink-0 flex items-center gap-1.5 px-3 py-1.5 text-xs font-medium bg-neutral-800 hover:bg-neutral-700 rounded-lg transition-colors duration-(--motion-fast) disabled:opacity-40">
            <svg className={`w-3.5 h-3.5 ${loading ? 'animate-spin' : ''}`} fill="none" viewBox="0 0 24 24" stroke="currentColor" strokeWidth={2}>
              <path strokeLinecap="round" strokeLinejoin="round" d="M4 4v5h.582m15.356 2A8.001 8.001 0 004.582 9m0 0H9m11 11v-5h-.581m0 0a8.003 8.003 0 01-15.357-2m15.357 2H15" />
            </svg>
            <span>{loading ? 'Scanning...' : 'Rescan'}</span>
          </button>
        </div>

        {/* Tabs */}
        <div className="flex gap-1 p-0.5 rounded-lg bg-neutral-900/50 w-fit">
          {[{ id: 'devices', label: 'Devices' }, { id: 'modules', label: 'Kernel Modules' }].map(t => (
            <button key={t.id} onClick={() => setTab(t.id)}
              aria-pressed={tab === t.id}
              className={`px-3 py-1.5 text-xs font-medium rounded-md transition-colors duration-(--motion-fast) ${
                tab === t.id ? 'bg-[var(--bg-active)] text-[var(--text-primary)] shadow-sm' : 'text-neutral-500 hover:text-neutral-300'}`}>
              {t.label}
              {t.id === 'modules' && status?.modules && (
                <span className="ml-1.5 tabular-nums text-neutral-600">{status.modules.length}</span>
              )}
            </button>
          ))}
        </div>
      </div>

      {/* Action message */}
      {actionMsg && (
        <div className={`mx-4 sm:mx-5 mt-3 flex items-center gap-2 px-3 py-2 rounded-lg border text-xs font-medium animate-[fadeIn_0.12s_ease-out] ${
          actionMsg.type === 'ok' ? 'bg-success-soft border-success-soft text-success' :
          actionMsg.type === 'err' ? 'bg-danger-soft border-danger-soft text-danger' :
          'accent-bg-soft accent-border-soft accent-text'}`}>
          {actionMsg.type === 'info' && <span className="spinner w-3.5 h-3.5 rounded-full shrink-0" />}
          <span className="min-w-0 truncate">{actionMsg.text}</span>
        </div>
      )}

      {/* Content */}
      <div className="flex-1 overflow-y-auto p-4 sm:p-5">
        {loading && !status && !loadError && (
          <div className="flex flex-col items-center justify-center gap-3 py-16 text-neutral-500">
            <span className="spinner w-7 h-7 rounded-full" />
            <span className="text-sm">Detecting hardware...</span>
          </div>
        )}

        {/* No spinner here, deliberately: this is a verdict, not progress. */}
        {loadError && !loading && (
          <div role="alert" className="flex flex-col items-center justify-center gap-2 py-16 text-center px-6">
            <span aria-hidden="true" className="text-2xl leading-none text-neutral-700">⚠</span>
            <span className="text-sm text-neutral-300">Could not scan hardware</span>
            <span className="text-xs text-neutral-500 max-w-sm break-words">
              The box did not answer the driver scan ({loadError}). Your devices are still there — this is a
              problem reading them.
            </span>
            <button onClick={refresh}
              className="mt-2 px-3 py-1.5 text-xs font-medium bg-neutral-800 hover:bg-neutral-700 rounded-lg transition-colors duration-(--motion-fast)">
              Rescan
            </button>
          </div>
        )}

        {tab === 'devices' && status && (
          <div className="space-y-6">
            {Object.entries(grouped).sort(([a], [b]) => a.localeCompare(b)).map(([cls, devices]) => (
              <div key={cls}>
                <h3 className="text-[12px] font-semibold uppercase tracking-wider text-neutral-500 mb-2 flex items-center gap-2">
                  <span className="text-sm leading-none">{classIcons[cls] || '⚙'}</span>
                  {classLabels[cls] || cls}
                  <span className="text-neutral-700 tabular-nums">{devices.length}</span>
                </h3>
                <div className="space-y-px rounded-xl overflow-hidden border border-neutral-800/50">
                  {devices.map((d, i) => (
                    <div key={d.id + i} className="flex items-center justify-between gap-3 px-3 sm:px-4 py-3 bg-neutral-900/40 hover:bg-neutral-900/70 transition-colors duration-(--motion-fast)">
                      <div className="min-w-0 flex-1">
                        <div className="text-sm truncate text-neutral-200">{d.name || d.id}</div>
                        <div className="text-[12px] text-neutral-600 mt-0.5 flex items-center gap-2 sm:gap-3 min-w-0">
                          {d.vendor && <span className="truncate">{d.vendor}</span>}
                          <span className="text-neutral-700 font-mono shrink-0">{d.bus}:{d.id}</span>
                        </div>
                      </div>
                      <div className="shrink-0 flex items-center gap-2 sm:gap-3">
                        {d.driver && (
                          <span className="hidden sm:inline text-xs text-neutral-500 font-mono truncate max-w-[10rem]">{d.driver}</span>
                        )}
                        <span className={`text-[12px] px-2 py-0.5 rounded-full font-medium ${
                          d.driver_state === 'active' ? 'bg-success-soft text-success' :
                          d.driver_state === 'available' ? 'bg-warning-soft text-warning' :
                          'bg-neutral-800 text-neutral-500'}`}>
                          {d.driver_state}
                        </span>
                        {d.module && d.driver_state !== 'active' && (
                          // d.module is checked truthy by the guard just above, but that
                          // narrowing doesn't cross into this onClick closure — asserted,
                          // not re-guarded, to keep the click handler trivial.
                          <button onClick={() => loadMod(d.module!)}
                            className="text-[12px] font-medium accent-text hover-accent-text rounded-md px-1.5 py-0.5 hover-accent-bg-soft transition-colors duration-(--motion-fast)">
                            Load
                          </button>
                        )}
                      </div>
                    </div>
                  ))}
                </div>
              </div>
            ))}
            {Object.keys(grouped).length === 0 && (
              <div className="flex flex-col items-center justify-center gap-3 py-16 text-neutral-500">
                <svg className="w-10 h-10 text-neutral-700" fill="none" viewBox="0 0 24 24" stroke="currentColor" strokeWidth={1.4}>
                  <path strokeLinecap="round" strokeLinejoin="round" d="M9 3v2m6-2v2M9 19v2m6-2v2M5 9H3m2 6H3m18-6h-2m2 6h-2M7 19h10a2 2 0 002-2V7a2 2 0 00-2-2H7a2 2 0 00-2 2v10a2 2 0 002 2zM9 9h6v6H9V9z" />
                </svg>
                <span className="text-sm">No devices detected</span>
              </div>
            )}
          </div>
        )}

        {tab === 'modules' && status && (
          <div>
            <div className="relative mb-4">
              <svg className="absolute left-3 top-1/2 -translate-y-1/2 w-4 h-4 text-neutral-600 pointer-events-none" fill="none" viewBox="0 0 24 24" stroke="currentColor" strokeWidth={2}>
                <path strokeLinecap="round" strokeLinejoin="round" d="M21 21l-6-6m2-5a7 7 0 11-14 0 7 7 0 0114 0z" />
              </svg>
              <input
                type="text" value={modFilter} onChange={e => setModFilter(e.target.value)}
                placeholder="Filter modules..."
                className="w-full pl-10 pr-3 py-2 bg-neutral-900/60 border border-neutral-800/50 rounded-lg text-sm text-neutral-200 outline-none placeholder:text-neutral-400 focus:border-neutral-700 focus:bg-neutral-900/80 transition-colors duration-(--motion-fast)"
              />
            </div>
            {filteredModules.length === 0 ? (
              <div className="flex flex-col items-center justify-center gap-2 py-16 text-neutral-500">
                <span className="text-sm">{modFilter ? `No modules match "${modFilter}"` : 'No modules loaded'}</span>
              </div>
            ) : (
              <div className="space-y-px rounded-xl overflow-hidden border border-neutral-800/50">
                {filteredModules.slice(0, 200).map(m => (
                  <div key={m.name} className="flex items-center justify-between gap-3 px-3 sm:px-4 py-2.5 bg-neutral-900/40 hover:bg-neutral-900/70 transition-colors duration-(--motion-fast)">
                    <div className="min-w-0 flex-1">
                      <span className="text-sm font-mono text-neutral-200">{m.name}</span>
                      <span className="text-[12px] text-neutral-600 ml-3 tabular-nums">{m.size} bytes</span>
                      {m.used_by && m.used_by !== '0' && (
                        <span className="text-[12px] text-neutral-700 ml-2">used by {m.used_by}</span>
                      )}
                    </div>
                    <div className="flex items-center gap-2 shrink-0">
                      <span className="text-[12px] px-2 py-0.5 rounded-full bg-success-soft text-success font-medium">loaded</span>
                      <button onClick={() => unloadMod(m.name)}
                        className="text-[12px] font-medium text-danger rounded-md px-1.5 py-0.5 hover:bg-[color-mix(in_srgb,var(--status-danger)_16%,transparent)] transition-colors duration-(--motion-fast)">
                        Unload
                      </button>
                    </div>
                  </div>
                ))}
              </div>
            )}
            {filteredModules.length > 200 && (
              <p className="text-xs text-neutral-600 mt-2 text-center tabular-nums">
                Showing 200 of {filteredModules.length} — use filter to narrow
              </p>
            )}
          </div>
        )}
      </div>
    </div>
  )
}
