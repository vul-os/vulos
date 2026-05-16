import { useState, useEffect } from 'react'
import DiskMap from './DiskMap'

function fmtSize(bytes) {
  if (bytes == null || bytes <= 0) return '—'
  const units = ['B', 'KB', 'MB', 'GB', 'TB']
  let v = bytes, i = 0
  while (v >= 1024 && i < units.length - 1) { v /= 1024; i++ }
  return `${v.toFixed(i >= 2 ? 1 : 0)} ${units[i]}`
}

function DiskTypeIcon({ type }) {
  // type: 'nvme' | 'ssd' | 'hdd' | 'usb' | 'mmc' | 'unknown'
  const t = (type || '').toLowerCase()
  if (t === 'nvme') return (
    <span className="text-indigo-400 text-xs font-bold tracking-tight">NVMe</span>
  )
  if (t === 'ssd') return (
    <span className="text-blue-400 text-xs font-bold tracking-tight">SSD</span>
  )
  if (t === 'hdd') return (
    <svg className="w-4 h-4 text-neutral-400" viewBox="0 0 16 16" fill="currentColor">
      <rect x="2" y="4" width="12" height="8" rx="1.5" stroke="currentColor" strokeWidth="1" fill="none" />
      <circle cx="11" cy="8" r="1.5" fill="currentColor" />
    </svg>
  )
  if (t === 'usb') return (
    <span className="text-amber-400 text-xs font-bold tracking-tight">USB</span>
  )
  if (t === 'mmc') return (
    <span className="text-teal-400 text-xs font-bold tracking-tight">MMC</span>
  )
  return <span className="text-xs text-neutral-600">?</span>
}

export default function DiskSelection({ onSelect, onBack }) {
  const [disks, setDisks] = useState(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState(null)
  const [selected, setSelected] = useState(null)
  const [confirmed, setConfirmed] = useState(false)
  const [showConfirm, setShowConfirm] = useState(false)

  const loadDisks = () => {
    setLoading(true)
    setError(null)
    fetch('/api/installer/disks')
      .then(r => {
        if (!r.ok) throw new Error(`HTTP ${r.status}`)
        return r.json()
      })
      .then(d => {
        setDisks(d.disks || d || [])
        setLoading(false)
      })
      .catch(e => {
        setError(e.message)
        setLoading(false)
      })
  }

  useEffect(() => { loadDisks() }, [])

  const handleChoose = (disk) => {
    setSelected(disk)
    setShowConfirm(false)
    setConfirmed(false)
  }

  const handleContinue = () => {
    if (!selected) return
    setShowConfirm(true)
  }

  const handleConfirmInstall = () => {
    if (!confirmed) return
    onSelect(selected)
  }

  return (
    <div className="flex-1 flex flex-col min-h-0">
      {/* Header */}
      <div className="shrink-0 px-6 pt-6 pb-4 border-b border-neutral-800/50">
        <button
          onClick={onBack}
          className="text-xs text-neutral-500 hover:text-neutral-300 transition-colors mb-3 flex items-center gap-1"
        >
          <span>&larr;</span> Back
        </button>
        <h2 className="text-lg font-semibold text-neutral-100">Select destination disk</h2>
        <p className="text-xs text-neutral-500 mt-1">
          All data on the selected disk will be erased. Choose carefully.
        </p>
      </div>

      {/* Disk list */}
      <div className="flex-1 overflow-y-auto min-h-0 p-6">
        {loading && (
          <div className="flex items-center justify-center py-16 gap-3">
            <span className="w-4 h-4 rounded-full border-2 border-indigo-500 border-t-transparent animate-spin" />
            <span className="text-sm text-neutral-500">Scanning disks…</span>
          </div>
        )}

        {error && (
          <div className="rounded-lg border border-red-900/40 bg-red-950/20 p-4 text-center space-y-3">
            <p className="text-sm text-red-400">Failed to load disks: {error}</p>
            <button
              onClick={loadDisks}
              className="text-xs text-neutral-400 hover:text-white underline transition-colors"
            >
              Retry
            </button>
          </div>
        )}

        {!loading && !error && disks?.length === 0 && (
          <div className="rounded-lg border border-neutral-800 p-8 text-center">
            <p className="text-sm text-neutral-500">No eligible disks found.</p>
            <p className="text-xs text-neutral-700 mt-1">
              Connect an internal disk and click Refresh.
            </p>
            <button
              onClick={loadDisks}
              className="mt-4 text-xs text-indigo-400 hover:text-indigo-300 underline"
            >
              Refresh
            </button>
          </div>
        )}

        {!loading && !error && disks && disks.length > 0 && (
          <div className="space-y-3">
            {disks.map((disk) => {
              const isSelected = selected?.path === disk.path
              const isLive = disk.is_live_usb

              return (
                <button
                  key={disk.path}
                  onClick={() => !isLive && handleChoose(disk)}
                  disabled={isLive}
                  className={`w-full text-left rounded-xl border transition-all p-4 focus:outline-none focus:ring-2 focus:ring-indigo-500
                    ${isLive
                      ? 'border-neutral-800/30 bg-neutral-900/20 opacity-50 cursor-not-allowed'
                      : isSelected
                        ? 'border-indigo-500/60 bg-indigo-950/30 ring-1 ring-indigo-500/30'
                        : 'border-neutral-800/50 bg-neutral-900/30 hover:border-neutral-700 hover:bg-neutral-900/50'
                    }`}
                >
                  {/* Disk header row */}
                  <div className="flex items-start justify-between gap-3 mb-3">
                    <div className="flex items-center gap-2.5">
                      {/* Selection indicator */}
                      <span className={`shrink-0 w-4 h-4 rounded-full border-2 flex items-center justify-center
                        ${isSelected ? 'border-indigo-500 bg-indigo-500' : 'border-neutral-700'}`}>
                        {isSelected && (
                          <span className="w-1.5 h-1.5 rounded-full bg-white" />
                        )}
                      </span>

                      <div>
                        <div className="flex items-center gap-2">
                          <span className="text-sm font-semibold text-neutral-100 font-mono">
                            {disk.path}
                          </span>
                          <DiskTypeIcon type={disk.type} />
                          {isLive && (
                            <span className="text-[10px] bg-amber-900/40 text-amber-400 border border-amber-800/40 px-1.5 py-0.5 rounded">
                              Live USB — cannot install here
                            </span>
                          )}
                        </div>
                        <div className="text-xs text-neutral-500 mt-0.5">
                          {disk.model || 'Unknown model'} &middot; {fmtSize(disk.size_bytes)}
                        </div>
                      </div>
                    </div>

                    <div className="shrink-0 text-right">
                      <div className="text-sm font-medium text-neutral-200">
                        {fmtSize(disk.size_bytes)}
                      </div>
                      {disk.partitions?.length > 0 && (
                        <div className="text-[10px] text-neutral-600 mt-0.5">
                          {disk.partitions.length} partition{disk.partitions.length !== 1 ? 's' : ''}
                        </div>
                      )}
                    </div>
                  </div>

                  {/* Visual disk map */}
                  <DiskMap disk={disk} />

                  {/* Partition list */}
                  {disk.partitions?.length > 0 && (
                    <div className="mt-3 space-y-1">
                      {disk.partitions.map((p, i) => (
                        <div key={i} className="flex items-center gap-2 text-[11px] text-neutral-500">
                          <span className="font-mono">{p.name || p.path}</span>
                          {p.fstype && <span className="text-neutral-700">{p.fstype}</span>}
                          {p.mountpoint && (
                            <span className="text-neutral-700 font-mono">{p.mountpoint}</span>
                          )}
                          <span className="ml-auto">{fmtSize(p.size_bytes)}</span>
                        </div>
                      ))}
                    </div>
                  )}
                </button>
              )
            })}

            <button
              onClick={loadDisks}
              className="w-full text-center py-2 text-xs text-neutral-600 hover:text-neutral-400 transition-colors"
            >
              Refresh disk list
            </button>
          </div>
        )}
      </div>

      {/* Confirm panel */}
      {showConfirm && selected && (
        <div className="shrink-0 border-t border-red-900/30 bg-red-950/10 p-5 space-y-4">
          <div className="space-y-1">
            <p className="text-sm font-semibold text-red-400">
              Warning: this will erase {selected.path}
            </p>
            <p className="text-xs text-neutral-500">
              All data on <span className="font-mono text-neutral-300">{selected.path}</span>
              {' '}({fmtSize(selected.size_bytes)}) will be permanently deleted.
              This cannot be undone.
            </p>
          </div>

          <label className="flex items-start gap-2.5 cursor-pointer">
            <input
              type="checkbox"
              checked={confirmed}
              onChange={e => setConfirmed(e.target.checked)}
              className="mt-0.5 accent-red-500"
            />
            <span className="text-xs text-neutral-400 leading-relaxed">
              I understand that all data on this disk will be erased and I have
              backed up anything I want to keep.
            </span>
          </label>

          <div className="flex gap-3">
            <button
              onClick={() => setShowConfirm(false)}
              className="flex-1 py-2 text-sm text-neutral-400 hover:text-white border border-neutral-700 hover:border-neutral-600 rounded-lg transition-colors"
            >
              Cancel
            </button>
            <button
              onClick={handleConfirmInstall}
              disabled={!confirmed}
              className={`flex-1 py-2 text-sm font-semibold rounded-lg transition-colors
                ${confirmed
                  ? 'bg-red-600 hover:bg-red-500 text-white'
                  : 'bg-neutral-800 text-neutral-600 cursor-not-allowed'
                }`}
            >
              Erase and install
            </button>
          </div>
        </div>
      )}

      {/* Footer: Continue button (before confirm) */}
      {!showConfirm && (
        <div className="shrink-0 border-t border-neutral-800/50 p-5 flex items-center justify-between gap-4">
          <span className="text-xs text-neutral-600">
            {selected
              ? `Selected: ${selected.path} · ${fmtSize(selected.size_bytes)}`
              : 'No disk selected'}
          </span>
          <button
            onClick={handleContinue}
            disabled={!selected}
            className={`px-6 py-2 text-sm font-semibold rounded-lg transition-colors
              ${selected
                ? 'bg-indigo-600 hover:bg-indigo-500 text-white'
                : 'bg-neutral-800 text-neutral-600 cursor-not-allowed'
              }`}
          >
            Continue
          </button>
        </div>
      )}
    </div>
  )
}
