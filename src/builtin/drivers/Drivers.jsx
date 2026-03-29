import { useState, useEffect } from 'react'

const classIcons = {
  display: '🖥', network: '📡', audio: '🔊', storage: '💾',
  usb: '🔌', bridge: '🔗', serial: '📟', other: '⚙',
}

const classLabels = {
  display: 'Display', network: 'Network', audio: 'Audio', storage: 'Storage',
  usb: 'USB', bridge: 'Bridge', serial: 'Serial', other: 'Other',
}

export default function Drivers() {
  const [status, setStatus] = useState(null)
  const [loading, setLoading] = useState(true)
  const [tab, setTab] = useState('devices')
  const [modFilter, setModFilter] = useState('')
  const [actionMsg, setActionMsg] = useState(null)

  const refresh = () => {
    setLoading(true)
    fetch('/api/drivers').then(r => r.json()).then(s => {
      setStatus(s)
      setLoading(false)
    }).catch(() => setLoading(false))
  }

  useEffect(() => { refresh() }, [])

  const loadMod = async (name) => {
    setActionMsg({ text: `Loading ${name}...`, type: 'info' })
    try {
      const res = await fetch('/api/drivers/load', {
        method: 'POST', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ module: name }),
      })
      if (!res.ok) throw new Error((await res.json()).error)
      setActionMsg({ text: `${name} loaded`, type: 'ok' })
      refresh()
    } catch (e) {
      setActionMsg({ text: `Failed: ${e.message}`, type: 'err' })
    }
  }

  const unloadMod = async (name) => {
    setActionMsg({ text: `Unloading ${name}...`, type: 'info' })
    try {
      const res = await fetch('/api/drivers/unload', {
        method: 'POST', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ module: name }),
      })
      if (!res.ok) throw new Error((await res.json()).error)
      setActionMsg({ text: `${name} unloaded`, type: 'ok' })
      refresh()
    } catch (e) {
      setActionMsg({ text: `Failed: ${e.message}`, type: 'err' })
    }
  }

  // Group devices by class
  const grouped = {}
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
      <div className="shrink-0 border-b border-neutral-800/50 px-5 pt-4 pb-3">
        <div className="flex items-center justify-between mb-3">
          <div>
            <h1 className="text-base font-semibold">Additional Drivers</h1>
            <p className="text-xs text-neutral-500 mt-0.5">
              Hardware devices & kernel modules{status?.kernel ? ` — ${status.kernel}` : ''}
            </p>
          </div>
          <button onClick={refresh} disabled={loading}
            className="px-3 py-1.5 text-xs bg-neutral-800 hover:bg-neutral-700 rounded-lg transition-colors disabled:opacity-40">
            {loading ? 'Scanning...' : 'Rescan'}
          </button>
        </div>

        {/* Tabs */}
        <div className="flex gap-1">
          {[{ id: 'devices', label: 'Devices' }, { id: 'modules', label: 'Kernel Modules' }].map(t => (
            <button key={t.id} onClick={() => setTab(t.id)}
              className={`px-3 py-1.5 text-xs rounded-lg transition-colors ${
                tab === t.id ? 'bg-neutral-800 text-white' : 'text-neutral-500 hover:text-neutral-300'}`}>
              {t.label}
              {t.id === 'modules' && status?.modules && (
                <span className="ml-1.5 text-neutral-600">{status.modules.length}</span>
              )}
            </button>
          ))}
        </div>
      </div>

      {/* Action message */}
      {actionMsg && (
        <div className={`mx-5 mt-3 px-3 py-2 rounded-lg text-xs ${
          actionMsg.type === 'ok' ? 'bg-green-900/30 text-green-400' :
          actionMsg.type === 'err' ? 'bg-red-900/30 text-red-400' :
          'bg-blue-900/30 text-blue-400'}`}>
          {actionMsg.text}
        </div>
      )}

      {/* Content */}
      <div className="flex-1 overflow-y-auto p-5">
        {loading && !status && (
          <div className="text-sm text-neutral-500 py-8 text-center">Detecting hardware...</div>
        )}

        {tab === 'devices' && status && (
          <div className="space-y-6">
            {Object.entries(grouped).sort(([a], [b]) => a.localeCompare(b)).map(([cls, devices]) => (
              <div key={cls}>
                <h3 className="text-xs uppercase tracking-wider text-neutral-500 mb-2 flex items-center gap-2">
                  <span>{classIcons[cls] || '⚙'}</span>
                  {classLabels[cls] || cls}
                  <span className="text-neutral-700">{devices.length}</span>
                </h3>
                <div className="space-y-px rounded-xl overflow-hidden border border-neutral-800/50">
                  {devices.map((d, i) => (
                    <div key={d.id + i} className="flex items-center justify-between px-4 py-3 bg-neutral-900/40">
                      <div className="min-w-0 flex-1">
                        <div className="text-sm truncate">{d.name || d.id}</div>
                        <div className="text-[11px] text-neutral-600 mt-0.5 flex items-center gap-3">
                          {d.vendor && <span>{d.vendor}</span>}
                          <span className="text-neutral-700">{d.bus}:{d.id}</span>
                        </div>
                      </div>
                      <div className="shrink-0 flex items-center gap-3 ml-4">
                        {d.driver && (
                          <span className="text-xs text-neutral-500 font-mono">{d.driver}</span>
                        )}
                        <span className={`text-[10px] px-2 py-0.5 rounded-full ${
                          d.driver_state === 'active' ? 'bg-green-900/30 text-green-400' :
                          d.driver_state === 'available' ? 'bg-amber-900/30 text-amber-400' :
                          'bg-neutral-800 text-neutral-500'}`}>
                          {d.driver_state}
                        </span>
                        {d.module && d.driver_state !== 'active' && (
                          <button onClick={() => loadMod(d.module)}
                            className="text-[11px] text-blue-400 hover:text-blue-300">
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
              <div className="text-sm text-neutral-500 py-8 text-center">No devices detected</div>
            )}
          </div>
        )}

        {tab === 'modules' && status && (
          <div>
            <input
              type="text" value={modFilter} onChange={e => setModFilter(e.target.value)}
              placeholder="Filter modules..."
              className="w-full mb-4 px-3 py-2 bg-neutral-900/60 border border-neutral-800/50 rounded-lg text-sm text-neutral-200 outline-none placeholder:text-neutral-600 focus:border-neutral-700"
            />
            <div className="space-y-px rounded-xl overflow-hidden border border-neutral-800/50">
              {filteredModules.slice(0, 200).map(m => (
                <div key={m.name} className="flex items-center justify-between px-4 py-2.5 bg-neutral-900/40">
                  <div className="min-w-0 flex-1">
                    <span className="text-sm font-mono">{m.name}</span>
                    <span className="text-[11px] text-neutral-600 ml-3">{m.size} bytes</span>
                    {m.used_by && m.used_by !== '0' && (
                      <span className="text-[11px] text-neutral-700 ml-2">used by {m.used_by}</span>
                    )}
                  </div>
                  <div className="flex items-center gap-2">
                    <span className="text-[10px] px-2 py-0.5 rounded-full bg-green-900/30 text-green-400">loaded</span>
                    <button onClick={() => unloadMod(m.name)}
                      className="text-[11px] text-red-400/60 hover:text-red-400">
                      Unload
                    </button>
                  </div>
                </div>
              ))}
            </div>
            {filteredModules.length > 200 && (
              <p className="text-xs text-neutral-600 mt-2 text-center">
                Showing 200 of {filteredModules.length} — use filter to narrow
              </p>
            )}
          </div>
        )}
      </div>
    </div>
  )
}
