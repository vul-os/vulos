/**
 * SharePeerModal.jsx — "Share to peer" sheet for the Files app.
 *
 * Reuses the existing peering "Drop" transport end to end:
 *   - roster comes from GET /api/peering/drop/nearby (same list the Drop panel
 *     shows — approved contacts on the LAN / reachable via the node),
 *   - the transfer is initiated with POST /api/peering/drop/send, handing the
 *     selected file's absolute path as `media_path`. The DropService then runs
 *     the same signed-URL + content-hash pipeline used by AirDrop-style Drop;
 *     nothing about the wire protocol is reinvented here.
 *
 * Folders are archived to a temp `.tar.gz` first (the Drop transport moves a
 * single blob, not a tree) via the Files exec bridge, then sent.
 *
 * Presentation follows the redesigned OS UI: theme tokens (accent and neutral),
 * light/dark aware, min 44px touch targets, full-screen sheet on phones.
 */
import { useState, useEffect, useCallback } from 'react'
import {
  resolveAbsPath, guessMime, buildSendBody, buildFolderArchiveCommand,
} from './shareToPeer.js'

/* ── Icons (inline SVG — no external deps, matches Drop.jsx) ── */

const IconClose = () => (
  <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" width="16" height="16">
    <line x1="18" y1="6" x2="6" y2="18" strokeLinecap="round" />
    <line x1="6" y1="6" x2="18" y2="18" strokeLinecap="round" />
  </svg>
)

const IconSend = () => (
  <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8" width="15" height="15">
    <line x1="22" y1="2" x2="11" y2="13" strokeLinecap="round" strokeLinejoin="round" />
    <polygon points="22 2 15 22 11 13 2 9 22 2" strokeLinecap="round" strokeLinejoin="round" />
  </svg>
)

const IconCheck = () => (
  <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" width="15" height="15">
    <polyline points="20 6 9 17 4 12" strokeLinecap="round" strokeLinejoin="round" />
  </svg>
)

const IconWifi = () => (
  <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8" width="18" height="18">
    <path d="M5 12.55a11 11 0 0 1 14.08 0" strokeLinecap="round" />
    <path d="M1.42 9a16 16 0 0 1 21.16 0" strokeLinecap="round" />
    <path d="M8.53 16.11a6 6 0 0 1 6.95 0" strokeLinecap="round" />
    <circle cx="12" cy="20" r="1" fill="currentColor" stroke="none" />
  </svg>
)

async function apiJSON(url, options = {}) {
  const res = await fetch(url, {
    headers: { 'Content-Type': 'application/json', ...options.headers },
    ...options,
  })
  if (!res.ok) {
    const text = await res.text().catch(() => '')
    throw new Error(`${res.status} ${text || res.statusText}`)
  }
  return res.json().catch(() => ({}))
}

/**
 * @param {object}   props
 * @param {{name:string, path:string, isDir:boolean}} props.target  file/folder to share
 * @param {string|null} props.home   resolved $HOME for ~-expansion
 * @param {(cmd:string)=>Promise<string>} props.exec  Files exec bridge (for folder tar)
 * @param {()=>void}  props.onClose
 */
export default function SharePeerModal({ target, home, exec, onClose }) {
  const [peers, setPeers] = useState([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState(null)
  // sending: vula_id currently in flight; sent: vula_id -> true; failed: vula_id -> msg
  const [sending, setSending] = useState(null)
  const [sent, setSent] = useState({})
  const [failed, setFailed] = useState({})

  const loadPeers = useCallback(async () => {
    setLoading(true)
    try {
      const data = await apiJSON('/api/peering/drop/nearby')
      setPeers(Array.isArray(data) ? data : [])
      setError(null)
    } catch (err) {
      setError(err.message)
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => { loadPeers() }, [loadPeers])

  // Close on Escape.
  useEffect(() => {
    const onKey = (e) => { if (e.key === 'Escape') onClose() }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [onClose])

  const share = useCallback(async (peer) => {
    if (sending) return
    setSending(peer.vula_id)
    setFailed(prev => { const n = { ...prev }; delete n[peer.vula_id]; return n })
    try {
      const absPath = resolveAbsPath(target.path, home)
      let mediaPath = absPath
      let mime = guessMime(target.name)

      // Folders can't be dropped as-is — archive first via the Files exec bridge.
      if (target.isDir) {
        if (typeof exec !== 'function') {
          throw new Error('Cannot archive folder in this environment')
        }
        const { command, archivePath } = buildFolderArchiveCommand(absPath)
        const out = await exec(command)
        if (!String(out).includes('OK')) {
          throw new Error('Could not archive folder')
        }
        mediaPath = archivePath
        mime = 'application/gzip'
      }

      await apiJSON('/api/peering/drop/send', {
        method: 'POST',
        body: JSON.stringify(buildSendBody(peer, mediaPath, mime)),
      })
      setSent(prev => ({ ...prev, [peer.vula_id]: true }))
    } catch (err) {
      setFailed(prev => ({ ...prev, [peer.vula_id]: err.message }))
    } finally {
      setSending(null)
    }
  }, [sending, target, home, exec])

  return (
    <div
      className="fixed inset-0 z-[9998] flex items-center justify-center p-0 sm:p-4 bg-black/50 backdrop-blur-sm"
      onPointerDown={onClose}
    >
      <div
        className="w-full h-full sm:h-auto sm:max-w-md sm:max-h-[80vh] flex flex-col
          bg-neutral-900/95 border border-neutral-800/70 sm:rounded-2xl overflow-hidden
          shadow-2xl shadow-black/60 anim-sheet-up"
        onPointerDown={(e) => e.stopPropagation()}
      >
        {/* Header */}
        <div className="flex items-center justify-between px-4 py-3 border-b border-neutral-800/60 shrink-0">
          <div className="min-w-0">
            <h2 className="text-sm font-semibold text-neutral-200">Share to peer</h2>
            <p className="text-[12px] text-neutral-500 truncate mt-0.5">
              {target.isDir ? 'Folder' : 'File'} · {target.name}
            </p>
          </div>
          <button
            onClick={onClose}
            className="inline-flex items-center justify-center min-w-[40px] min-h-[40px] -mr-1 rounded-lg text-neutral-500 hover:text-neutral-300 transition-colors"
            aria-label="Close"
          >
            <IconClose />
          </button>
        </div>

        {/* Body */}
        <div className="flex-1 overflow-y-auto p-4 space-y-2">
          <div className="flex items-center justify-between">
            <h3 className="text-[12px] font-medium text-neutral-400 uppercase tracking-wider">
              Nearby peers
            </h3>
            <button
              onClick={loadPeers}
              className="px-2 py-1 -mr-2 text-[12px] text-neutral-500 hover:text-neutral-300 transition-colors"
            >
              Refresh
            </button>
          </div>

          {error && <p className="text-xs text-danger">{error}</p>}

          {loading ? (
            <div className="flex justify-center py-10">
              <span className="w-5 h-5 spinner" aria-hidden="true" />
            </div>
          ) : peers.length === 0 ? (
            <div className="flex flex-col items-center gap-3 px-6 py-10 text-center rounded-2xl border border-dashed border-neutral-800/70 bg-neutral-900/30">
              <span className="flex items-center justify-center w-12 h-12 rounded-full accent-bg-soft accent-text">
                <IconWifi />
              </span>
              <p className="text-sm text-neutral-400">No peers available</p>
              <p className="text-xs text-neutral-600 max-w-xs leading-relaxed">
                Approve a contact and make sure they are reachable, then refresh.
              </p>
            </div>
          ) : (
            <ul className="space-y-1.5">
              {peers.map(peer => {
                const isSent = sent[peer.vula_id]
                const isSending = sending === peer.vula_id
                const fail = failed[peer.vula_id]
                const initial = (peer.display_name || peer.vula_id || '?')[0].toUpperCase()
                return (
                  <li key={peer.vula_id}>
                    <button
                      onClick={() => share(peer)}
                      disabled={isSending || isSent || !!sending}
                      className="w-full flex items-center gap-3 px-3 py-2.5 rounded-xl text-left transition-colors
                        bg-neutral-800/40 border border-neutral-700/40
                        hover:border-neutral-600/60 hover:bg-neutral-800/70
                        disabled:cursor-not-allowed disabled:hover:bg-neutral-800/40"
                    >
                      <span className={`w-9 h-9 shrink-0 rounded-full flex items-center justify-center text-sm font-bold
                        ${peer.is_contact ? 'accent-bg-soft accent-border accent-text' : 'bg-neutral-800/70 border border-neutral-700/50 text-neutral-300'}`}>
                        {initial}
                      </span>
                      <span className="flex-1 min-w-0">
                        <span className="block text-sm text-neutral-200 truncate">
                          {peer.display_name || 'Unknown'}
                        </span>
                        {fail
                          ? <span className="block text-[12px] text-danger truncate">{fail}</span>
                          : peer.is_contact
                            ? <span className="block text-[12px] accent-text">Contact</span>
                            : <span className="block text-[12px] text-neutral-600 truncate">{peer.vula_id}</span>}
                      </span>
                      <span className="shrink-0 flex items-center justify-center w-8 h-8 rounded-lg accent-text">
                        {isSending
                          ? <span className="w-4 h-4 spinner" aria-hidden="true" />
                          : isSent
                            ? <span className="text-success"><IconCheck /></span>
                            : <IconSend />}
                      </span>
                    </button>
                  </li>
                )
              })}
            </ul>
          )}
        </div>

        {/* Footer note */}
        <div className="px-4 py-2.5 border-t border-neutral-800/60 shrink-0">
          <p className="text-[12px] text-neutral-600 leading-relaxed">
            End-to-end over your peering transport. The recipient must accept;
            the file then lands in their Downloads.
          </p>
        </div>
      </div>
    </div>
  )
}
