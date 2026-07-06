/**
 * Drop.jsx — AirDrop-style LAN file drop UI (PEER-34 + PEER-35).
 *
 * Features:
 *   - Nearby peers tile grid (polling /api/peering/drop/nearby every 10 s)
 *   - Per-peer send-file action (opens file picker, POST /api/peering/drop/send)
 *   - Per-transfer progress indicator (indeterminate spinner + byte counter)
 *   - Inbound accept / decline from /api/peering/drop/decide
 *   - Discoverability toggle (everyone / peers / nobody)
 *   - Pending-transfer badge on the tab (raised from parent via onPendingCount)
 *   - [PEER-35] Proximity code: "My Code" (generate 6-digit TTL-5-min code) +
 *     "Enter Code" (redeem a peer's code) for cross-network / no-mDNS scenarios
 *     via POST /api/peering/drop/code/generate and /api/peering/drop/code/redeem
 *
 * Dependencies: React only — no external deps.
 */
import { useState, useEffect, useCallback, useRef } from 'react'

// ---------------------------------------------------------------------------
// API helpers
// ---------------------------------------------------------------------------

async function apiFetch(url, options = {}) {
  const res = await fetch(url, {
    headers: { 'Content-Type': 'application/json', ...options.headers },
    ...options,
  })
  if (!res.ok) {
    const text = await res.text().catch(() => '')
    throw new Error(`${res.status} ${text || res.statusText}`)
  }
  return res.json()
}

function humanBytes(n) {
  if (!n) return '0 B'
  const units = ['B', 'KB', 'MB', 'GB']
  let i = 0, v = n
  while (v >= 1024 && i < units.length - 1) { v /= 1024; i++ }
  return `${v.toFixed(i > 0 ? 1 : 0)} ${units[i]}`
}

// ---------------------------------------------------------------------------
// Icons (inline SVG — no external deps)
// ---------------------------------------------------------------------------

const IconWifi = () => (
  <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8" width="18" height="18">
    <path d="M5 12.55a11 11 0 0 1 14.08 0" strokeLinecap="round" />
    <path d="M1.42 9a16 16 0 0 1 21.16 0" strokeLinecap="round" />
    <path d="M8.53 16.11a6 6 0 0 1 6.95 0" strokeLinecap="round" />
    <circle cx="12" cy="20" r="1" fill="currentColor" stroke="none" />
  </svg>
)

const IconUpload = () => (
  <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8" width="16" height="16">
    <path d="M21 15v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4" strokeLinecap="round" strokeLinejoin="round" />
    <polyline points="17 8 12 3 7 8" strokeLinecap="round" strokeLinejoin="round" />
    <line x1="12" y1="3" x2="12" y2="15" strokeLinecap="round" />
  </svg>
)

const IconCheck = () => (
  <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" width="16" height="16">
    <polyline points="20 6 9 17 4 12" strokeLinecap="round" strokeLinejoin="round" />
  </svg>
)

const IconX = () => (
  <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" width="16" height="16">
    <line x1="18" y1="6" x2="6" y2="18" strokeLinecap="round" />
    <line x1="6" y1="6" x2="18" y2="18" strokeLinecap="round" />
  </svg>
)

const IconSettings = () => (
  <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8" width="16" height="16">
    <circle cx="12" cy="12" r="3" />
    <path d="M19.4 15a1.65 1.65 0 0 0 .33 1.82l.06.06a2 2 0 0 1-2.83 2.83l-.06-.06a1.65 1.65 0 0 0-1.82-.33 1.65 1.65 0 0 0-1 1.51V21a2 2 0 0 1-4 0v-.09A1.65 1.65 0 0 0 9 19.4a1.65 1.65 0 0 0-1.82.33l-.06.06a2 2 0 0 1-2.83-2.83l.06-.06A1.65 1.65 0 0 0 4.68 15a1.65 1.65 0 0 0-1.51-1H3a2 2 0 0 1 0-4h.09A1.65 1.65 0 0 0 4.6 9a1.65 1.65 0 0 0-.33-1.82l-.06-.06a2 2 0 0 1 2.83-2.83l.06.06A1.65 1.65 0 0 0 9 4.68a1.65 1.65 0 0 0 1-1.51V3a2 2 0 0 1 4 0v.09a1.65 1.65 0 0 0 1 1.51 1.65 1.65 0 0 0 1.82-.33l.06-.06a2 2 0 0 1 2.83 2.83l-.06.06A1.65 1.65 0 0 0 19.4 9a1.65 1.65 0 0 0 1.51 1H21a2 2 0 0 1 0 4h-.09a1.65 1.65 0 0 0-1.51 1z" />
  </svg>
)

const IconHash = () => (
  <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8" width="16" height="16">
    <line x1="4"  y1="9"  x2="20" y2="9"  strokeLinecap="round" />
    <line x1="4"  y1="15" x2="20" y2="15" strokeLinecap="round" />
    <line x1="10" y1="3"  x2="8"  y2="21" strokeLinecap="round" />
    <line x1="16" y1="3"  x2="14" y2="21" strokeLinecap="round" />
  </svg>
)

// ---------------------------------------------------------------------------
// Discoverability picker
// ---------------------------------------------------------------------------

const DISC_OPTIONS = [
  { value: 'everyone', label: 'Everyone', desc: 'Visible to all Vula peers on this network' },
  { value: 'peers',    label: 'Peers',    desc: 'Visible only to approved contacts' },
  { value: 'nobody',  label: 'Nobody',   desc: 'Not discoverable via mDNS' },
]

function DiscoverabilityBadge({ value }) {
  const opt = DISC_OPTIONS.find(o => o.value === value)
  const color =
    value === 'everyone' ? 'bg-emerald-500/15 border-emerald-500/30 text-emerald-400' :
    value === 'peers'    ? 'bg-blue-500/15 border-blue-500/30 text-blue-400' :
                           'bg-neutral-700/40 border-neutral-600/40 text-neutral-500'
  return (
    <span className={`text-[11px] px-2 py-0.5 rounded-full border ${color}`}>
      {opt?.label ?? value}
    </span>
  )
}

function DiscoverabilitySettings({ current, onSave, onClose }) {
  const [selected, setSelected] = useState(current)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState(null)

  const save = async () => {
    setBusy(true)
    setError(null)
    try {
      await apiFetch('/api/peering/drop/settings', {
        method: 'PUT',
        body: JSON.stringify({ discoverability: selected }),
      })
      onSave(selected)
      onClose()
    } catch (err) {
      setError(err.message)
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="bg-neutral-900/90 border border-neutral-800/70 rounded-2xl p-5 space-y-4">
      <div className="flex items-center justify-between">
        <h3 className="text-sm font-semibold text-neutral-200">Discoverability</h3>
        <button
          onClick={onClose}
          className="text-neutral-600 hover:text-neutral-400 transition-colors"
          aria-label="Close"
        >
          <IconX />
        </button>
      </div>

      <div className="space-y-2">
        {DISC_OPTIONS.map(opt => (
          <button
            key={opt.value}
            onClick={() => setSelected(opt.value)}
            className={`w-full text-left px-4 py-3 rounded-xl border transition-colors
              ${selected === opt.value
                ? 'bg-blue-600/15 border-blue-500/40 text-blue-200'
                : 'bg-neutral-800/40 border-neutral-700/40 text-neutral-400 hover:border-neutral-600/60 hover:text-neutral-300'
              }`}
          >
            <p className="text-sm font-medium leading-snug">{opt.label}</p>
            <p className="text-[11px] mt-0.5 opacity-70">{opt.desc}</p>
          </button>
        ))}
      </div>

      {error && <p className="text-xs text-red-400">{error}</p>}

      <button
        onClick={save}
        disabled={busy || selected === current}
        className="w-full py-2 rounded-lg text-sm font-medium transition-colors
          bg-blue-600/20 border border-blue-500/30 text-blue-300
          hover:bg-blue-600/30 disabled:opacity-40 disabled:cursor-not-allowed"
      >
        {busy ? 'Saving…' : 'Save'}
      </button>
    </div>
  )
}

// ---------------------------------------------------------------------------
// Peer tile
// ---------------------------------------------------------------------------

function PeerTile({ peer, onSend }) {
  const initial = (peer.display_name || peer.vula_id || '?')[0].toUpperCase()

  return (
    <div className="flex flex-col items-center gap-2 p-4 bg-neutral-900/60 border border-neutral-800/60 rounded-2xl hover:border-neutral-700/60 transition-colors">
      {/* Avatar */}
      <div className={`w-14 h-14 rounded-full flex items-center justify-center text-xl font-bold text-white
        ${peer.is_contact
          ? 'bg-gradient-to-br from-blue-600/40 to-violet-600/40 border border-blue-500/20'
          : 'bg-neutral-700/60 border border-neutral-600/30'}`}
      >
        {initial}
      </div>

      {/* Name + contact badge */}
      <div className="text-center min-w-0 w-full">
        <p className="text-sm font-medium text-neutral-200 truncate">
          {peer.display_name || 'Unknown'}
        </p>
        {peer.is_contact && (
          <span className="text-[10px] text-blue-400">Contact</span>
        )}
      </div>

      {/* Send button */}
      <button
        onClick={() => onSend(peer)}
        className="mt-1 flex items-center gap-1.5 px-3 py-1.5 rounded-lg text-xs font-medium transition-colors
          bg-blue-600/15 border border-blue-500/30 text-blue-300 hover:bg-blue-600/25"
      >
        <IconUpload />
        Send
      </button>
    </div>
  )
}

// ---------------------------------------------------------------------------
// Transfer progress row
// ---------------------------------------------------------------------------

function TransferRow({ transfer }) {
  const statusColor =
    transfer.status === 'done'     ? 'text-emerald-400' :
    transfer.status === 'error'    ? 'text-red-400' :
    transfer.status === 'sending'  ? 'text-blue-400' : 'text-neutral-500'

  const statusLabel =
    transfer.status === 'done'    ? 'Complete' :
    transfer.status === 'error'   ? transfer.error || 'Error' :
    transfer.status === 'sending' ? 'Sending…' : 'Pending'

  return (
    <div className="flex items-center gap-3 px-4 py-3 bg-neutral-900/50 border border-neutral-800/50 rounded-xl">
      {/* Spinner or icon */}
      {transfer.status === 'sending' ? (
        <span className="w-4 h-4 spinner shrink-0" />
      ) : transfer.status === 'done' ? (
        <span className="text-emerald-400 shrink-0"><IconCheck /></span>
      ) : (
        <span className="text-red-400 shrink-0"><IconX /></span>
      )}

      {/* Details */}
      <div className="flex-1 min-w-0">
        <p className="text-sm text-neutral-200 truncate">{transfer.fileName}</p>
        <p className="text-[11px] text-neutral-600">
          To {transfer.peerName} · {humanBytes(transfer.fileSize)}
        </p>
      </div>

      <span className={`text-xs shrink-0 ${statusColor}`}>{statusLabel}</span>
    </div>
  )
}

// ---------------------------------------------------------------------------
// Inbound request card
// ---------------------------------------------------------------------------

function InboundCard({ req, onDecide }) {
  const [busy, setBusy] = useState(false)

  const decide = async (accept) => {
    setBusy(true)
    try {
      await apiFetch('/api/peering/drop/decide', {
        method: 'POST',
        body: JSON.stringify({ transfer_id: req.transfer_id, accept }),
      })
      onDecide(req.transfer_id, accept)
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="bg-amber-500/8 border border-amber-500/25 rounded-xl px-4 py-3 space-y-2">
      <div className="flex items-start justify-between gap-2">
        <div className="min-w-0">
          <p className="text-sm font-medium text-neutral-200 truncate">
            {req.display_name || req.from_vula_id} wants to send you a file
          </p>
          <p className="text-xs text-neutral-500 truncate mt-0.5">
            {req.file_name} · {humanBytes(req.file_size)}
          </p>
        </div>
        <span className="text-[10px] px-2 py-0.5 rounded-full bg-amber-500/15 border border-amber-500/30 text-amber-400 shrink-0">
          Incoming
        </span>
      </div>

      <div className="flex gap-2 pt-1">
        <button
          onClick={() => decide(true)}
          disabled={busy}
          className="flex-1 flex items-center justify-center gap-1.5 py-2 rounded-lg text-xs font-medium transition-colors
            bg-emerald-600/15 border border-emerald-500/30 text-emerald-300
            hover:bg-emerald-600/25 disabled:opacity-50"
        >
          <IconCheck />
          Accept
        </button>
        <button
          onClick={() => decide(false)}
          disabled={busy}
          className="flex-1 flex items-center justify-center gap-1.5 py-2 rounded-lg text-xs font-medium transition-colors
            bg-red-600/10 border border-red-500/25 text-red-400
            hover:bg-red-600/20 disabled:opacity-50"
        >
          <IconX />
          Decline
        </button>
      </div>
    </div>
  )
}

// ---------------------------------------------------------------------------
// Proximity code panel (PEER-35)
// ---------------------------------------------------------------------------

const PROX_TABS = ['my-code', 'enter-code']

/**
 * ProximityCodePanel — two-tab UI for cross-network / no-mDNS pairing.
 *
 * My Code tab:   POST /api/peering/drop/code/generate → display 6-digit code
 *                with countdown to expiry (5 min / single-use).
 * Enter Code tab: type a 6-digit code → POST /api/peering/drop/code/redeem
 *                 → connect to the peer.
 */
function ProximityCodePanel({ onPeerResolved }) {
  const [tab, setTab]           = useState('my-code')

  // My Code state
  const [myCode, setMyCode]           = useState(null)   // { code, expires_at }
  const [myCodeSecsLeft, setMyCodeSecsLeft] = useState(0)
  const [generating, setGenerating]   = useState(false)
  const [genError, setGenError]       = useState(null)
  const timerRef                      = useRef(null)

  // Enter Code state
  const [inputCode, setInputCode]     = useState('')
  const [redeeming, setRedeeming]     = useState(false)
  const [redeemError, setRedeemError] = useState(null)
  const [redeemResult, setRedeemResult] = useState(null)  // proxRedeemResponse

  // ── My Code: countdown ────────────────────────────────────────────────────

  useEffect(() => {
    if (!myCode) return
    const tick = () => {
      const secs = Math.max(0, Math.round((new Date(myCode.expires_at) - Date.now()) / 1000))
      setMyCodeSecsLeft(secs)
      if (secs === 0) {
        setMyCode(null)
        clearInterval(timerRef.current)
      }
    }
    tick()
    timerRef.current = setInterval(tick, 1000)
    return () => clearInterval(timerRef.current)
  }, [myCode])

  // ── My Code: generate ─────────────────────────────────────────────────────

  const handleGenerate = async () => {
    setGenerating(true)
    setGenError(null)
    setMyCode(null)
    try {
      const data = await apiFetch('/api/peering/drop/code/generate', { method: 'POST', body: '{}' })
      setMyCode(data)  // { code, expires_at }
    } catch (err) {
      setGenError(err.message)
    } finally {
      setGenerating(false)
    }
  }

  // ── Enter Code: redeem ────────────────────────────────────────────────────

  const handleRedeem = async () => {
    const code = inputCode.replace(/\D/g, '').slice(0, 6)
    if (code.length !== 6) {
      setRedeemError('Enter a 6-digit code')
      return
    }
    setRedeeming(true)
    setRedeemError(null)
    setRedeemResult(null)
    try {
      const data = await apiFetch('/api/peering/drop/code/redeem', {
        method: 'POST',
        body: JSON.stringify({ code }),
      })
      setRedeemResult(data)
      setInputCode('')
      onPeerResolved?.(data)
    } catch (err) {
      setRedeemError(err.message)
    } finally {
      setRedeeming(false)
    }
  }

  // ── Format countdown ──────────────────────────────────────────────────────

  const fmtCountdown = (secs) => {
    const m = Math.floor(secs / 60)
    const s = secs % 60
    return `${m}:${String(s).padStart(2, '0')}`
  }

  // ── Render ────────────────────────────────────────────────────────────────

  return (
    <div className="bg-neutral-900/60 border border-neutral-800/60 rounded-2xl overflow-hidden">
      {/* Tab bar */}
      <div className="flex border-b border-neutral-800/60">
        {PROX_TABS.map(t => (
          <button
            key={t}
            onClick={() => setTab(t)}
            className={`flex-1 py-2.5 text-xs font-medium transition-colors
              ${tab === t
                ? 'text-blue-300 border-b-2 border-blue-500/60 bg-blue-600/5'
                : 'text-neutral-500 hover:text-neutral-400'}`}
          >
            {t === 'my-code' ? 'My Code' : 'Enter Code'}
          </button>
        ))}
      </div>

      <div className="p-4 space-y-4">
        {tab === 'my-code' && (
          <>
            <p className="text-xs text-neutral-500 leading-relaxed">
              Generate a 6-digit code and read it to your peer. Valid for&nbsp;5&nbsp;minutes
              or until first use — works across networks via vulos.org rendezvous.
            </p>

            {myCode ? (
              <div className="space-y-3">
                {/* Big code display */}
                <div className="flex items-center justify-center gap-1.5 py-5">
                  {myCode.code.split('').map((digit, i) => (
                    <span
                      key={i}
                      className="w-10 h-12 flex items-center justify-center rounded-xl
                        bg-neutral-800/80 border border-neutral-700/60
                        text-2xl font-mono font-bold text-blue-200 tracking-widest"
                    >
                      {digit}
                    </span>
                  ))}
                </div>

                {/* Countdown */}
                <div className="flex items-center justify-between text-xs text-neutral-500">
                  <span>Expires in</span>
                  <span className={`font-mono font-medium ${myCodeSecsLeft < 60 ? 'text-amber-400' : 'text-neutral-400'}`}>
                    {fmtCountdown(myCodeSecsLeft)}
                  </span>
                </div>

                {/* Refresh */}
                <button
                  onClick={handleGenerate}
                  disabled={generating}
                  className="w-full py-2 rounded-lg text-xs font-medium transition-colors
                    bg-neutral-800/50 border border-neutral-700/40 text-neutral-400
                    hover:border-neutral-600/60 hover:text-neutral-300 disabled:opacity-40"
                >
                  Generate new code
                </button>
              </div>
            ) : (
              <button
                onClick={handleGenerate}
                disabled={generating}
                className="w-full py-2.5 rounded-xl text-sm font-medium transition-colors
                  bg-blue-600/15 border border-blue-500/30 text-blue-300
                  hover:bg-blue-600/25 disabled:opacity-40 disabled:cursor-not-allowed"
              >
                {generating ? 'Generating…' : 'Generate Code'}
              </button>
            )}

            {genError && <p className="text-xs text-red-400">{genError}</p>}
          </>
        )}

        {tab === 'enter-code' && (
          <>
            <p className="text-xs text-neutral-500 leading-relaxed">
              Enter the 6-digit code shown on your peer&rsquo;s screen to connect
              directly, even across networks.
            </p>

            {/* Code input */}
            <input
              type="text"
              inputMode="numeric"
              maxLength={6}
              placeholder="000000"
              value={inputCode}
              onChange={e => {
                setRedeemError(null)
                setRedeemResult(null)
                setInputCode(e.target.value.replace(/\D/g, '').slice(0, 6))
              }}
              onKeyDown={e => e.key === 'Enter' && handleRedeem()}
              className="w-full px-4 py-3 rounded-xl bg-neutral-800/60 border border-neutral-700/50
                text-center font-mono text-2xl font-bold tracking-[0.35em] text-blue-200
                placeholder:text-neutral-700 placeholder:tracking-widest
                focus:outline-none focus:border-blue-500/60 focus:bg-neutral-800/80 transition-colors"
            />

            <button
              onClick={handleRedeem}
              disabled={redeeming || inputCode.length !== 6}
              className="w-full py-2.5 rounded-xl text-sm font-medium transition-colors
                bg-blue-600/15 border border-blue-500/30 text-blue-300
                hover:bg-blue-600/25 disabled:opacity-40 disabled:cursor-not-allowed"
            >
              {redeeming ? 'Connecting…' : 'Connect'}
            </button>

            {redeemError && <p className="text-xs text-red-400">{redeemError}</p>}

            {redeemResult && (
              <div className="flex items-start gap-2 px-3 py-2.5 rounded-xl
                bg-emerald-500/8 border border-emerald-500/25 text-emerald-300">
                <IconCheck />
                <div className="text-xs leading-relaxed">
                  <p className="font-medium">Peer found</p>
                  <p className="text-emerald-500 font-mono mt-0.5 break-all">
                    {redeemResult.vula_id || redeemResult.owner_addr}
                  </p>
                </div>
              </div>
            )}
          </>
        )}
      </div>
    </div>
  )
}

// ---------------------------------------------------------------------------
// Main DropPanel
// ---------------------------------------------------------------------------

export default function DropPanel({ onPendingCount }) {
  const [peers, setPeers] = useState([])
  const [discoverability, setDiscoverability] = useState('peers')
  const [showSettings, setShowSettings] = useState(false)
  const [transfers, setTransfers] = useState([])   // outbound
  const [inbound, setInbound] = useState([])        // pending inbound requests
  const [loadingPeers, setLoadingPeers] = useState(true)
  const [error, setError] = useState(null)
  const [showProximity, setShowProximity] = useState(false)  // PEER-35
  const fileInputRef = useRef(null)
  const pendingSendTarget = useRef(null)   // DropPeer we want to send to
  const pollRef = useRef(null)

  // ── Fetch nearby peers ────────────────────────────────────────────────────

  const fetchNearby = useCallback(async (silent = false) => {
    if (!silent) setLoadingPeers(true)
    try {
      const data = await apiFetch('/api/peering/drop/nearby')
      setPeers(Array.isArray(data) ? data : [])
      setError(null)
    } catch (err) {
      if (!silent) setError(err.message)
    } finally {
      if (!silent) setLoadingPeers(false)
    }
  }, [])

  // ── Fetch current settings ────────────────────────────────────────────────

  const fetchSettings = useCallback(async () => {
    try {
      const data = await apiFetch('/api/peering/drop/settings')
      if (data?.discoverability) setDiscoverability(data.discoverability)
    } catch {
      // ignore — defaults fine
    }
  }, [])

  // ── Initial load + polling ────────────────────────────────────────────────

  useEffect(() => {
    fetchNearby()
    fetchSettings()
    pollRef.current = setInterval(() => fetchNearby(true), 10_000)
    return () => clearInterval(pollRef.current)
  }, [fetchNearby, fetchSettings])

  // ── Propagate pending count to parent ────────────────────────────────────

  useEffect(() => {
    onPendingCount?.(inbound.length)
  }, [inbound.length, onPendingCount])

  // ── File send flow ────────────────────────────────────────────────────────

  const handleSendClick = (peer) => {
    pendingSendTarget.current = peer
    fileInputRef.current?.click()
  }

  const handleFileSelected = async (e) => {
    const file = e.target.files?.[0]
    e.target.value = ''   // reset so same file can be resent
    if (!file || !pendingSendTarget.current) return

    const peer = pendingSendTarget.current
    pendingSendTarget.current = null

    const txId = `tx-${Date.now()}`
    const newTransfer = {
      id: txId,
      peerName: peer.display_name || peer.vula_id,
      fileName: file.name,
      fileSize: file.size,
      mimeType: file.type || 'application/octet-stream',
      status: 'sending',
      error: null,
    }

    setTransfers(prev => [newTransfer, ...prev.slice(0, 19)])

    try {
      // The backend expects a local absolute path for the file. Since we are
      // running in a browser we cannot supply a true filesystem path; instead
      // we upload the file bytes to a staging endpoint and use the returned
      // path — or fall back to a direct POST with the file path when the peer
      // OS exposes it (Electron/file-URI environments).
      //
      // For the standard browser path we use the media upload endpoint
      // POST /api/peering/media/upload, then pass the returned media_path
      // to the drop/send endpoint.
      let mediaPath = ''
      const uploadForm = new FormData()
      uploadForm.append('file', file)
      const uploadRes = await fetch('/api/peering/media/upload', {
        method: 'POST',
        body: uploadForm,
      })
      if (uploadRes.ok) {
        const uploadData = await uploadRes.json()
        mediaPath = uploadData.path || uploadData.media_path || ''
      }

      if (!mediaPath) {
        throw new Error('Media upload failed — no path returned')
      }

      await apiFetch('/api/peering/drop/send', {
        method: 'POST',
        body: JSON.stringify({
          target_vula_id: peer.vula_id,
          target_addr: peer.addr ? `http://${peer.addr}` : undefined,
          media_path: mediaPath,
          mime_type: newTransfer.mimeType,
        }),
      })

      setTransfers(prev => prev.map(t => t.id === txId ? { ...t, status: 'done' } : t))
    } catch (err) {
      setTransfers(prev => prev.map(t => t.id === txId
        ? { ...t, status: 'error', error: err.message }
        : t
      ))
    }
  }

  // ── Inbound decisions ─────────────────────────────────────────────────────

  const handleDecide = useCallback((transferId, accepted) => {
    setInbound(prev => prev.filter(r => r.transfer_id !== transferId))
    if (accepted) {
      // Add a synthetic "receiving" transfer entry for UI consistency.
      setTransfers(prev => [{
        id: transferId,
        peerName: 'Peer',
        fileName: '…',
        fileSize: 0,
        mimeType: '',
        status: 'done',
        error: null,
      }, ...prev.slice(0, 19)])
    }
  }, [])

  // ── Discoverability saved ─────────────────────────────────────────────────

  const handleDiscSaved = (newVal) => {
    setDiscoverability(newVal)
  }

  // ── Render ────────────────────────────────────────────────────────────────

  const hasActivity = inbound.length > 0 || transfers.length > 0

  return (
    <div className="space-y-5">
      {/* Hidden file picker */}
      <input
        ref={fileInputRef}
        type="file"
        className="hidden"
        onChange={handleFileSelected}
      />

      {/* Header row */}
      <div className="flex items-center justify-between">
        <div className="flex items-center gap-2">
          <span className="text-neutral-500"><IconWifi /></span>
          <p className="text-xs text-neutral-500">LAN-first · falls back to internet</p>
        </div>
        <div className="flex items-center gap-2">
          <DiscoverabilityBadge value={discoverability} />
          <button
            onClick={() => setShowSettings(v => !v)}
            className={`p-1.5 rounded-lg border transition-colors
              ${showSettings
                ? 'bg-neutral-700/40 border-neutral-600/40 text-neutral-300'
                : 'bg-neutral-800/30 border-neutral-700/30 text-neutral-500 hover:text-neutral-400'}`}
            aria-label="Discoverability settings"
          >
            <IconSettings />
          </button>
        </div>
      </div>

      {/* Discoverability settings panel */}
      {showSettings && (
        <DiscoverabilitySettings
          current={discoverability}
          onSave={handleDiscSaved}
          onClose={() => setShowSettings(false)}
        />
      )}

      {/* Inbound requests */}
      {inbound.length > 0 && (
        <div className="space-y-2">
          <h3 className="text-xs font-medium text-neutral-400 uppercase tracking-wider">Incoming</h3>
          {inbound.map(req => (
            <InboundCard key={req.transfer_id} req={req} onDecide={handleDecide} />
          ))}
        </div>
      )}

      {/* Nearby peers */}
      <div>
        <div className="flex items-center justify-between mb-3">
          <h3 className="text-xs font-medium text-neutral-400 uppercase tracking-wider">Nearby</h3>
          <button
            onClick={() => fetchNearby()}
            className="text-[11px] text-neutral-600 hover:text-neutral-400 transition-colors"
          >
            Refresh
          </button>
        </div>

        {error && (
          <p className="text-xs text-red-400 mb-3">{error}</p>
        )}

        {loadingPeers ? (
          <div className="flex justify-center py-10">
            <span className="w-5 h-5 spinner" />
          </div>
        ) : peers.length === 0 ? (
          <div className="flex flex-col items-center gap-3 py-12 text-center">
            <span className="text-neutral-700 opacity-60"><IconWifi /></span>
            <p className="text-sm text-neutral-600">No nearby Vula peers found</p>
            <p className="text-xs text-neutral-700 max-w-xs">
              {discoverability === 'nobody'
                ? 'Your discoverability is set to Nobody — other peers cannot find you either.'
                : 'Make sure the other device is on the same network and has discoverability enabled.'}
            </p>
          </div>
        ) : (
          <div className="grid grid-cols-2 gap-3 sm:grid-cols-3">
            {peers.map(peer => (
              <PeerTile
                key={peer.vula_id}
                peer={peer}
                onSend={handleSendClick}
              />
            ))}
          </div>
        )}
      </div>

      {/* Activity (outbound transfers) */}
      {hasActivity && transfers.length > 0 && (
        <div className="space-y-2">
          <h3 className="text-xs font-medium text-neutral-400 uppercase tracking-wider">Transfers</h3>
          {transfers.map(t => (
            <TransferRow key={t.id} transfer={t} />
          ))}
        </div>
      )}

      {/* Proximity codes (PEER-35) */}
      <div>
        <button
          onClick={() => setShowProximity(v => !v)}
          className="flex items-center gap-1.5 text-xs font-medium text-neutral-500
            hover:text-neutral-400 transition-colors select-none"
          aria-expanded={showProximity}
        >
          <IconHash />
          Proximity Code
          <span className="ml-1 text-[10px] text-neutral-600">
            {showProximity ? '▲' : '▼'}
          </span>
        </button>
        {showProximity && (
          <div className="mt-3">
            <ProximityCodePanel
              onPeerResolved={(peer) => {
                // Add a synthetic nearby peer entry so the user can send a file.
                if (!peer?.vula_id) return
                setPeers(prev => {
                  if (prev.some(p => p.vula_id === peer.vula_id)) return prev
                  return [...prev, {
                    vula_id: peer.vula_id,
                    display_name: peer.vula_id,
                    addr: peer.owner_addr || '',
                    is_contact: false,
                  }]
                })
              }}
            />
          </div>
        )}
      </div>
    </div>
  )
}
