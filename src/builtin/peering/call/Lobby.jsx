import { useState, useEffect, useCallback } from 'react'

/**
 * Lobby — Pre-call bandwidth visibility and SFU host selection.
 *
 * Props:
 *   participants  – array of { id, displayName, server? }
 *                  The first entry is treated as the local user (initiator).
 *   initiatorId   – id of the call initiator (defaults host selection)
 *   onStart       – fn({ hostId }) called when user clicks "Start Call"
 *   onCancel      – fn() called when user cancels
 *
 * Roadmap math (PEERING.md § Bandwidth Visibility):
 *   video capacity  = floor(host_upload_mbps / 3)        (Last-N=6, 360p, 500 kbps/stream forwarded)
 *   audio capacity  = floor(host_upload_mbps / 0.096)    (Opus 32 kbps + mixing overhead ~96 kbps per 3-speaker mix)
 *   hard cap        = 50 participants
 */

// Capacity estimate per roadmap formula
function estimateCapacity(uploadMbps) {
  if (!uploadMbps || uploadMbps <= 0) return { video: 0, audio: 0 }
  const video = Math.min(50, Math.floor(uploadMbps / 3))
  // Each participant receives a mixed audio stream at ~96 kbps (3 speakers, Opus).
  // SFU must forward N-1 audio streams total.  Simplified: upload / 0.096 kbps per stream.
  const audio = Math.min(200, Math.floor(uploadMbps / 0.096))
  return { video, audio }
}

function fmtMbps(val) {
  if (val == null) return '—'
  return `${Number(val).toFixed(1)} Mbps`
}

function fmtMs(val) {
  if (val == null) return '—'
  return `${Math.round(val)} ms`
}

// Fetch own bandwidth from the local Vula server
async function fetchLocalBandwidth() {
  const res = await fetch('/api/peering/bandwidth')
  if (!res.ok) throw new Error(`HTTP ${res.status}`)
  return res.json() // { upload_mbps, download_mbps, latency_ms }
}

// Fetch a peer's bandwidth from their Vula server
async function fetchPeerBandwidth(server) {
  if (!server) throw new Error('no server address')
  const url = `https://${server}/api/peering/bandwidth`
  const res = await fetch(url)
  if (!res.ok) throw new Error(`HTTP ${res.status}`)
  return res.json()
}

export default function Lobby({ participants = [], initiatorId, onStart, onCancel }) {
  // bandwidth map: participantId → { upload_mbps, download_mbps, latency_ms, loading, error }
  const [bwMap, setBwMap] = useState(() => {
    const m = {}
    for (const p of participants) {
      m[p.id] = { loading: true, error: null, upload_mbps: null, download_mbps: null, latency_ms: null }
    }
    return m
  })

  const [hostId, setHostId] = useState(initiatorId || participants[0]?.id || '')
  const [starting, setStarting] = useState(false)

  // Helper: update one participant's bandwidth entry
  const setBw = useCallback((id, patch) => {
    setBwMap(prev => ({ ...prev, [id]: { ...prev[id], ...patch } }))
  }, [])

  // Fetch bandwidth for all participants on mount
  useEffect(() => {
    if (!participants.length) return

    participants.forEach((p, idx) => {
      const isLocal = idx === 0 || p.id === initiatorId

      const doFetch = async () => {
        try {
          const data = isLocal
            ? await fetchLocalBandwidth()
            : await fetchPeerBandwidth(p.server)
          setBw(p.id, { loading: false, error: null, ...data })
        } catch {
          setBw(p.id, { loading: false, error: 'unavailable' })
        }
      }

      doFetch()
    })
  // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []) // intentionally run once on mount — participants/initiatorId are stable at lobby entry

  const hostEntry = bwMap[hostId] || {}
  const capacity = estimateCapacity(hostEntry.upload_mbps)

  function handleStart() {
    if (starting) return
    setStarting(true)
    try {
      onStart?.({ hostId })
    } finally {
      setStarting(false)
    }
  }

  return (
    <div className="flex flex-col bg-neutral-950 text-neutral-100 rounded-2xl border border-neutral-800/60 overflow-hidden shadow-2xl" style={{ minWidth: 480, maxWidth: 600 }}>
      {/* Header */}
      <div className="flex items-center justify-between px-5 py-4 border-b border-neutral-800/40">
        <div>
          <h2 className="text-sm font-semibold tracking-tight">Start Group Call</h2>
          <p className="text-[11px] text-neutral-500 mt-0.5">
            {participants.length} participant{participants.length !== 1 ? 's' : ''}
          </p>
        </div>
        <button
          onClick={onCancel}
          className="w-6 h-6 flex items-center justify-center rounded-full text-neutral-500 hover:text-neutral-200 hover:bg-neutral-800 transition-colors text-xs"
          aria-label="Cancel"
        >
          &#x2715;
        </button>
      </div>

      {/* Bandwidth Table */}
      <div className="px-4 pt-3 pb-2">
        <BandwidthTable
          participants={participants}
          bwMap={bwMap}
          hostId={hostId}
          initiatorId={initiatorId || participants[0]?.id}
        />
      </div>

      {/* Host Selector */}
      <div className="px-4 py-3 border-t border-neutral-800/30 flex flex-col gap-2">
        <HostSelector
          participants={participants}
          bwMap={bwMap}
          hostId={hostId}
          onChange={setHostId}
        />
      </div>

      {/* Capacity Estimate */}
      <CapacityBar capacity={capacity} hostEntry={hostEntry} />

      {/* Actions */}
      <div className="flex items-center justify-end gap-2 px-4 py-3 border-t border-neutral-800/30">
        <button
          onClick={onCancel}
          className="px-4 py-1.5 text-xs text-neutral-400 hover:text-neutral-200 rounded-lg transition-colors"
        >
          Cancel
        </button>
        <button
          onClick={handleStart}
          disabled={starting || !hostId}
          className="px-4 py-1.5 text-xs font-medium bg-blue-600 hover:bg-blue-500 disabled:bg-neutral-700 disabled:text-neutral-500 text-white rounded-lg transition-colors"
        >
          {starting ? 'Starting…' : 'Start Call'}
        </button>
      </div>
    </div>
  )
}

/* ── Bandwidth Table ── */
function BandwidthTable({ participants, bwMap, hostId, initiatorId }) {
  const cols = [
    { label: 'Participant', w: '1fr', align: '' },
    { label: '▲ Upload',   w: '100px', align: 'text-right' },
    { label: '▼ Download', w: '100px', align: 'text-right' },
    { label: 'Latency',        w: '80px',  align: 'text-right' },
  ]
  const gridTemplate = cols.map(c => c.w).join(' ')

  return (
    <div className="rounded-xl border border-neutral-800/60 bg-neutral-900/40 overflow-hidden">
      {/* Header */}
      <div
        className="grid gap-3 px-3 py-1.5 text-[10px] uppercase tracking-wider text-neutral-600 border-b border-neutral-800/40 bg-neutral-900/80"
        style={{ gridTemplateColumns: gridTemplate }}
      >
        {cols.map(c => (
          <span key={c.label} className={c.align}>{c.label}</span>
        ))}
      </div>
      {/* Rows */}
      {participants.map((p) => {
        const bw = bwMap[p.id] || {}
        const isHost = p.id === hostId
        const isInitiator = p.id === initiatorId

        return (
          <div
            key={p.id}
            className={`grid gap-3 items-center px-3 py-2 text-[11px] border-b border-neutral-800/20 last:border-b-0 transition-colors ${isHost ? 'bg-blue-950/30' : 'hover:bg-neutral-800/20'}`}
            style={{ gridTemplateColumns: gridTemplate }}
          >
            {/* Name + badges */}
            <div className="flex items-center gap-2 min-w-0">
              <span className="flex-shrink-0 w-6 h-6 rounded-full bg-neutral-700 flex items-center justify-center text-[10px] text-neutral-400 select-none">
                {(p.displayName || p.id || '?')[0].toUpperCase()}
              </span>
              <span className="truncate text-neutral-200">{p.displayName || p.id}</span>
              <div className="flex items-center gap-1 flex-shrink-0">
                {isInitiator && (
                  <span className="text-[9px] px-1.5 py-0.5 rounded bg-neutral-800 text-neutral-400 leading-none">you</span>
                )}
                {isHost && (
                  <span className="text-[9px] px-1.5 py-0.5 rounded bg-blue-900/70 text-blue-300 leading-none">host</span>
                )}
              </div>
            </div>

            {/* Upload */}
            <BwCell loading={bw.loading} error={bw.error} value={bw.upload_mbps} format={fmtMbps} goodThreshold={10} />

            {/* Download */}
            <BwCell loading={bw.loading} error={bw.error} value={bw.download_mbps} format={fmtMbps} goodThreshold={25} />

            {/* Latency */}
            <BwCell loading={bw.loading} error={bw.error} value={bw.latency_ms} format={fmtMs} latency />
          </div>
        )
      })}
    </div>
  )
}

function BwCell({ loading, error, value, format, goodThreshold, latency }) {
  if (loading) {
    return (
      <span className="flex justify-end">
        <span className="w-3 h-3 border border-neutral-700 border-t-neutral-400 rounded-full animate-spin" />
      </span>
    )
  }
  if (error || value == null) {
    return <span className="text-right text-neutral-600 font-mono">—</span>
  }

  let colorClass = 'text-neutral-300'
  if (latency) {
    colorClass = value <= 30 ? 'text-emerald-400' : value <= 80 ? 'text-amber-400' : 'text-red-400'
  } else if (goodThreshold != null) {
    colorClass = value >= goodThreshold * 4 ? 'text-emerald-400'
      : value >= goodThreshold ? 'text-neutral-300'
      : value >= goodThreshold / 2 ? 'text-amber-400'
      : 'text-red-400'
  }

  return (
    <span className={`text-right font-mono text-[11px] ${colorClass}`}>{format(value)}</span>
  )
}

/* ── Host Selector ── */
function HostSelector({ participants, bwMap, hostId, onChange }) {
  return (
    <div className="flex items-center gap-3">
      <span className="text-[11px] text-neutral-500 shrink-0">SFU Host</span>
      <select
        value={hostId}
        onChange={e => onChange(e.target.value)}
        className="flex-1 bg-neutral-900 border border-neutral-700 rounded-lg px-3 py-1.5 text-[11px] text-neutral-200 outline-none focus:border-blue-600 transition-colors cursor-pointer appearance-none"
        style={{ backgroundImage: 'none' }}
        aria-label="Select SFU host"
      >
        {participants.map(p => {
          const bw = bwMap[p.id] || {}
          const uploadLabel = bw.loading
            ? '…'
            : bw.error || bw.upload_mbps == null
            ? 'unavailable'
            : `${Number(bw.upload_mbps).toFixed(1)} Mbps up`
          return (
            <option key={p.id} value={p.id}>
              {p.displayName || p.id} — {uploadLabel}
            </option>
          )
        })}
      </select>
      <UploadQualityHint bw={bwMap[hostId]} />
    </div>
  )
}

function UploadQualityHint({ bw }) {
  if (!bw || bw.loading || bw.error || bw.upload_mbps == null) return null
  const mbps = bw.upload_mbps
  if (mbps >= 50) return <span className="text-[10px] text-emerald-400 shrink-0">Excellent</span>
  if (mbps >= 25) return <span className="text-[10px] text-blue-400 shrink-0">Good</span>
  if (mbps >= 10) return <span className="text-[10px] text-amber-400 shrink-0">Fair</span>
  return <span className="text-[10px] text-red-400 shrink-0">Limited</span>
}

/* ── Capacity Bar ── */
function CapacityBar({ capacity, hostEntry }) {
  const hasData = !hostEntry.loading && !hostEntry.error && hostEntry.upload_mbps != null

  return (
    <div className="px-4 pb-1">
      <div className="rounded-xl border border-neutral-800/40 bg-neutral-900/30 px-3 py-2">
        {!hasData ? (
          <span className="text-[11px] text-neutral-600">
            {hostEntry.loading ? 'Measuring host bandwidth…' : 'Host bandwidth unavailable — capacity estimate not possible'}
          </span>
        ) : (
          <div className="flex items-center justify-between gap-4">
            <div className="flex items-center gap-4">
              <CapacityChip label="Video" value={capacity.video} unit="people" icon="▣" color="text-blue-400" />
              <CapacityChip label="Audio only" value={capacity.audio} unit="people" icon="♪" color="text-emerald-400" />
            </div>
            <div className="text-[10px] text-neutral-600 text-right leading-tight">
              <span className="block">Based on {Number(hostEntry.upload_mbps).toFixed(1)} Mbps upload</span>
              <span className="block">Last-N=6, 360p @ 500 kbps</span>
            </div>
          </div>
        )}
      </div>
    </div>
  )
}

function CapacityChip({ label, value, unit, icon, color }) {
  return (
    <div className="flex items-baseline gap-1.5">
      <span className={`text-[10px] ${color}`}>{icon}</span>
      <span className="text-sm font-semibold text-neutral-100">~{value}</span>
      <span className="text-[10px] text-neutral-500">{unit}</span>
      <span className="text-[10px] text-neutral-600 ml-0.5">{label}</span>
    </div>
  )
}
