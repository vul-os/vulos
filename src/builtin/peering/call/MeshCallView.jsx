/**
 * MeshCallView — full-mesh group A/V call UI for 3–4 participants (PEER-26).
 *
 * Renders a CSS-grid video/audio tile layout for up to 4 participants.
 * Each tile shows:
 *   - A live <video> element (muted for the local tile, auto-play for remote)
 *   - Peer's short ID as a label
 *   - Connection state badge (connecting / connected / disconnected)
 *   - Upload bandwidth hint from the bw probe
 *
 * The SFU recommendation prompt appears as a dismissible banner when
 * useMeshCall fires onSFURecommend.
 *
 * Props:
 *   roomId            {string}   — unique room identifier
 *   localPeerId       {string}   — this user's Vula peer ID
 *   iceServers        {object[]} — RTCIceServer list (optional)
 *   onClose           {function} — called when the user leaves and closes
 */
import { useState, useRef, useCallback, useEffect } from 'react'
import { useMeshCall } from './useMeshCall'

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

function shortId(id) {
  if (!id) return '?'
  const parts = id.split(':')
  const key = parts[parts.length - 1]
  return key.slice(0, 8)
}

function bwLabel(bw) {
  if (!bw) return null
  if (bw.upload_kbps > 0) {
    const mbps = (bw.upload_kbps / 1000).toFixed(1)
    return `↑${mbps} Mbps`
  }
  return null
}

function stateBadge(state) {
  switch (state) {
    case 'connected':    return 'bg-green-500'
    case 'disconnected': return 'bg-yellow-500'
    case 'failed':       return 'bg-red-500'
    default:             return 'bg-neutral-500 animate-pulse'
  }
}

// ---------------------------------------------------------------------------
// VideoTile — one participant tile
// ---------------------------------------------------------------------------

function VideoTile({ stream, label, state, bw, isSelf = false }) {
  const videoRef = useRef(null)

  useEffect(() => {
    const el = videoRef.current
    if (!el) return
    if (stream) {
      el.srcObject = stream
    } else {
      el.srcObject = null
    }
  }, [stream])

  return (
    <div className="relative bg-neutral-800 rounded-xl overflow-hidden flex items-center justify-center aspect-video">
      <video
        ref={videoRef}
        autoPlay
        playsInline
        muted={isSelf}
        className="w-full h-full object-cover"
      />

      {/* Fallback avatar when no video */}
      {!stream && (
        <div className="absolute inset-0 flex flex-col items-center justify-center gap-2">
          <div className="w-14 h-14 rounded-full bg-neutral-700 flex items-center justify-center text-xl font-semibold text-neutral-300 ring-2 ring-neutral-600">
            {shortId(label)[0]?.toUpperCase() ?? '?'}
          </div>
          <span className="text-xs text-neutral-500">No video</span>
        </div>
      )}

      {/* Bottom info bar */}
      <div className="absolute bottom-0 left-0 right-0 flex items-center justify-between px-2 py-1 bg-black/50 text-xs">
        <span className="text-neutral-200 truncate">{shortId(label)}{isSelf ? ' (you)' : ''}</span>
        <div className="flex items-center gap-1.5">
          {bwLabel(bw) && (
            <span className="text-neutral-400">{bwLabel(bw)}</span>
          )}
          <span className={`w-2 h-2 rounded-full ${stateBadge(state)}`} />
        </div>
      </div>
    </div>
  )
}

// ---------------------------------------------------------------------------
// SFU recommendation banner
// ---------------------------------------------------------------------------

function SFUBanner({ recommendation, onDismiss }) {
  if (!recommendation) return null
  return (
    <div className="flex items-start gap-3 bg-yellow-900/80 border border-yellow-700 text-yellow-100 text-xs rounded-lg px-3 py-2 mx-4 mt-2">
      <span className="text-lg leading-none mt-0.5">⚠️</span>
      <div className="flex-1">
        <p className="font-medium mb-0.5">Low bandwidth detected</p>
        <p className="text-yellow-300">{recommendation.message}</p>
      </div>
      <button
        onClick={onDismiss}
        className="text-yellow-400 hover:text-yellow-200 leading-none text-base"
        aria-label="Dismiss"
      >
        ×
      </button>
    </div>
  )
}

// ---------------------------------------------------------------------------
// Grid layout helper — mirrors useMeshCall computeGridLayout
// ---------------------------------------------------------------------------

function gridStyle(cols) {
  return { gridTemplateColumns: `repeat(${cols}, minmax(0, 1fr))` }
}

// ---------------------------------------------------------------------------
// Main component
// ---------------------------------------------------------------------------

export default function MeshCallView({
  roomId = '',
  localPeerId = '',
  iceServers,
  onClose,
}) {
  const [sfuRecommendation, setSFURecommendation] = useState(null)
  const [dismissed, setDismissed] = useState(false)

  const onSFURecommend = useCallback((rec) => {
    setDismissed(false)
    setSFURecommendation(rec)
  }, [])

  const {
    localStream,
    peers,
    gridLayout,
    joined,
    muted,
    cameraOff,
    join,
    leave,
    toggleMute,
    toggleCamera,
  } = useMeshCall({
    roomId,
    localPeerId,
    iceServers,
    onSFURecommend,
  })

  // Tiles: local + remote peers
  const totalTiles = peers.size + (joined ? 1 : 0)

  // ---------------------------------------------------------------------------
  // Layout: pre-join lobby
  // ---------------------------------------------------------------------------
  if (!joined) {
    return (
      <div className="flex flex-col h-full bg-neutral-900 text-white select-none">
        {/* Header */}
        <div className="flex items-center justify-between px-4 py-3 border-b border-neutral-800">
          <h2 className="text-sm font-semibold text-neutral-200">Group Call</h2>
          {onClose && (
            <button
              onClick={onClose}
              className="text-neutral-500 hover:text-neutral-200 transition-colors text-lg leading-none"
              aria-label="Close"
            >
              ×
            </button>
          )}
        </div>

        <div className="flex-1 flex flex-col items-center justify-center gap-6 px-6">
          <div className="text-center space-y-1">
            <p className="text-neutral-300 font-medium">Room: <span className="font-mono text-neutral-100">{roomId || '—'}</span></p>
            <p className="text-neutral-500 text-sm">Up to 4 participants · full-mesh A/V</p>
          </div>

          <button
            onClick={join}
            disabled={!roomId}
            className="flex items-center gap-2 bg-green-600 hover:bg-green-500 disabled:opacity-40 disabled:cursor-not-allowed transition-colors text-white font-medium px-8 py-3 rounded-full text-sm"
          >
            <span>📹</span> Join Call
          </button>

          {localPeerId && (
            <p className="text-xs text-neutral-600 text-center break-all max-w-xs">
              Your peer ID: <span className="text-neutral-500 font-mono">{localPeerId}</span>
            </p>
          )}
        </div>
      </div>
    )
  }

  // ---------------------------------------------------------------------------
  // Layout: in-call grid
  // ---------------------------------------------------------------------------
  return (
    <div className="flex flex-col h-full bg-neutral-900 text-white select-none">
      {/* Header */}
      <div className="flex items-center justify-between px-4 py-2 border-b border-neutral-800 shrink-0">
        <div className="flex items-center gap-2">
          <span className="w-2 h-2 rounded-full bg-green-400 animate-pulse" />
          <h2 className="text-sm font-semibold text-neutral-200">Group Call</h2>
          <span className="text-xs text-neutral-500">
            {totalTiles} participant{totalTiles !== 1 ? 's' : ''}
          </span>
        </div>
        <span className="text-xs text-neutral-600 font-mono truncate max-w-[8rem]">{roomId}</span>
      </div>

      {/* SFU recommendation banner */}
      {sfuRecommendation && !dismissed && (
        <SFUBanner
          recommendation={sfuRecommendation}
          onDismiss={() => setDismissed(true)}
        />
      )}

      {/* Video grid */}
      <div
        className="flex-1 grid gap-1 p-2 overflow-hidden"
        style={gridStyle(gridLayout.cols)}
      >
        {/* Local tile */}
        <VideoTile
          stream={localStream}
          label={localPeerId}
          state="connected"
          bw={null}
          isSelf
        />

        {/* Remote peer tiles */}
        {Array.from(peers.entries()).map(([peerId, entry]) => (
          <VideoTile
            key={peerId}
            stream={entry.stream}
            label={peerId}
            state={entry.state}
            bw={entry.bandwidth}
          />
        ))}
      </div>

      {/* Control bar */}
      <div className="shrink-0 flex items-center justify-center gap-4 px-4 py-3 border-t border-neutral-800">
        {/* Mute */}
        <button
          onClick={toggleMute}
          className={`flex flex-col items-center gap-1 px-4 py-2 rounded-xl transition-colors ${
            muted
              ? 'bg-yellow-700 hover:bg-yellow-600'
              : 'bg-neutral-700 hover:bg-neutral-600'
          }`}
        >
          <span className="text-2xl leading-none">{muted ? '🔇' : '🎙'}</span>
          <span className="text-xs text-neutral-300 font-medium">{muted ? 'Unmute' : 'Mute'}</span>
        </button>

        {/* Camera */}
        <button
          onClick={toggleCamera}
          className={`flex flex-col items-center gap-1 px-4 py-2 rounded-xl transition-colors ${
            cameraOff
              ? 'bg-yellow-700 hover:bg-yellow-600'
              : 'bg-neutral-700 hover:bg-neutral-600'
          }`}
        >
          <span className="text-2xl leading-none">{cameraOff ? '📷' : '📹'}</span>
          <span className="text-xs text-neutral-300 font-medium">{cameraOff ? 'Cam On' : 'Cam Off'}</span>
        </button>

        {/* Leave */}
        <button
          onClick={() => { leave(); onClose?.() }}
          className="flex flex-col items-center gap-1 px-4 py-2 rounded-xl transition-colors bg-red-700 hover:bg-red-600"
        >
          <span className="text-2xl leading-none">📵</span>
          <span className="text-xs text-neutral-300 font-medium">Leave</span>
        </button>
      </div>
    </div>
  )
}
